package main

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The vulnerability these guard is the key appearing in argv, where ps,
// /proc/<pid>/cmdline and the shell history all read it. The env var is the fix,
// the argument stays for the scripts already written against it, so the
// assertions below are mostly about *where* a key comes from and about nothing
// printed ever carrying one — a key that leaks to stdout is leaked just as
// permanently as one that leaks to argv.

// configureInTemp points the config at a temp dir and checks that it took.
// os.UserConfigDir only honours XDG_CONFIG_HOME on Unix, so the assumption is
// verified here rather than trusted: a silently ignored variable would let these
// tests overwrite the developer's real config.toml.
func configureInTemp(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("os.UserConfigDir ignores XDG_CONFIG_HOME on %s", runtime.GOOS)
	}
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)

	dir, err := configDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, "solar-assistant"); dir != want {
		t.Fatalf("configDir() = %q, want %q — the tests would write the real user config", dir, want)
	}
	return dir
}

// captureOutput runs fn with os.Stdout and os.Stderr on a pipe. Both are read
// at call time by fmt.Printf/Fprintln, so this sees everything runConfigure
// writes on either stream.
func captureOutput(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w

	captured := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		io.Copy(&buf, r)
		captured <- buf.String()
	}()

	runErr := fn()

	os.Stdout, os.Stderr = origOut, origErr
	w.Close()
	out := <-captured
	r.Close()
	return out, runErr
}

// answerPrompt makes the key prompt answer with the given text, the way
// term.ReadPassword would after a paste. The returned func reports whether the
// prompt was reached, so a test can tell the prompt path from the direct-save
// one instead of inferring it from what got stored.
func answerPrompt(t *testing.T, answer string) func() bool {
	t.Helper()
	asked := false
	orig := readAPIKeyFromTerminal
	readAPIKeyFromTerminal = func() string {
		asked = true
		return answer
	}
	t.Cleanup(func() { readAPIKeyFromTerminal = orig })
	return func() bool { return asked }
}

// unsetAPIKeyEnv removes SACLI_API_KEY for the test, restoring it afterwards.
// An empty value is not the same thing to os.LookupEnv, and "unset" is the case
// the fallback has to survive.
func unsetAPIKeyEnv(t *testing.T) {
	t.Helper()
	if prev, ok := os.LookupEnv(apiKeyEnvVar); ok {
		t.Cleanup(func() { os.Setenv(apiKeyEnvVar, prev) })
	}
	if err := os.Unsetenv(apiKeyEnvVar); err != nil {
		t.Fatal(err)
	}
}

func storedKey(t *testing.T) string {
	t.Helper()
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	return cfg.CloudAPIKey
}

// configFiles lists every file below the config dir, as paths relative to it.
func configFiles(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		found = append(found, rel)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return found
}

func TestConfigureKeyFromEnv(t *testing.T) {
	dir := configureInTemp(t)
	const key = "eyJ-env-key-do-not-print"
	t.Setenv(apiKeyEnvVar, key)

	out, err := captureOutput(t, func() error { return runConfigure(nil) })
	if err != nil {
		t.Fatal(err)
	}

	if got := storedKey(t); got != key {
		t.Errorf("cloud_api_key = %q, want the env var value %q", got, key)
	}
	// The confirmation is what makes the no-leak assertion below mean anything:
	// a run that printed nothing would pass it trivially.
	if !strings.Contains(out, "API key saved to") {
		t.Errorf("no confirmation printed, so the leak check is vacuous: %q", out)
	}
	if strings.Contains(out, key) {
		t.Errorf("the key reached stdout/stderr: %q", out)
	}
	if files := configFiles(t, dir); len(files) != 1 || files[0] != "config.toml" {
		t.Errorf("config dir holds %v, want only config.toml — the key must not land in another file", files)
	}
}

func TestConfigureArgumentWinsOverEnv(t *testing.T) {
	configureInTemp(t)
	t.Setenv(apiKeyEnvVar, "eyJ-from-env")

	const arg = "eyJ-from-argument"
	out, err := captureOutput(t, func() error { return runConfigure([]string{arg}) })
	if err != nil {
		t.Fatal(err)
	}

	// Released scripts pass the key positionally, so the argument has to keep
	// beating the environment.
	if got := storedKey(t); got != arg {
		t.Errorf("cloud_api_key = %q, want the argument %q to win over the env var", got, arg)
	}
	if strings.Contains(out, arg) || strings.Contains(out, "eyJ-from-env") {
		t.Errorf("a key reached stdout/stderr: %q", out)
	}
	// The positional form is deprecated, not removed, so it still has to say so.
	if !strings.Contains(out, "deprecated") {
		t.Errorf("the argument path should warn that it is deprecated: %q", out)
	}
}

