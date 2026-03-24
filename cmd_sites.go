package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	sa "solar_assistant"
	v1 "solar_assistant/api/v1"
)

// ── sites command ─────────────────────────────────────────────────────────────

func runSites(args []string) {
	if len(args) > 0 && args[0] == "authorize" {
		runSitesAuthorize(args[1:])
		return
	}

	jsonOut, args := extractFlag(args, "--json")

	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Println(`Usage: sacli sites [--json] [key:value ...]

List and filter sites. All key:value arguments are passed as the search query.
Use limit:N and offset:N for pagination.

Examples:
  sacli sites
  sacli sites name:my-site
  sacli sites inverter:srne
  sacli sites inverter_params_output_power:5000 inverter:growatt
  sacli sites last_seen_after:2026-01-01 build_date_after:2026-02-26
  sacli sites inverter:srne limit:50 offset:20
  sacli sites --json name:my-site`)
		return
	}

	result, err := v1.ListSites(newClient(), parseQuery(args))
	if err != nil {
		fatal(err)
	}

	if len(result) == 0 {
		fmt.Println("No sites found.")
		return
	}

	if jsonOut {
		out, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(out))
		return
	}

	printSites(result)
}

func runSitesAuthorize(args []string) {
	jsonOut, args := extractFlag(args, "--json")

	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: sacli sites authorize <site_id>")
		os.Exit(1)
	}

	var siteID int
	if _, err := fmt.Sscanf(args[0], "%d", &siteID); err != nil {
		fatal(fmt.Errorf("invalid site ID: %s", args[0]))
	}

	result, err := v1.AuthorizeSite(newClient(), siteID)
	if err != nil {
		fatal(err)
	}

	if jsonOut {
		out, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(out))
		return
	}

	printAuthorize(result)
}

func printSites(sites []v1.Site) {
	for _, s := range sites {
		fmt.Printf("Site ID:      %d\n", s.ID)
		fmt.Printf("Name:         %s\n", s.Name)
		fmt.Printf("Inverter:     %s (x%d)\n", strOr(s.Inverter, "unknown"), max1(s.InverterCount))
		fmt.Printf("Inv params:   %s\n", fmtParams(s.InverterParams))
		fmt.Printf("Battery:      %s (x%d)\n", strOr(s.Battery, "unknown"), max1(s.BatteryCount))
		fmt.Printf("Bat params:   %s\n", fmtParams(s.BatteryParams))
		fmt.Printf("Proxy:        %s\n", strOr(s.Proxy, "none"))
		fmt.Printf("Web port:     %v\n", anyOr(s.WebPort, "none"))
		fmt.Printf("SSH port:     %v\n", anyOr(s.SSHPort, "none"))
		fmt.Printf("Arch:         %s\n", strOr(s.Arch, "unknown"))
		fmt.Printf("Build date:   %s\n", strOr(s.BuildDate, "unknown"))
		fmt.Printf("Last seen:    %s\n", strOr(s.LastSeenAt, "unknown"))
		fmt.Printf("Owner:        %s\n", strOr(s.Owner.Email, "unknown"))
		fmt.Println()
	}
}

func printAuthorize(r *v1.AuthorizeResponse) {
	if r.SiteName != "" {
		authURL := fmt.Sprintf("https://%s.%s/callback?token=%s&key=%s", r.SiteName, proxyDomain(r.Host), r.Token, r.SiteKey)
		fmt.Printf("URL:       %s\n", authURL)
	}
	fmt.Printf("Site ID:   %d\n", r.SiteID)
	fmt.Printf("Site name: %s\n", r.SiteName)
	fmt.Printf("Host:      %s\n", r.Host)
	fmt.Printf("Site key:  %s\n", r.SiteKey)
	fmt.Printf("Token:     %s\n", r.Token)
}

func fmtParams(p map[string]any) string {
	if len(p) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(p))
	for k, v := range p {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return strings.Join(parts, ", ")
}

// ── site command ──────────────────────────────────────────────────────────────

