package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	sa "github.com/Solar-Assistant/go_solar_assistant"
	"golang.org/x/term"
)

const version = "0.2.2"

var verbose bool

func main() {
	if len(os.Args) < 2 {
		if term.IsTerminal(int(os.Stdin.Fd())) {
			runInteractive()
			return
		}
		printUsage()
		os.Exit(1)
	}

	args := os.Args[1:]
	verbose, args = extractFlag(args, "-v")
	// -v alone means version; -v with other args means verbose
	if verbose && len(args) == 0 {
		fmt.Println(version)
		return
	}
	if len(args) == 0 {
		printUsage()
		os.Exit(1)
	}
	cmd := args[0]
	args = args[1:]

	switch cmd {
	case "site":
		runSite(args)
	case "sites":
		runSites(args)
	case "configure":
		runConfigure(args)
	case "mcp":
		runMCP(args)
	case "version", "--version":
		fmt.Println(version)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`Solar Assistant CLI

Usage:
  sacli <command> [arguments]

Commands:
  site        Connect to a site and run subcommands
  sites       List or search sites
  configure   Set credentials
  mcp         Run as a stdio MCP server
  version     Print version
  help        Show this help

Example:
  sacli site --help           Show site subcommand help`)
}

func newClient() *sa.Client {
	cfg, err := loadConfig()
	if err != nil {
		fatal(err)
	}
	if cfg.CloudAPIKey == "" {
		fatal(fmt.Errorf("no API key configured — run: sacli configure"))
	}
	c := sa.NewClient(cfg.CloudAPIKey)
	c.Verbose = verbose
	return c
}

func runInteractive() {
	fmt.Printf("Solar Assistant CLI %s\n", version)
	fmt.Println("Type 'help' for available commands, 'exit' to quit.")
	fmt.Println("Can also be used as a command line tool — run 'sacli --help' for usage.")
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		args := strings.Fields(line)
		cmd, rest := args[0], args[1:]
		switch cmd {
		case "exit", "quit":
			return
		case "help", "--help", "-h":
			printUsage()
		case "site":
			runSite(rest)
		case "sites":
			runSites(rest)
		case "configure":
			runConfigure(rest)
		case "version":
			fmt.Println(version)
		default:
			fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		}
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func strVal(v any) string {
	s, _ := v.(string)
	return s
}

func intVal(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}

func strOr(vals ...string) string {
	for _, s := range vals {
		if s != "" {
			return s
		}
	}
	return ""
}

func anyOr(v any, fallback string) any {
	if v == nil {
		return fallback
	}
	return v
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

func matchesAny(s string, patterns []string) bool {
	lower := strings.ToLower(s)
	for _, p := range patterns {
		if globMatch(strings.ToLower(p), lower) {
			return true
		}
	}
	return false
}

func globMatch(pattern, s string) bool {
	for len(pattern) > 0 {
		star := strings.IndexByte(pattern, '*')
		if star == -1 {
			return pattern == s
		}
		prefix := pattern[:star]
		if !strings.HasPrefix(s, prefix) {
			return false
		}
		s = s[len(prefix):]
		pattern = pattern[star+1:]
		if len(pattern) == 0 {
			return true
		}
		next := strings.IndexByte(pattern, '*')
		var chunk string
		if next == -1 {
			chunk = pattern
		} else {
			chunk = pattern[:next]
		}
		idx := strings.Index(s, chunk)
		if idx == -1 {
			return false
		}
		s = s[idx+len(chunk):]
		pattern = pattern[len(chunk):]
	}
	return s == ""
}

func extractFlag(args []string, flag string) (bool, []string) {
	found := false
	rest := make([]string, 0, len(args))
	for _, a := range args {
		if a == flag {
			found = true
		} else {
			rest = append(rest, a)
		}
	}
	return found, rest
}

func extractStringFlag(args []string, flag string) ([]string, []string) {
	var values, rest []string
	for i := 0; i < len(args); i++ {
		if args[i] == flag && i+1 < len(args) {
			values = append(values, args[i+1])
			i++
		} else {
			rest = append(rest, args[i])
		}
	}
	return values, rest
}

// parseQuery converts CLI key:value args into a map[string]any.
// Numeric values for "limit" and "offset" are parsed as integers.
func parseQuery(args []string) map[string]any {
	q := make(map[string]any, len(args))
	for _, arg := range args {
		idx := strings.IndexByte(arg, ':')
		if idx < 1 {
			continue
		}
		k, v := arg[:idx], arg[idx+1:]
		if k == "limit" || k == "offset" {
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
				q[k] = n
				continue
			}
		}
		q[k] = v
	}
	return q
}

// proxyDomain converts a proxy host like "us-htz-1.solar-assistant.io"
// into "us.solar-assistant.io" by keeping only the part before the first "-"
// in the first segment, then appending the rest of the domain.
func proxyDomain(host string) string {
	dot := strings.IndexByte(host, '.')
	if dot == -1 {
		return host
	}
	segment := host[:dot]
	rest := host[dot:] // e.g. ".solar-assistant.io"
	if dash := strings.IndexByte(segment, '-'); dash != -1 {
		segment = segment[:dash]
	}
	return segment + rest
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
