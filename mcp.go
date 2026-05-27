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
	"path/filepath"
	"strings"
	"sync"
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
		fmt.Println(`Usage: sacli mcp [--list] [--tools pattern,...] [--http [port]] [--stdio]

Run sacli as an MCP server. Requires a cloud API key (run: sacli configure).

  --list              List available tools and exit
  --tools pattern,... Only expose tools matching these names or glob patterns
                      Example: --tools site_status,site_get_*
  --http [port]       Serve over HTTP (default port: 3005)
  --stdio             Serve over stdio (default when --http is not given)

stdio — Claude Desktop (~/.config/claude/claude_desktop_config.json), Cursor (~/.cursor/mcp.json):
  {
    "mcpServers": {
      "solar-assistant": {
        "command": "sacli",
        "args": ["mcp"]
      }
    }
  }

HTTP — start the server first: sacli mcp --http
Then point your client at it:
  {
    "mcpServers": {
      "solar-assistant": {
        "type": "http",
        "url": "http://localhost:3005"
      }
    }
  }

--stdio and --http can be combined to run both transports at once,
though this is only useful in advanced setups.`)
		return
	}

	listMode, args := extractFlag(args, "--list")
	stdioFlag, args := extractFlag(args, "--stdio")

	httpMode := false
	httpPort := "3005"
	var rest []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--http" {
			httpMode = true
			if i+1 < len(args) && isPortStr(args[i+1]) {
				httpPort = args[i+1]
				i++
			}
		} else {
			rest = append(rest, args[i])
		}
	}
	args = rest

	stdioMode := stdioFlag || !httpMode

	toolsValues, _ := extractStringFlag(args, "--tools")
	var toolPatterns []string
	for _, v := range toolsValues {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				toolPatterns = append(toolPatterns, p)
			}
		}
	}

	cfg, err := loadConfig()
	if err != nil || cfg.CloudAPIKey == "" {
		fmt.Fprintln(os.Stderr, "error: no API key configured — run: sacli configure")
		os.Exit(1)
	}

	cachePath, _ := mcpToolsCachePath()
	hasCachedTools := cachePath != "" && fileExists(cachePath)

	timeout := 5 * time.Second
	if hasCachedTools {
		timeout = 3 * time.Second
	}

	toolList, err := mcpFetchToolList(cfg.CloudAPIKey, timeout)
	if err != nil {
		if !hasCachedTools {
			fmt.Fprintf(os.Stderr, "error: could not fetch MCP tool list from cloud: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "warning: could not fetch MCP tool list from cloud (%v), using cached tools\n", err)
		toolList, err = mcpLoadCachedToolList(cachePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: could not read cached tool list: %v\n", err)
			os.Exit(1)
		}
	} else if cachePath != "" {
		mcpSaveToolList(cachePath, toolList)
	}

	if len(toolPatterns) > 0 {
		toolList = mcpFilterTools(toolList, toolPatterns)
	}

	if listMode {
		mcpPrintToolList(toolList)
		return
	}

	if httpMode {
		if stdioMode {
			go func() {
				if err := runMCPHTTP(httpPort, cfg, toolList, toolPatterns); err != nil {
					fmt.Fprintf(os.Stderr, "warning: MCP HTTP server on :%s unavailable: %v\n", httpPort, err)
				}
			}()
		} else {
			if err := runMCPHTTP(httpPort, cfg, toolList, toolPatterns); err != nil {
				fatal(err)
			}
			return
		}
	}

	if stdioMode {
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
			resp := processMCPRequest(req, toolList, cfg.CloudAPIKey, toolPatterns)
			if resp != nil {
				writeJSON(*resp)
			}
		}
	}
}