func printSiteUsage() {
	fmt.Println(`Usage: sacli site <id|host|query> <subcommand> [args]

Subcommands:
  authorize   Generate authorization token for a site
  metrics     Stream live metrics from a site

Metrics flags:
  -t <pattern>    Filter by topic glob (e.g. "battery*", "total/*"). Default is a
                  curated set of common metrics. Use -t "*" to receive all topics.
  -n <count>      Stop after receiving N metrics
  --watch         Stream metrics continuously via WebSocket (default: snapshot via REST)
  --value         Output values only, no topic or unit (useful for scripting)
  --json          Machine-readable NDJSON output
  --max-freq <s>  Minimum seconds between updates per topic (server-side throttle)
  -v              Verbose: show all requests and socket frames

Examples:
  sacli site 19489 authorize
  sacli site 19489 metrics
  sacli site 19489 metrics -t "*"
  sacli site 19489 metrics -t "battery_1/power" -n 1 --value
  sacli site 19489 metrics -t "battery*" --watch --json
  sacli site name:my-site metrics
  sacli site localhost:4000 metrics
  sacli site localhost:4000 metrics --password <password>`)
}

// isHost returns true if s looks like a host or host:port rather than a site
// ID or key:value query. Matches IPs, localhost, and dot-separated hostnames.
func isHost(s string) bool {
	host := s
	if i := strings.LastIndex(s, ":"); i != -1 {
		host = s[:i]
	}
	return host == "localhost" ||
		strings.Contains(host, ".")
}

func runSite(args []string) {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printSiteUsage()
		return
	}
	if len(args) < 2 {
		printSiteUsage()
		os.Exit(1)
	}

	identifier := args[0]
	// my-site.us.solar-assistant.io → look up by site name "my-site"
	if strings.HasSuffix(identifier, ".solar-assistant.io") {
		identifier = strings.SplitN(identifier, ".", 2)[0]
	}
	subCmd := args[1]
	subArgs := args[2:]

	var auth CachedAuthorize
	if isHost(identifier) {
		pwVals, rest := extractStringFlag(subArgs, "--password")
		subArgs = rest
		pw := ""
		if len(pwVals) > 0 {
			pw = pwVals[0]
		} else {
			cfg, err := loadConfig()
			if err != nil {
				fatal(err)
			}
			pw = cfg.Passwords[identifier]
		}
		if pw == "" {
			fmt.Fprintf(os.Stderr, "no password for %s\n", identifier)
			fmt.Fprintf(os.Stderr, "  use --password <password>, or save it with:\n")
			fmt.Fprintf(os.Stderr, "  sacli configure %s\n", identifier)
			os.Exit(1)
		}
		auth = CachedAuthorize{LocalIP: identifier, Password: pw}
	} else {
		siteID := resolveSiteID(identifier)
		auth = authorizeWithCache(siteID)
	}

	switch subCmd {
	case "authorize":
		runSiteAuthorize(auth, subArgs)
	case "metrics":
		runSiteMetrics(auth, subArgs)
	default:
		fmt.Fprintf(os.Stderr, "unknown site subcommand: %s\n", subCmd)
		os.Exit(1)
	}
}

func runSiteAuthorize(auth CachedAuthorize, args []string) {
	jsonOut, _ := extractFlag(args, "--json")
	if jsonOut {
		out, _ := json.MarshalIndent(auth, "", "  ")
		fmt.Println(string(out))
		return
	}
	printAuthorize(&v1.AuthorizeResponse{
		Host:     auth.Host,
		SiteID:   auth.SiteID,
		SiteName: auth.SiteName,
		SiteKey:  auth.SiteKey,
		Token:    auth.Token,
	})
}

func resolveSiteID(identifier string) int {
	var id int
	if _, err := fmt.Sscanf(identifier, "%d", &id); err == nil {
		return id
	}
	queryArg := identifier
	if !strings.Contains(identifier, ":") {
		queryArg = "name:" + identifier
	}
	q := parseQuery([]string{queryArg})
	q["limit"] = 1
	sites, err := v1.ListSites(newClient(), q)
	if err != nil {
		fatal(err)
	}
	if len(sites) == 0 {
		fatal(fmt.Errorf("no site found for query: %s", identifier))
	}
	if len(sites) > 1 {
		fmt.Fprintf(os.Stderr, "warning: multiple sites matched, using %s (%d)\n", sites[0].Name, sites[0].ID)
	}
	return sites[0].ID
}

