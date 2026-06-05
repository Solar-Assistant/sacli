package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const sshProxyPort = 2222

func runSSH(args []string) {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Println(`Usage: sacli ssh <subcommand|site>

Subcommands:
  proxy <site>   ProxyCommand for use in custom SSH configs
  install        Add SolarAssistant entry to ~/.ssh/config

Connect directly to a site:
  sacli ssh 19489
  sacli ssh my-site`)
		return
	}

	switch args[0] {
	case "proxy":
		runSSHProxy(args[1:])
	case "install":
		runSSHInstall(args[1:])
	default:
		runSSHConnect(args)
	}
}

func runSSHConnect(args []string) {
	identifier := args[0]
	siteID, err := resolveSiteID(identifier)
	if err != nil {
		fatal(err)
	}
	auth, err := authorizeWithCache(siteID)
	if err != nil {
		fatal(err)
	}

	sacliPath, err := os.Executable()
	if err != nil {
		fatal(err)
	}
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		fatal(fmt.Errorf("ssh not found: %w", err))
	}

	// Use site_host as the SSH hostname for per-site known_hosts tracking — avoids
	// all sites on the same proxy sharing one fingerprint. The ProxyCommand still
	// connects via Host directly, so DNS propagation delays don't affect connectivity.
	sshHost := auth.SiteHost
	if sshHost == "" {
		sshHost = auth.Host
	}
	sshArgs := []string{
		"ssh",
		"-o", fmt.Sprintf("ProxyCommand=%s ssh proxy %s", sacliPath, identifier),
		"-o", "StrictHostKeyChecking=accept-new",
		fmt.Sprintf("solar-assistant@%s", sshHost),
	}
	if err := syscall.Exec(sshPath, sshArgs, os.Environ()); err != nil {
		fatal(err)
	}
}

func runSSHProxy(args []string) {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Println(`Usage: sacli ssh proxy <site>

ProxyCommand that connects to the SASH proxy for a site. Used by sacli ssh,
but can also be used directly in your own SSH config:

  Host *.solar-assistant.io
    User solar-assistant
    ProxyCommand sacli ssh proxy %h

Then the following will work:

  ssh my-site.us.solar-assistant.io`)
		return
	}

	identifier := args[0]
	if strings.HasSuffix(identifier, ".solar-assistant.io") {
		identifier = strings.SplitN(identifier, ".", 2)[0]
	}
	siteID, err := resolveSiteID(identifier)
	if err != nil {
		fatal(err)
	}
	auth, err := authorizeWithCache(siteID)
	if err != nil {
		fatal(err)
	}

	addr := fmt.Sprintf("%s:%d", auth.Host, sshProxyPort)
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		fatal(fmt.Errorf("could not connect to %s: %w", addr, err))
	}
	defer conn.Close()

	token := []byte(auth.Token)
	preamble := make([]byte, 4+2+len(token))
	copy(preamble, "SASH")
	binary.BigEndian.PutUint16(preamble[4:], uint16(len(token)))
	copy(preamble[6:], token)
	if _, err := conn.Write(preamble); err != nil {
		fatal(err)
	}

	status := make([]byte, 1)
	if _, err := io.ReadFull(conn, status); err != nil {
		fatal(fmt.Errorf("no response from proxy: %w", err))
	}
	if status[0] != 0x00 {
		lenByte := make([]byte, 1)
		if _, err := io.ReadFull(conn, lenByte); err != nil {
			fatal(fmt.Errorf("proxy rejected connection"))
		}
		msg := make([]byte, lenByte[0])
		io.ReadFull(conn, msg)
		fatal(fmt.Errorf("proxy error: %s", msg))
	}

	done := make(chan struct{}, 2)
	go func() {
		io.Copy(conn, os.Stdin)
		done <- struct{}{}
	}()
	go func() {
		io.Copy(os.Stdout, conn)
		done <- struct{}{}
	}()
	<-done
}

const sshConfigBlock = `
Host *.solar-assistant.io
  User solar-assistant
  ProxyCommand sacli ssh proxy %h
`

func runSSHInstall(args []string) {
	home, err := os.UserHomeDir()
	if err != nil {
		fatal(err)
	}
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		fatal(err)
	}
	configPath := filepath.Join(sshDir, "config")

	existing, _ := os.ReadFile(configPath)
	if strings.Contains(string(existing), "*.solar-assistant.io") {
		fmt.Println("~/.ssh/config already contains a SolarAssistant entry.")
		return
	}

	f, err := os.OpenFile(configPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(sshConfigBlock); err != nil {
		fatal(err)
	}
	fmt.Println("Added SolarAssistant entry to ~/.ssh/config.")
	fmt.Println("You can now connect with: ssh my-site.us.solar-assistant.io")
}
