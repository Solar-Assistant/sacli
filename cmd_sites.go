package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/Solar-Assistant/go_solar_assistant/cloud"
	"github.com/Solar-Assistant/go_solar_assistant/device"
)

// ── sites command ─────────────────────────────────────────────────────────────

func runSites(args []string) error {
	if len(args) > 0 && args[0] == "authorize" {
		return runSitesAuthorize(args[1:])
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
  sacli sites region:us online:true
  sacli sites license:trial channel:beta
  sacli sites user_id:123
  sacli sites --json name:my-site`)
		return nil
	}

	client, err := newClient()
	if err != nil {
		return err
	}
	result, err := client.ListSites(parseQuery(args))
	if err != nil {
		return err
	}

	if len(result) == 0 {
		fmt.Println("No sites found.")
		return nil
	}

	if jsonOut {
		out, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	printSites(result)

	return nil
}

func runSitesAuthorize(args []string) error {
	jsonOut, args := extractFlag(args, "--json")
	roles, args := extractStringFlag(args, "--roles")

	if len(args) != 1 {
		return fmt.Errorf("Usage: sacli sites authorize <site_id> [--roles admin,ssh]")
	}

	var siteID int
	if _, err := fmt.Sscanf(args[0], "%d", &siteID); err != nil {
		return fmt.Errorf("invalid site ID: %s", args[0])
	}

	client, err := newClient()
	if err != nil {
		return err
	}
	result, err := client.AuthorizeSite(siteID, roles...)
	if err != nil {
		return err
	}

	if jsonOut {
		out, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	printAuthorize(result)
	return nil
}

func printSites(sites []cloud.Site) {
	for _, s := range sites {
		fmt.Printf("Site ID:      %d\n", s.ID)
		fmt.Printf("Name:         %s\n", s.Name)
		fmt.Printf("Inverter:     %s (x%d)\n", strOr(s.Inverter, "unknown"), max1(s.InverterCount))
		fmt.Printf("Inv params:   %s\n", fmtParams(s.InverterParams))
		fmt.Printf("Battery:      %s (x%d)\n", strOr(s.Battery, "unknown"), max1(s.BatteryCount))
		fmt.Printf("Bat params:   %s\n", fmtParams(s.BatteryParams))
		fmt.Printf("Proxy:        %s\n", strOr(s.Proxy, "none"))
		fmt.Printf("Local IP:     %s\n", strOr(s.LocalIP, "none"))
		fmt.Printf("Arch:         %s\n", strOr(s.Arch, "unknown"))
		fmt.Printf("Build date:   %s\n", strOr(s.BuildDate, "unknown"))
		fmt.Printf("Last seen:    %s\n", strOr(s.LastSeenAt, "unknown"))
		fmt.Printf("Owner:        %s\n", strOr(s.Owner.Email, "unknown"))
		fmt.Println()
	}
}

func printAuthorize(r *cloud.AuthorizeResponse) {
	if r.SiteName != "" {
		authURL := fmt.Sprintf("https://%s.%s/callback?token=%s&key=%s", r.SiteName, proxyDomain(r.Host), r.Token, r.SiteKey)
		fmt.Printf("URL:       %s\n", authURL)
	}
	fmt.Printf("Site ID:   %d\n", r.SiteID)
	fmt.Printf("Site name: %s\n", r.SiteName)
	fmt.Printf("Host:      %s\n", r.Host)
	if r.SiteHost != "" {
		fmt.Printf("Site host/alias: %s\n", r.SiteHost)
	}
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
  metrics     Read or stream metrics from a site
  set         Write a setting via REST

Metrics flags:
  -t <pattern>    Filter by topic glob (e.g. "battery*", "total/*"). Default is a
                  curated set of common metrics. Use -t "*" to receive all topics.
  -n <count>      Stop after receiving N metrics
  --watch         Stream metrics continuously via WebSocket (default: snapshot via REST)
  --value         Output values only, no topic or unit (useful for scripting)
  --json          Machine-readable NDJSON output
  --max-freq <s>  Minimum seconds between updates per topic (server-side throttle)
  -v              Verbose: show all requests and socket frames

Authentication:
  A cloud token (from "authorize") can be used to connect via the cloud or directly
  over the local network. A local password can only be used for direct local connections.

Examples:
  sacli site 123 authorize
  sacli site 123 metrics
  sacli site 123 metrics -t "*" -n 500
  sacli site 123 metrics -t "battery_1/power" -n 1 --value
  sacli site 123 metrics -t "battery*" --watch --json
  sacli site name:my-site metrics
  sacli site localhost:4000 metrics
  sacli site localhost:4000 metrics --password <password>
  sacli site localhost:4000 metrics --token <token>
  sacli site 123 set inverter_1/charge_current_limit:20
  sacli site 123 set inverter_1/device_mode:"Off grid"
  sacli site localhost:4000 set inverter_1/charge_current_limit:20 --password <password>`)
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

func runSite(args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printSiteUsage()
		return nil
	}
	if len(args) < 2 {
		printSiteUsage()
		return fmt.Errorf("not enough arguments")
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
		tokenVals, rest := extractStringFlag(subArgs, "--token")
		subArgs = rest
		pwVals, rest := extractStringFlag(subArgs, "--password")
		subArgs = rest
		if len(tokenVals) > 0 {
			auth = CachedAuthorize{LocalIP: identifier, Token: tokenVals[0]}
		} else {
			pw := ""
			if len(pwVals) > 0 {
				pw = pwVals[0]
			} else {
				cfg, err := loadConfig()
				if err != nil {
					return err
				}
				pw = cfg.Passwords[identifier]
			}
			if pw == "" {
				fmt.Fprintf(os.Stderr, "no password for %s\n", identifier)
				fmt.Fprintf(os.Stderr, "  use --password <password> or --token <token>, or save the password with:\n")
				fmt.Fprintf(os.Stderr, "  sacli configure %s\n", identifier)
				return fmt.Errorf("no password for %s", identifier)
			}
			auth = CachedAuthorize{LocalIP: identifier, Password: pw}
		}
	} else {
		siteID, err := resolveSiteID(identifier)
		if err != nil {
			return err
		}
		auth, err = authorizeWithCache(siteID)
		if err != nil {
			return err
		}
	}

	switch subCmd {
	case "authorize":
		return runSiteAuthorize(auth, subArgs)
	case "metrics":
		return runSiteMetrics(auth, subArgs)
	case "set":
		return runSiteSet(auth, subArgs)
	default:
		return fmt.Errorf("unknown site subcommand: %s", subCmd)
	}
}