func authorizeWithCache(siteID int) CachedAuthorize {
	key := fmt.Sprintf("%d", siteID)

	cache, err := loadAuthorizeCache()
	if err != nil {
		fatal(err)
	}

	if entry, ok := cache.Sites[key]; ok {
		exp, err := tokenExpiry(entry.Token)
		if err == nil && time.Now().Before(exp.Add(-5*time.Minute)) {
			return entry
		}
	}

	resp, err := v1.AuthorizeSite(newClient(), siteID)
	if err != nil {
		fatal(err)
	}

	exp, _ := tokenExpiry(resp.Token)
	entry := CachedAuthorize{
		Host:      resp.Host,
		LocalIP:   resp.LocalIP,
		SiteID:    resp.SiteID,
		SiteName:  resp.SiteName,
		SiteKey:   resp.SiteKey,
		Token:     resp.Token,
		ExpiresAt: exp.Format(time.RFC3339),
	}
	cache.Sites[key] = entry
	if err := saveAuthorizeCache(cache); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not save authorize cache: %v\n", err)
	}
	return entry
}

func runSiteMetrics(auth CachedAuthorize, args []string) {
	jsonOut, args := extractFlag(args, "--json")
	watch, args := extractFlag(args, "--watch")
	valueOut, args := extractFlag(args, "--value")
	filters, args := extractStringFlag(args, "-t")
	limitStrs, args := extractStringFlag(args, "-n")
	maxFreqStrs, _ := extractStringFlag(args, "--max-freq")
	limit := 0
	if len(limitStrs) > 0 {
		fmt.Sscanf(limitStrs[0], "%d", &limit)
	}
	maxFreq := 0
	if len(maxFreqStrs) > 0 {
		fmt.Sscanf(maxFreqStrs[0], "%d", &maxFreq)
	}

	host := auth.Host

	status := func(msg string) {
		fmt.Fprintln(os.Stderr, msg)
	}

	if !watch {
		runRESTMetrics(auth, filters, limit, jsonOut, valueOut)
		return
	}

	if verbose {
		fmt.Fprintln(os.Stderr, "# Verbose mode — Phoenix Channel V2 protocol. See https://github.com/Solar-Assistant/sacli for implementation details.")
	}

	status("Connecting to " + strOr(auth.SiteName, auth.LocalIP, auth.Host) + "...")
	sock, err := sa.Connect(sa.Options{
		Host:     host,
		LocalIP:  auth.LocalIP,
		Token:    auth.Token,
		Password: auth.Password,
		SiteID:   auth.SiteID,
		SiteKey:  auth.SiteKey,
		Verbose:  verbose,
	})
	if err != nil {
		fatal(err)
	}
	defer sock.Close()
	if auth.LocalIP != "" && sock.ConnectedHost == auth.LocalIP {
		status("Connected via local network (" + auth.LocalIP + ").")
	} else {
		status("Connected via cloud (" + sock.ConnectedHost + ").")
	}

	sock.Subscribe("metrics", "phx_reply", func(msg sa.Message) {
		if s, _ := msg.Payload["status"].(string); s == "ok" {
			status("Streaming metrics (Ctrl+C to stop)...")
		} else {
			reason, _ := func() (string, bool) {
				if r, ok := msg.Payload["response"].(map[string]any); ok {
					s, ok := r["reason"].(string)
					return s, ok
				}
				return "", false
			}()
			if reason == "unmatched topic" {
				fmt.Fprintf(os.Stderr, "failed to join metrics channel — site may be running an outdated version (requires build 2026-03-24 or later)\n")
			} else {
				fmt.Fprintf(os.Stderr, "failed to join metrics channel: %s\n", reason)
			}
			os.Exit(1)
		}
	})
	sock.Subscribe("*", "phx_error", func(msg sa.Message) {
		fmt.Fprintf(os.Stderr, "error: %v\n", msg.Payload)
	})

	if !jsonOut && !valueOut {
		sock.Subscribe("metrics", "definition", func(msg sa.Message) {
			items, _ := msg.Payload["definitions"].([]any)
			for _, item := range items {
				mm, _ := item.(map[string]any)
				topic := strVal(mm["topic"])
				if len(filters) > 0 && !matchesAny(topic, filters) {
					continue
				}
				line := fmt.Sprintf("New topic='%s' device='%s'", topic, strVal(mm["device"]))
				if mm["number"] != nil {
					line += fmt.Sprintf(" number=%d", intVal(mm["number"]))
				}
				line += fmt.Sprintf(" group='%s' name='%s' unit='%s'", strVal(mm["group"]), strVal(mm["name"]), strVal(mm["unit"]))
				fmt.Println(line)
			}
		})
	}

	topicFilters := make([]sa.TopicFilter, len(filters))
	for i, f := range filters {
		topicFilters[i] = sa.TopicFilter{Topic: f, MaxFrequencyS: maxFreq}
	}

	count := 0
	if err := sock.SubscribeMetrics(func(m sa.Metric) {
		if valueOut {
			fmt.Println(m.Value)
		} else if jsonOut {
			line, _ := json.Marshal(struct {
				Topic  string `json:"topic"`
				Device string `json:"device"`
				Number int    `json:"number"`
				Name   string `json:"name"`
				Value  any    `json:"value"`
				Unit   string `json:"unit"`
			}{m.Topic, m.Device, m.Number, m.Name, m.Value, m.Unit})
			fmt.Println(string(line))
		} else {
			fmt.Printf("%s %v %s\n", m.Topic, m.Value, m.Unit)
		}
		count++
		if limit > 0 && count >= limit {
			os.Exit(0)
		}
	}, topicFilters...); err != nil {
		fatal(err)
	}
	sock.Listen()
}