func TestConfigureFallsBackToPromptWithoutEnv(t *testing.T) {
	configureInTemp(t)
	unsetAPIKeyEnv(t)
	prompted := answerPrompt(t, "eyJ-from-prompt")

	out, err := captureOutput(t, func() error { return runConfigure(nil) })
	if err != nil {
		t.Fatal(err)
	}

	if !prompted() {
		t.Error("the interactive prompt was not reached with no argument and no env var")
	}
	if got := storedKey(t); got != "eyJ-from-prompt" {
		t.Errorf("cloud_api_key = %q, want the prompted value", got)
	}
	if strings.Contains(out, "eyJ-from-prompt") {
		t.Errorf("the key reached stdout/stderr: %q", out)
	}
}

// An env var set to whitespace is not a key: without this, an exported-but-empty
// SACLI_API_KEY in a shell profile would silently overwrite a good key with "".
func TestConfigureBlankEnvFallsBackToPrompt(t *testing.T) {
	configureInTemp(t)
	t.Setenv(apiKeyEnvVar, "   ")
	prompted := answerPrompt(t, "eyJ-after-blank-env")

	if _, err := captureOutput(t, func() error { return runConfigure(nil) }); err != nil {
		t.Fatal(err)
	}
	if !prompted() {
		t.Error("a blank env var should be treated as unset and prompt instead")
	}
	if got := storedKey(t); got != "eyJ-after-blank-env" {
		t.Errorf("cloud_api_key = %q, want the prompted value", got)
	}
}

func TestConfigureKeepsExistingConfig(t *testing.T) {
	configureInTemp(t)
	if err := saveConfig(&Config{CloudAPIKey: "eyJ-existing"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(apiKeyEnvVar, "eyJ-replacement")

	if _, err := captureOutput(t, func() error { return runConfigure(nil) }); err != nil {
		t.Fatal(err)
	}
	if got := storedKey(t); got != "eyJ-replacement" {
		t.Errorf("cloud_api_key = %q, want the env var value to replace the stored key", got)
	}
}

// The prompt keeps an existing key when the answer is empty, which is the one
// path where a new key is not written at all.
func TestConfigurePromptEmptyKeepsStoredKey(t *testing.T) {
	configureInTemp(t)
	t.Setenv(apiKeyEnvVar, "")
	if err := saveConfig(&Config{CloudAPIKey: "eyJ-existing"}); err != nil {
		t.Fatal(err)
	}
	answerPrompt(t, "")

	out, err := captureOutput(t, func() error { return runConfigure(nil) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "already configured") {
		t.Errorf("an existing key should be announced before the prompt: %q", out)
	}
	if got := storedKey(t); got != "eyJ-existing" {
		t.Errorf("cloud_api_key = %q, want the stored key to survive an empty answer", got)
	}
}

// The at-rest half of the problem was already fixed in saveConfig; this pins it,
// because widening the mode or moving the file is as bad as printing the key.
func TestConfigureWritesConfigPrivate(t *testing.T) {
	dir := configureInTemp(t)
	t.Setenv(apiKeyEnvVar, "eyJ-perms")

	if _, err := captureOutput(t, func() error { return runConfigure(nil) }); err != nil {
		t.Fatal(err)
	}

	// Exact modes, not upper bounds: the default umask only clears bits, so a
	// run under an ordinary umask lands on 0600/0700 and nothing else.
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0700 {
		t.Errorf("config dir mode = %04o, want 0700", got)
	}
	fileInfo, err := os.Stat(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0600 {
		t.Errorf("config.toml mode = %04o, want 0600", got)
	}
}

func TestConfigureHelpDocumentsEnvVar(t *testing.T) {
	configureInTemp(t)

	out, err := captureOutput(t, func() error { return runConfigure([]string{"--help"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{apiKeyEnvVar, "argument > " + apiKeyEnvVar + " > prompt", "deprecated"} {
		if !strings.Contains(out, want) {
			t.Errorf("configure --help does not document %q:\n%s", want, out)
		}
	}
}

// A host argument must not consume the env var. isHost() claims anything with a
// dot, so a SACLI_API_KEY set in the user's profile would otherwise be written
// as the *cloud* key while they meant to set a site password.
func TestConfigureHostArgumentLeavesTheCloudKeyAlone(t *testing.T) {
	configureInTemp(t)
	t.Setenv(apiKeyEnvVar, "eyJ-env-key")
	prompted := answerPrompt(t, "eyJ-should-not-be-used")

	out, err := captureOutput(t, func() error { return runConfigure([]string{"192.168.0.100", "--password", "sitepw"}) })
	if err != nil {
		t.Fatal(err)
	}
	if prompted() {
		t.Error("a host argument should not reach the cloud key prompt")
	}
	if got := storedKey(t); got != "" {
		t.Errorf("cloud_api_key = %q, want it left empty when configuring a host password", got)
	}
	if strings.Contains(out, "eyJ-env-key") {
		t.Errorf("the env var key reached stdout/stderr: %q", out)
	}
}