func runSiteAuthorize(auth CachedAuthorize, args []string) error {
	jsonOut, _ := extractFlag(args, "--json")
	if jsonOut {
		out, _ := json.MarshalIndent(auth, "", "  ")
		fmt.Println(string(out))
		return nil
	}
	printAuthorize(&cloud.AuthorizeResponse{
		Host:     auth.Host,
		SiteID:   auth.SiteID,
		SiteName: auth.SiteName,
		SiteKey:  auth.SiteKey,
		Token:    auth.Token,
	})
	return nil
}

func resolveSiteID(identifier string) (int, error) {
	var id int
	if _, err := fmt.Sscanf(identifier, "%d", &id); err == nil {
		return id, nil
	}
	if !strings.Contains(identifier, ":") {
		if cache, err := loadAuthorizeCache(); err == nil {
			for _, entry := range cache.Sites {
				if strings.EqualFold(entry.SiteName, identifier) {
					return entry.SiteID, nil
				}
			}
		}
	}
	queryArg := identifier
	if !strings.Contains(identifier, ":") {
		queryArg = "name:" + identifier
	}
	q := parseQuery([]string{queryArg})
	q["limit"] = 1
	client, err := newClient()
	if err != nil {
		return 0, err
	}
	sites, err := client.ListSites(q)
	if err != nil {
		return 0, err
	}
	if len(sites) == 0 {
		return 0, fmt.Errorf("no site found for query: %s", identifier)
	}
	if len(sites) > 1 {
		fmt.Fprintf(os.Stderr, "warning: multiple sites matched, using %s (%d)\n", sites[0].Name, sites[0].ID)
	}
	return sites[0].ID, nil
}

