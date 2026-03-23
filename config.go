package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type Config struct {
	CloudAPIKey string            `toml:"cloud_api_key"`
	Passwords   map[string]string `toml:"site_passwords"`
}

func configDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine config directory: %w", err)
	}
	return filepath.Join(base, "solar-assistant"), nil
}

func configPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

func loadConfig() (*Config, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}

	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("cannot parse config: %w", err)
	}
	return &cfg, nil
}

// ── authorize cache ───────────────────────────────────────────────────────────

type CachedAuthorize struct {
	Host      string `toml:"host"`
	LocalIP   string `toml:"local_ip"`
	SiteID    int    `toml:"site_id"`
	SiteName  string `toml:"site_name"`
	SiteKey   string `toml:"site_key"`
	Token     string `toml:"token"`
	ExpiresAt string `toml:"expires_at"`
}

type AuthorizeCache struct {
	Sites map[string]CachedAuthorize `toml:"sites"`
}

func authorizeCachePath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "authorize_cache.toml"), nil
}

func loadAuthorizeCache() (*AuthorizeCache, error) {
	path, err := authorizeCachePath()
	if err != nil {
		return nil, err
	}
	var cache AuthorizeCache
	if _, err := toml.DecodeFile(path, &cache); err != nil {
		if os.IsNotExist(err) {
			return &AuthorizeCache{Sites: map[string]CachedAuthorize{}}, nil
		}
		return nil, fmt.Errorf("cannot parse authorize cache: %w", err)
	}
	if cache.Sites == nil {
		cache.Sites = map[string]CachedAuthorize{}
	}
	return &cache, nil
}

func saveAuthorizeCache(cache *AuthorizeCache) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("cannot create config directory: %w", err)
	}
	path := filepath.Join(dir, "authorize_cache.toml")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("cannot open authorize cache for writing: %w", err)
	}
	defer f.Close()
	if err := toml.NewEncoder(f).Encode(cache); err != nil {
		return fmt.Errorf("cannot write authorize cache: %w", err)
	}
	return nil
}

// tokenExpiry decodes the exp claim from a JWT without verifying the signature.
func tokenExpiry(token string) (time.Time, error) {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 {
		return time.Time{}, fmt.Errorf("invalid token")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, err
	}
	var claims struct {
		Exp string `json:"exp"`
	}
	if err := json.Unmarshal(data, &claims); err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339Nano, claims.Exp)
}

func saveConfig(cfg *Config) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("cannot create config directory: %w", err)
	}

	path := filepath.Join(dir, "config.toml")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("cannot open config for writing: %w", err)
	}
	defer f.Close()

	if err := toml.NewEncoder(f).Encode(cfg); err != nil {
		return fmt.Errorf("cannot write config: %w", err)
	}
	return nil
}
