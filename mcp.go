package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const mcpCloudURL = "https://solar-assistant.io/mcp"

var mcpLocalClient = &http.Client{Timeout: 5 * time.Second}
var mcpProxyClient = &http.Client{Timeout: 10 * time.Second}
var mcpCloudClient = &http.Client{Timeout: 10 * time.Second}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	ID      any             `json:"id,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   *mcpError `json:"error,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpToolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func runMCP(args []string) {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Println(`Usage: sacli mcp

Run sacli as a stdio MCP server. Requires a cloud API key (run: sacli configure).

Site tools are routed directly to the SolarAssistant unit on the local
network or via regional proxy for lower latency.

Claude Desktop (~/.config/claude/claude_desktop_config.json), Cursor (~/.cursor/mcp.json):
  {
    "mcpServers": {
      "solar-assistant": {
        "command": "sacli",
        "args": ["mcp"]
      }
    }
  }`)
		return
	}
	cfg, err := loadConfig()
	if err != nil || cfg.CloudAPIKey == "" {
		fmt.Fprintln(os.Stderr, "error: no API key configured — run: sacli configure")
		os.Exit(1)
	}

	// Fetch and cache tool list from cloud on startup. Fatal if unreachable.
	toolList, err := mcpFetchToolList(cfg.CloudAPIKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: could not fetch MCP tool list from cloud: %v\n", err)
		os.Exit(1)
	}

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		var req mcpRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			writeJSON(mcpResponse{
				JSONRPC: "2.0",
				Error:   &mcpError{Code: -32700, Message: "parse error"},
			})
			continue
		}

		switch req.Method {
		case "initialize":
			writeJSON(mcpResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]any{
					"protocolVersion": "2024-11-05",
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "sacli", "version": version},
				},
			})

		case "notifications/initialized":
			// no response

		case "tools/list":
			writeJSON(mcpResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  toolList,
			})

		case "tools/call":
			var call mcpToolCall
			if err := json.Unmarshal(req.Params, &call); err != nil {
				writeJSON(mcpResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error:   &mcpError{Code: -32602, Message: "invalid params"},
				})
				continue
			}
			result, mcpErr := mcpDispatch(cfg.CloudAPIKey, call)
			if mcpErr != nil {
				writeJSON(mcpResponse{JSONRPC: "2.0", ID: req.ID, Error: mcpErr})
			} else {
				writeJSON(mcpResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
			}

		default:
			writeJSON(mcpResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &mcpError{Code: -32601, Message: "method not found"},
			})
		}
	}
}

// mcpDispatch routes a tool call to the appropriate destination.
func mcpDispatch(apiKey string, call mcpToolCall) (any, *mcpError) {
	if strings.HasPrefix(call.Name, "site_") {
		siteID := siteIDFromArgs(call.Arguments)
		if siteID != 0 {
			auth := authorizeWithCache(siteID)
			return mcpCallSite(apiKey, auth, call)
		}
	}
	return mcpCallCloud(apiKey, call)
}

// mcpCallSite forwards a site_ tool call, trying local network first,
// then the regional proxy, then falling back to cloud.
func mcpCallSite(apiKey string, auth CachedAuthorize, call mcpToolCall) (any, *mcpError) {
	localName := strings.TrimPrefix(call.Name, "site_")
	args := make(map[string]any, len(call.Arguments))
	for k, v := range call.Arguments {
		if k != "site_id" {
			args[k] = v
		}
	}

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "tools/call",
		"id":      1,
		"params":  map[string]any{"name": localName, "arguments": args},
	})
	if err != nil {
		return nil, &mcpError{Code: -32603, Message: err.Error()}
	}

	// Try local network first (skipped if recently failed via cloud-resolved auth).
	if auth.LocalIP != "" && !localIPRecentlyFailed(auth) && isLocallyReachable(auth.LocalIP) {
		req, _ := http.NewRequest("POST", "http://"+auth.LocalIP+"/mcp", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+auth.Token)
		if result, mcpErr, ok := mcpDoUnitRequest(auth, req, mcpLocalClient); ok {
			markLocalIPSucceeded(auth.SiteID)
			return result, mcpErr
		}
		reachabilityCache[auth.LocalIP] = reachabilityEntry{reachable: false, checkedAt: time.Now()}
		markLocalIPFailed(auth.SiteID)
	}

	// Try regional proxy.
	if auth.Host != "" {
		req, _ := http.NewRequest("POST", "https://"+auth.Host+"/api/v1/mcp", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+auth.Token)
		req.Header.Set("site-id", fmt.Sprintf("%d", auth.SiteID))
		req.Header.Set("site-key", auth.SiteKey)
		if result, mcpErr, ok := mcpDoUnitRequest(auth, req, mcpProxyClient); ok {
			return result, mcpErr
		}
	}

	// Fall back to cloud.
	return mcpCallCloud(apiKey, call)
}

// mcpDoUnitRequest executes a prepared request to a unit (local or proxy).
// Returns (result, error, true) if the request completed (even on HTTP error),
// or (nil, nil, false) if the request failed at the transport level (unreachable).
func mcpDoUnitRequest(auth CachedAuthorize, req *http.Request, client *http.Client) (any, *mcpError, bool) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, false
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case 200, 201:
		var rpc struct {
			Result any       `json:"result"`
			Error  *mcpError `json:"error"`
		}
		json.Unmarshal(respBody, &rpc)
		if rpc.Error != nil {
			return nil, rpc.Error, true
		}
		return rpc.Result, nil, true
	case 502, 503:
		return nil, nil, false
	case 404:
		return nil, &mcpError{Code: -32603, Message: fmt.Sprintf(
			"Site #%d may be running an outdated version (requires build 2026-03-24 or later).",
			auth.SiteID,
		)}, true
	default:
		return nil, &mcpError{Code: -32603, Message: fmt.Sprintf(
			"Site #%d returned HTTP %d: %s",
			auth.SiteID, resp.StatusCode, strings.TrimSpace(string(respBody)),
		)}, true
	}
}

// mcpCallCloud forwards a tool call to the cloud MCP endpoint.
func mcpCallCloud(apiKey string, call mcpToolCall) (any, *mcpError) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "tools/call",
		"id":      1,
		"params":  map[string]any{"name": call.Name, "arguments": call.Arguments},
	})
	if err != nil {
		return nil, &mcpError{Code: -32603, Message: err.Error()}
	}

	req, _ := http.NewRequest("POST", mcpCloudURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := mcpCloudClient.Do(req)
	if err != nil {
		return nil, &mcpError{Code: -32603, Message: fmt.Sprintf("cloud unreachable: %v", err)}
	}
	defer resp.Body.Close()

	var rpc struct {
		Result any       `json:"result"`
		Error  *mcpError `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&rpc)
	if rpc.Error != nil {
		return nil, rpc.Error
	}
	return rpc.Result, nil
}

// mcpFetchToolList fetches the tools/list from the cloud and returns the result object.
func mcpFetchToolList(apiKey string) (any, error) {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "tools/list",
		"id":      1,
	})
	req, _ := http.NewRequest("POST", mcpCloudURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := mcpCloudClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var rpc struct {
		Result any `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpc); err != nil {
		return nil, err
	}
	return rpc.Result, nil
}

func siteIDFromArgs(args map[string]any) int {
	v, ok := args["site_id"]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

type reachabilityEntry struct {
	reachable bool
	checkedAt time.Time
}

var reachabilityCache = map[string]reachabilityEntry{}

const reachabilityTTL = 15 * time.Minute

func isLocallyReachable(localIP string) bool {
	if entry, ok := reachabilityCache[localIP]; ok && time.Since(entry.checkedAt) < reachabilityTTL {
		return entry.reachable
	}
	host := localIP
	if !strings.Contains(host, ":") {
		host += ":80"
	}
	conn, err := net.DialTimeout("tcp", host, 500*time.Millisecond)
	reachable := err == nil
	if reachable {
		conn.Close()
	}
	reachabilityCache[localIP] = reachabilityEntry{reachable: reachable, checkedAt: time.Now()}
	return reachable
}

func writeJSON(v any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}
