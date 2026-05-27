package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

func runConfigure(args []string) error {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		dir, _ := configDir()
		fmt.Printf(`Usage: sacli configure [host] [token]

  sacli configure                         Prompt for cloud API key
  sacli configure <api_key>               Save cloud API key directly
  sacli configure <host>                  Prompt for password for host
  sacli configure <host> --password <pw>  Save password directly

Credentials are stored in: %s

Examples:
  sacli configure
  sacli configure eyJ...
  sacli configure localhost:4000
  sacli configure localhost:4000 --password initpass
  sacli configure 192.168.1.100 --password mypassword
`, dir)
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

	// Direct save if key provided as argument
	if len(args) == 1 {
		cfg.CloudAPIKey = args[0]
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

	b, _ := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	input := strings.TrimSpace(string(b))

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
