package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathHonorsXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-test")
	p, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if want := "/tmp/xdg-test/kha/config.yaml"; p != want {
		t.Fatalf("Path() = %q, want %q", p, want)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := &Config{}
	cfg.SetProfile("work", Profile{APIKey: "sk-work", BaseURL: "https://example.internal"})
	cfg.SetProfile("play", Profile{APIKey: "sk-play"})
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	p, _ := Path()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config file mode = %04o, want 0600", perm)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Current != "work" {
		t.Fatalf("Current = %q, want work (first profile anchors)", loaded.Current)
	}
	if got := loaded.GetProfile("work"); got == nil || got.APIKey != "sk-work" || got.BaseURL != "https://example.internal" {
		t.Fatalf("work profile = %+v", got)
	}
	if got := loaded.GetProfile("play"); got == nil || got.APIKey != "sk-play" || got.BaseURL != "" {
		t.Fatalf("play profile = %+v", got)
	}
}

func TestLoadTightensLoosePermissions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	p := filepath.Join(dir, "kha", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("current: a\nprofiles:\n  a:\n    api_key: sk-a\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Current != "a" {
		t.Fatalf("Current = %q, want a", loaded.Current)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode after Load = %04o, want 0600 (loose file must be repaired)", perm)
	}
}

// A replacement remains private even when the old file had looser permissions.
func TestSaveReplacesWithPrivateFile(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o444} {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)
		p := filepath.Join(dir, "kha", "config.yaml")
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("current: old\nprofiles:\n  old:\n    api_key: sk-old\n"), mode); err != nil {
			t.Fatal(err)
		}

		cfg := &Config{}
		cfg.SetProfile("new", Profile{APIKey: "sk-new"})
		if err := cfg.Save(); err != nil {
			t.Fatalf("Save over a %04o file: %v", mode, err)
		}
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if perm := st.Mode().Perm(); perm != 0o600 {
			t.Errorf("mode after Save over %04o = %04o, want 0600", mode, perm)
		}
		loaded, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if got := loaded.GetProfile("new"); got == nil || got.APIKey != "sk-new" {
			t.Errorf("profile after Save over %04o = %+v, want the new profile", mode, got)
		}
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Current != "" || len(cfg.Profiles) != 0 {
		t.Fatalf("Load() = %+v, want empty config", cfg)
	}
}

func TestDeleteProfileClearsCurrent(t *testing.T) {
	cfg := &Config{}
	cfg.SetProfile("a", Profile{APIKey: "sk-a"})
	cfg.SetProfile("b", Profile{APIKey: "sk-b"})
	if !cfg.DeleteProfile("a") {
		t.Fatal("DeleteProfile(a) = false, want true")
	}
	if cfg.Current != "" {
		t.Fatalf("Current = %q, want cleared after deleting the current profile", cfg.Current)
	}
	if cfg.DeleteProfile("missing") {
		t.Fatal("DeleteProfile(missing) = true, want false")
	}
	// SetProfile re-anchors Current when it is empty; deleting that profile
	// clears Current again.
	cfg.SetProfile("b", Profile{APIKey: "sk-b2"})
	cfg.DeleteProfile("b")
	if cfg.Current != "" {
		t.Fatalf("Current = %q, want empty (b was re-anchored then deleted)", cfg.Current)
	}
}

func TestSaveFailedReplacementCleansTemporaryFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(p, "keep")
	if err := os.WriteFile(sentinel, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (&Config{}).Save(); err == nil {
		t.Fatal("expected rename failure")
	}
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "original" {
		t.Fatalf("destination changed: %q, %v", data, err)
	}
	entries, err := os.ReadDir(filepath.Dir(p))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary file remains: %v, %v", entries, err)
	}
}
