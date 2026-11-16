// Package config manages the kha CLI profile file
// ($XDG_CONFIG_HOME/kha/config.yaml, default ~/.config/kha/config.yaml).
//
// The file stores connection settings and keychain references, with support
// for legacy plaintext keys. It always has mode 0600:
// Save writes it 0600, and Load tightens the permissions of a pre-existing
// file that is more permissive. Only the CLI reads this file; the SDK itself
// only knows KIMI_API_KEY.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Profile is one named credential set.
type Profile struct {
	// APIKey is a legacy plaintext key, read until the next login.
	APIKey string `yaml:"api_key,omitempty"`
	// APIKeyRef identifies the key in the system keychain. New logins store
	// only this reference and connection settings in the profile.
	APIKeyRef string `yaml:"api_key_ref,omitempty"`
	// BaseURL overrides the API root for this profile (debugging, proxies).
	// Empty means the SDK default.
	BaseURL string `yaml:"base_url,omitempty"`
	// APIVersion overrides the kimi-api-version header for this profile
	// (debugging against a deployment that serves another version). Empty
	// means the SDK's pinned version.
	APIVersion string `yaml:"api_version,omitempty"`
}

// Config is the on-disk credential file.
type Config struct {
	// Current names the profile used when neither --profile nor
	// KHA_PROFILE selects one.
	Current string `yaml:"current,omitempty"`
	// Profiles maps profile names to credentials.
	Profiles map[string]*Profile `yaml:"profiles,omitempty"`
}

// Path returns the credential file path: $XDG_CONFIG_HOME/kha/config.yaml
// when XDG_CONFIG_HOME is set, ~/.config/kha/config.yaml otherwise.
func Path() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "kha", "config.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home directory: %w", err)
	}
	return filepath.Join(home, ".config", "kha", "config.yaml"), nil
}

// Load reads the credential file. A missing file yields an empty Config and
// no error. When the file exists but is readable or writable by anyone but
// its owner, Load tightens it to 0600 before parsing.
func Load() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		// The file holds API keys: repair, do not just warn.
		if err := os.Chmod(p, 0o600); err != nil {
			return nil, fmt.Errorf("tightening permissions of %s to 0600 (was %04o): %w", p, perm, err)
		}
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", p, err)
	}
	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", p, err)
	}
	return cfg, nil
}

// Save replaces the config after writing and syncing a private temporary file.
// The parent directory is created with mode 0700 and the file with mode 0600.
func (c *Config) Save() error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".config-*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", p, err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing %s: %w", p, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("syncing %s: %w", p, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", p, err)
	}
	if err := os.Rename(f.Name(), p); err != nil {
		return fmt.Errorf("replacing %s: %w", p, err)
	}
	return nil
}

// Profile returns the named profile, or nil when it does not exist.
func (c *Config) GetProfile(name string) *Profile {
	if c.Profiles == nil {
		return nil
	}
	return c.Profiles[name]
}

// SetProfile stores the profile under name, making it current when the
// config has no current profile yet.
func (c *Config) SetProfile(name string, p Profile) {
	if c.Profiles == nil {
		c.Profiles = map[string]*Profile{}
	}
	c.Profiles[name] = &p
	if c.Current == "" {
		c.Current = name
	}
}

// DeleteProfile removes the named profile. It reports whether the profile
// existed. When the deleted profile was current, Current is cleared (the
// next login re-anchors it).
func (c *Config) DeleteProfile(name string) bool {
	if c.GetProfile(name) == nil {
		return false
	}
	delete(c.Profiles, name)
	if c.Current == name {
		c.Current = ""
	}
	return true
}
