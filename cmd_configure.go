package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// apiKeyEnvVar carries the cloud API key to `sacli configure` so the key never
// has to appear in argv, where ps and the shell history can both read it.
const apiKeyEnvVar = "SACLI_API_KEY"

// readAPIKeyFromTerminal prompts for a key with echo off. It is a variable only
// so tests can drive the prompt without a tty; production always gets the
// terminal read below.
var readAPIKeyFromTerminal = func() string {
	b, _ := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	return strings.TrimSpace(string(b))
}

func runConfigure(args []string) error {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		dir, _ := configDir()
		fmt.Printf(`Usage: sacli configure [host] [token]

  sacli configure                           Prompt for cloud API key
  SACLI_API_KEY=<key> sacli configure       Save cloud API key from the environment
  sacli configure <api_key>                 Save cloud API key directly (deprecated)
  sacli configure <host>                    Prompt for password for host
  sacli configure <host> --password <pw>    Save password directly

API key precedence: argument > %s > prompt.

Passing the key as an argument is deprecated: it is visible in ps, in
/proc/<pid>/cmdline and in shell history for the life of the process. Prefer
the environment variable or the prompt.

Credentials are stored in: %s

Examples:
  sacli configure
  SACLI_API_KEY=eyJ... sacli configure
  sacli configure localhost:4000
  sacli configure localhost:4000 --password initpass
  sacli configure 192.168.1.100 --password mypassword
`, apiKeyEnvVar, dir)
		return nil
	}

	// Local host password
	if len(args) >= 1 && isHost(args[0]) {
		host := args[0]
		pwVals, _ := extractStringFlag(args[1:], "--password")
		pw := ""
		if len(pwVals) > 0 {
			pw = pwVals[0]
		} else {
			fmt.Printf("Enter local password for %s (set at http://%s/configuration/system): ", host, host)
			b, _ := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Println()
			pw = strings.TrimSpace(string(b))
			if pw == "" {
				fmt.Println("No changes made.")
				return nil
			}
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		if cfg.Passwords == nil {
			cfg.Passwords = map[string]string{}
		}
		cfg.Passwords[host] = pw
		if err := saveConfig(cfg); err != nil {
			return err
		}
		path, _ := configPath()
		fmt.Printf("Password for %s saved to %s\n", host, path)
		return nil
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	// Direct save if a key came from the argument or the environment. The
	// argument wins, so scripts written against the positional form keep
	// working; the env var is the way to avoid argv altogether.
	key, haveKey := "", false
	if len(args) == 1 {
		key, haveKey = args[0], true
		fmt.Fprintf(os.Stderr,
			"warning: passing the API key as an argument is deprecated: it is visible in ps, in /proc/<pid>/cmdline and in shell history; use %s=<key> instead\n",
			apiKeyEnvVar)
	} else if v := strings.TrimSpace(os.Getenv(apiKeyEnvVar)); v != "" {
		key, haveKey = v, true
	}

	if haveKey {
		cfg.CloudAPIKey = key
		if err := saveConfig(cfg); err != nil {
			return err
		}
		deleteMCPToolsCache()
		path, _ := configPath()
		fmt.Printf("API key saved to %s\n", path)
		return nil
	}

	// Prompt
	if cfg.CloudAPIKey != "" {
		fmt.Println("API key is already configured.")
		fmt.Print("Enter a new key to replace it, or press Enter to keep it: ")
	} else {
		fmt.Println("No API key configured. Generate one at https://solar-assistant.io/user/edit#api")
		fmt.Println()
		fmt.Print("Paste your API key here: ")
	}

	input := readAPIKeyFromTerminal()

	if input == "" {
		fmt.Println("No changes made.")
		return nil
	}

	cfg.CloudAPIKey = input
	if err := saveConfig(cfg); err != nil {
		return err
	}
	deleteMCPToolsCache()
	path, _ := configPath()
	fmt.Printf("API key saved to %s\n", path)
	return nil
}

func deleteMCPToolsCache() {
	if path, err := mcpToolsCachePath(); err == nil {
		os.Remove(path)
	}
}
