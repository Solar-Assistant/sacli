package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func useLocalTokenFile(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "local-token")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	old := localTokenPath
	localTokenPath = path
	t.Cleanup(func() { localTokenPath = old })
}

func TestExtractLocalToken(t *testing.T) {
	useLocalTokenFile(t, "s3cret\n")

	tests := []struct {
		name  string
		args  []string
		rest  []string
		token string
	}{
		{"flag absent leaves args alone", []string{"my-site", "metrics"}, []string{"my-site", "metrics"}, ""},
		{"host is implied", []string{"--local-token", "metrics"}, []string{"127.0.0.1", "metrics"}, "s3cret"},
		{"explicit host is kept", []string{"127.0.0.1", "metrics", "soc", "--local-token"}, []string{"127.0.0.1", "metrics", "soc"}, "s3cret"},
		{"explicit host with port is kept", []string{"localhost:4000", "set", "--local-token", "a:1"}, []string{"localhost:4000", "set", "a:1"}, "s3cret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rest, token, err := extractLocalToken(tt.args)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(rest, tt.rest) || token != tt.token {
				t.Errorf("got %v %q, want %v %q", rest, token, tt.rest, tt.token)
			}
		})
	}
}

func TestExtractLocalTokenConflicts(t *testing.T) {
	useLocalTokenFile(t, "s3cret")

	for _, flag := range []string{"--password", "--token"} {
		_, _, err := extractLocalToken([]string{"metrics", "--local-token", flag, "x"})
		if err == nil || !strings.Contains(err.Error(), "--local-token cannot be combined with "+flag) {
			t.Errorf("%s: got %v", flag, err)
		}
	}
}

func TestExtractLocalTokenUnreadable(t *testing.T) {
	useLocalTokenFile(t, "")
	want := "cannot read " + localTokenPath + " (run as the solar-assistant user or root, with the app running)"

	if _, _, err := extractLocalToken([]string{"--local-token", "metrics"}); err == nil || err.Error() != want {
		t.Errorf("empty file: got %v", err)
	}

	os.Remove(localTokenPath)
	if _, _, err := extractLocalToken([]string{"--local-token", "metrics"}); err == nil || err.Error() != want {
		t.Errorf("missing file: got %v", err)
	}
}