func processMCPRequest(req mcpRequest, toolList any, apiKey string, toolPatterns []string) *mcpResponse {
	switch req.Method {
	case "initialize":
		return &mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "sacli", "version": version},
			},
		}
	case "notifications/initialized":
		return nil
	case "tools/list":
		return &mcpResponse{JSONRPC: "2.0", ID: req.ID, Result: toolList}
	case "tools/call":
		var call mcpToolCall
		if err := json.Unmarshal(req.Params, &call); err != nil {
			return &mcpResponse{JSONRPC: "2.0", ID: req.ID, Error: &mcpError{Code: -32602, Message: "invalid params"}}
		}
		if len(toolPatterns) > 0 && !matchesAny(call.Name, toolPatterns) {
			return &mcpResponse{JSONRPC: "2.0", ID: req.ID, Error: &mcpError{Code: -32602, Message: "tool not found: " + call.Name}}
		}
		result, mcpErr := mcpDispatch(apiKey, call)
		if mcpErr != nil {
			return &mcpResponse{JSONRPC: "2.0", ID: req.ID, Error: mcpErr}
		}
		return &mcpResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
	default:
		return &mcpResponse{JSONRPC: "2.0", ID: req.ID, Error: &mcpError{Code: -32601, Message: "method not found"}}
	}
}

func runMCPHTTP(port string, cfg *Config, toolList any, toolPatterns []string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req mcpRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(mcpResponse{JSONRPC: "2.0", Error: &mcpError{Code: -32700, Message: "parse error"}})
			return
		}
		resp := processMCPRequest(req, toolList, cfg.CloudAPIKey, toolPatterns)
		if resp == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(*resp)
	})
	addr := ":" + port
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "MCP HTTP server listening on %s\n", addr)
	return http.Serve(ln, mux)
}

func isPortStr(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func mcpExtractTools(toolList any) []map[string]any {
	m, ok := toolList.(map[string]any)
	if !ok {
		return nil
	}
	raw, _ := m["tools"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, t := range raw {
		if tool, ok := t.(map[string]any); ok {
			out = append(out, tool)
		}
	}
	return out
}

func mcpPrintToolList(toolList any) {
	tools := mcpExtractTools(toolList)
	maxLen := 0
	for _, t := range tools {
		if n, _ := t["name"].(string); len(n) > maxLen {
			maxLen = len(n)
		}
	}
	for _, t := range tools {
		name, _ := t["name"].(string)
		desc, _ := t["description"].(string)
		fmt.Printf("%-*s  %s\n", maxLen, name, desc)
	}
}

func mcpFilterTools(toolList any, patterns []string) any {
	m, ok := toolList.(map[string]any)
	if !ok {
		return toolList
	}
	tools := mcpExtractTools(toolList)
	filtered := make([]any, 0, len(tools))
	for _, t := range tools {
		name, _ := t["name"].(string)
		if matchesAny(name, patterns) {
			filtered = append(filtered, t)
		}
	}
	result := make(map[string]any, len(m))
	for k, v := range m {
		result[k] = v
	}
	result["tools"] = filtered
	return result
}

// mcpDispatch routes a tool call to the appropriate destination.
func mcpDispatch(apiKey string, call mcpToolCall) (any, *mcpError) {
	if strings.HasPrefix(call.Name, "site_") {
		siteID := siteIDFromArgs(call.Arguments)
		if siteID != 0 {
			auth, err := authorizeWithCache(siteID)
			if err != nil {
				return nil, &mcpError{Code: -32603, Message: err.Error()}
			}
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
		reachabilityMu.Lock()
		reachabilityCache[auth.LocalIP] = reachabilityEntry{reachable: false, checkedAt: time.Now()}
		reachabilityMu.Unlock()
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
func mcpFetchToolList(apiKey string, timeout time.Duration) (any, error) {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "tools/list",
		"id":      1,
	})
	client := &http.Client{Timeout: timeout}
	req, _ := http.NewRequest("POST", mcpCloudURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
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

func mcpSaveToolList(path string, toolList any) {
	data, err := json.Marshal(toolList)
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(path), 0700)
	os.WriteFile(path, data, 0600)
}

func mcpLoadCachedToolList(path string) (any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var toolList any
	if err := json.Unmarshal(data, &toolList); err != nil {
		return nil, err
	}
	return toolList, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
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
var reachabilityMu sync.Mutex

const reachabilityTTL = 15 * time.Minute

func isLocallyReachable(localIP string) bool {
	reachabilityMu.Lock()
	entry, ok := reachabilityCache[localIP]
	reachabilityMu.Unlock()
	if ok && time.Since(entry.checkedAt) < reachabilityTTL {
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
	reachabilityMu.Lock()
	reachabilityCache[localIP] = reachabilityEntry{reachable: reachable, checkedAt: time.Now()}
	reachabilityMu.Unlock()
	return reachable
}

func writeJSON(v any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}