func authorizeWithCache(siteID int, roles ...string) (CachedAuthorize, error) {
	key := fmt.Sprintf("%d", siteID)
	if len(roles) > 0 {
		key += ":" + strings.Join(roles, ",")
	}

	cache, err := loadAuthorizeCache()
	if err != nil {
		return CachedAuthorize{}, err
	}

	if entry, ok := cache.Sites[key]; ok {
		exp, err := tokenExpiry(entry.Token)
		cachedAt, _ := time.Parse(time.RFC3339, entry.CachedAt)
		if err == nil && time.Now().Before(exp.Add(-5*time.Minute)) && time.Since(cachedAt) < 8*time.Hour {
			return entry, nil
		}
	}

	client, err := newClient()
	if err != nil {
		return CachedAuthorize{}, err
	}
	resp, err := client.AuthorizeSite(siteID, roles...)
	if err != nil {
		if entry, ok := cache.Sites[key]; ok {
			exp, terr := tokenExpiry(entry.Token)
			if terr == nil && time.Now().Before(exp) {
				fmt.Fprintf(os.Stderr, "warning: could not refresh auth for site %d, using stale cache: %v\n", siteID, err)
				return entry, nil
			}
		}
		return CachedAuthorize{}, err
	}

	exp, _ := tokenExpiry(resp.Token)
	entry := CachedAuthorize{
		Host:      resp.Host,
		SiteHost:  resp.SiteHost,
		LocalIP:   resp.LocalIP,
		SiteID:    resp.SiteID,
		SiteName:  resp.SiteName,
		SiteKey:   resp.SiteKey,
		Token:     resp.Token,
		ExpiresAt: exp.Format(time.RFC3339),
		CachedAt:  time.Now().Format(time.RFC3339),
	}
	cache.Sites[key] = entry
	if err := saveAuthorizeCache(cache); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not save authorize cache: %v\n", err)
	}
	return entry, nil
}

const localIPFailedTTL = time.Hour

// markLocalIPFailed records that the local IP for a cloud-resolved site is unreachable.
func markLocalIPFailed(siteID int) {
	key := fmt.Sprintf("%d", siteID)
	cache, err := loadAuthorizeCache()
	if err != nil {
		return
	}
	entry, ok := cache.Sites[key]
	if !ok {
		return
	}
	entry.LocalIPFailedAt = time.Now().Format(time.RFC3339)
	cache.Sites[key] = entry
	saveAuthorizeCache(cache)
}

// markLocalIPSucceeded clears the local IP failure record for a site.
func markLocalIPSucceeded(siteID int) {
	key := fmt.Sprintf("%d", siteID)
	cache, err := loadAuthorizeCache()
	if err != nil {
		return
	}
	entry, ok := cache.Sites[key]
	if !ok || entry.LocalIPFailedAt == "" {
		return
	}
	entry.LocalIPFailedAt = ""
	cache.Sites[key] = entry
	saveAuthorizeCache(cache)
}

// localIPRecentlyFailed returns true if the local IP for a cloud-resolved site
// has failed within the past hour. Never blocks direct-host connections (auth.Password != "").
func localIPRecentlyFailed(auth CachedAuthorize) bool {
	if auth.Password != "" || auth.LocalIPFailedAt == "" {
		return false
	}
	failedAt, err := time.Parse(time.RFC3339, auth.LocalIPFailedAt)
	return err == nil && time.Since(failedAt) < localIPFailedTTL
}