type restMetric struct {
	Topic string `json:"topic"`
	Group string `json:"group"`
	Name  string `json:"name"`
	Value any    `json:"value"`
	Unit  string `json:"unit"`
}

func runRESTMetrics(auth CachedAuthorize, filters []string, limit int, jsonOut, valueOut bool) {
	var baseURL string
	if auth.LocalIP != "" {
		// Try local first with a short timeout; fall back to cloud proxy if unreachable.
		host := auth.LocalIP
		if !strings.Contains(host, ":") {
			host += ":80"
		}
		conn, err := net.DialTimeout("tcp", host, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			baseURL = "http://" + auth.LocalIP
		} else {
			baseURL = "https://" + auth.Host
		}
	} else {
		baseURL = "https://" + auth.Host
	}

	// Collect metrics, one request per filter (or one request with no filter)
	seen := map[string]bool{}
	var results []restMetric

	topics := filters
	if len(topics) == 0 {
		topics = []string{""}
	}

	httpClient := &http.Client{Timeout: 5 * time.Second}
	for _, topic := range topics {
		u := baseURL + "/api/v1/metrics"
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			fatal(err)
		}
		if topic != "" {
			q := req.URL.Query()
			q.Set("topic", topic)
			req.URL.RawQuery = q.Encode()
		}
		if auth.Password != "" {
			// Pure local connection (no cloud auth) — use basic auth with web password.
			req.SetBasicAuth("solar-assistant", auth.Password)
		} else {
			req.Header.Set("Authorization", "Bearer "+auth.Token)
			if auth.SiteID != 0 {
				req.Header.Set("site-id", fmt.Sprintf("%d", auth.SiteID))
			}
			if auth.SiteKey != "" {
				req.Header.Set("site-key", auth.SiteKey)
			}
		}
		if verbose {
			fmt.Fprintf(os.Stderr, "> GET %s\n", req.URL.String())
			for k, v := range req.Header {
				fmt.Fprintf(os.Stderr, "> %s: %s\n", k, strings.Join(v, ", "))
			}
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			fatal(err)
		}
		if verbose {
			fmt.Fprintf(os.Stderr, "< %d %s\n", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		if resp.StatusCode == http.StatusNotFound {
			fatal(fmt.Errorf("HTTP 404: site may be running an outdated version (requires build 2026-03-24 or later)"))
		}
		if resp.StatusCode != http.StatusOK {
			fatal(fmt.Errorf("API error %d: %s", resp.StatusCode, strings.TrimSpace(string(body))))
		}
		var batch []restMetric
		if err := json.Unmarshal(body, &batch); err != nil {
			fatal(fmt.Errorf("unexpected response: %w", err))
		}
		for _, m := range batch {
			if !seen[m.Topic] {
				seen[m.Topic] = true
				results = append(results, m)
			}
		}
	}

	count := 0
	for _, m := range results {
		if valueOut {
			fmt.Println(m.Value)
		} else if jsonOut {
			line, _ := json.Marshal(struct {
				Topic string `json:"topic"`
				Group string `json:"group"`
				Name  string `json:"name"`
				Value any    `json:"value"`
				Unit  string `json:"unit"`
			}{m.Topic, m.Group, m.Name, m.Value, m.Unit})
			fmt.Println(string(line))
		} else {
			fmt.Printf("%s %v %s\n", m.Topic, m.Value, m.Unit)
		}
		count++
		if limit > 0 && count >= limit {
			return
		}
	}
}