func runSiteMetrics(auth CachedAuthorize, args []string) error {
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
		return runRESTMetrics(auth, filters, limit, jsonOut, valueOut)
	}

	if verbose {
		fmt.Fprintln(os.Stderr, "# Verbose mode — Phoenix Channel V2 protocol. See https://github.com/Solar-Assistant/sacli for implementation details.")
	}

	status("Connecting to " + strOr(auth.SiteName, auth.LocalIP, auth.Host) + "...")
	sock, err := device.Connect(device.Options{
		Host:     host,
		LocalIP:  auth.LocalIP,
		Token:    auth.Token,
		Password: auth.Password,
		SiteID:   auth.SiteID,
		SiteKey:  auth.SiteKey,
		Verbose:  verbose,
	})
	if err != nil {
		return err
	}
	defer sock.Close()
	if auth.LocalIP != "" && sock.ConnectedHost == auth.LocalIP {
		status("Connected via local network (" + auth.LocalIP + ").")
	} else {
		status("Connected via cloud (" + sock.ConnectedHost + ").")
	}

	var sockErr error

	sock.Subscribe("metrics", "phx_reply", func(msg device.Message) {
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
				sockErr = fmt.Errorf("failed to join metrics channel — site may be running an outdated version (requires build 2026-03-24 or later)")
			} else {
				sockErr = fmt.Errorf("failed to join metrics channel: %s", reason)
			}
			sock.Close()
		}
	})
	sock.Subscribe("*", "phx_error", func(msg device.Message) {
		fmt.Fprintf(os.Stderr, "error: %v\n", msg.Payload)
	})

	if !jsonOut && !valueOut {
		sock.Subscribe("metrics", "definition", func(msg device.Message) {
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

	topicFilters := make([]device.TopicFilter, len(filters))
	for i, f := range filters {
		topicFilters[i] = device.TopicFilter{Topic: f, MaxFrequencyS: maxFreq}
	}

	count := 0
	if err := sock.SubscribeMetrics(func(m device.Metric) {
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
			sock.Close()
		}
	}, topicFilters...); err != nil {
		return err
	}
	sock.Listen()
	return sockErr
}

// resolveRESTHost returns the host and scheme to use for device REST calls.
// Tries the local IP first (500ms TCP probe); falls back to the cloud proxy host.
func resolveRESTHost(auth CachedAuthorize) (host, scheme string) {
	if auth.LocalIP != "" {
		probe := auth.LocalIP
		if !strings.Contains(probe, ":") {
			probe += ":80"
		}
		conn, err := net.DialTimeout("tcp", probe, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return auth.LocalIP, "http"
		}
	}
	return auth.Host, "https"
}

// newDeviceClient builds a device.Client from cached auth, probing local vs cloud.
func newDeviceClient(auth CachedAuthorize) *device.Client {
	host, scheme := resolveRESTHost(auth)
	dc := device.NewClient(host)
	dc.Password = auth.Password
	dc.Token = auth.Token
	dc.SiteID = auth.SiteID
	dc.SiteKey = auth.SiteKey
	dc.Scheme = scheme
	dc.Verbose = verbose
	return dc
}

func runSiteSet(auth CachedAuthorize, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("Usage: sacli site <id|host> set <topic>:<value>")
	}
	idx := strings.IndexByte(args[0], ':')
	if idx < 1 {
		return fmt.Errorf("invalid argument %q — expected topic:value", args[0])
	}
	topic, value := args[0][:idx], args[0][idx+1:]

	if err := newDeviceClient(auth).SetMetric(topic, value); err != nil {
		return err
	}
	fmt.Printf("set %s = %s\n", topic, value)
	return nil
}

func runRESTMetrics(auth CachedAuthorize, filters []string, limit int, jsonOut, valueOut bool) error {
	results, err := newDeviceClient(auth).GetMetrics(filters...)
	if err != nil {
		return err
	}

	count := 0
	for _, m := range results {
		if valueOut {
			fmt.Println(m.Value)
		} else if jsonOut {
			line, _ := json.Marshal(struct {
				Topic  string `json:"topic"`
				Device string `json:"device"`
				Number int    `json:"number"`
				Group  string `json:"group"`
				Name   string `json:"name"`
				Value  any    `json:"value"`
				Unit   string `json:"unit"`
			}{m.Topic, m.Device, m.Number, m.Group, m.Name, m.Value, m.Unit})
			fmt.Println(string(line))
		} else {
			fmt.Printf("%s %v %s\n", m.Topic, m.Value, m.Unit)
		}
		count++
		if limit > 0 && count >= limit {
			return nil
		}
	}
	return nil
}
