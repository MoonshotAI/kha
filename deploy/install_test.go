//go:build !windows

package deploy_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallScript(t *testing.T) {
	payload := []byte("#!/bin/sh\nprintf 'kha 0.5.0\\n'\n")
	sum := sha256.Sum256(payload)
	checksums := fmt.Sprintf("%s  kha-linux-amd64\n", hex.EncodeToString(sum[:]))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /stable.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "0.5.0\n")
	})
	mux.HandleFunc("GET /v0.5.0/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, checksums)
	})
	mux.HandleFunc("GET /v0.5.0/kha-linux-amd64", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	dir := t.TempDir()
	installDir := filepath.Join(dir, "custom bin")
	cmd := exec.Command("/bin/sh", "install.sh")
	cmd.Dir = "."
	cmd.Env = append(os.Environ(),
		"KHA_RELEASE_BASE_URL="+server.URL,
		"KHA_INSTALL_DIR="+installDir,
		"TEST_UNAME_S=Linux",
		"TEST_UNAME_M=x86_64",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}
	installed, err := os.ReadFile(filepath.Join(installDir, "kha"))
	if err != nil {
		t.Fatalf("reading installed binary: %v", err)
	}
	if string(installed) != string(payload) {
		t.Errorf("installed binary mismatch: got %q, want %q", installed, payload)
	}
	if !strings.Contains(string(out), "kha 0.5.0") {
		t.Errorf("expected installed binary version output, got %q", out)
	}
}

func TestInstallScriptRejectsBadChecksum(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stable.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "0.5.0\n")
	})
	mux.HandleFunc("GET /v0.5.0/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, "%s  kha-linux-amd64\n", strings.Repeat("0", 64))
	})
	mux.HandleFunc("GET /v0.5.0/kha-linux-amd64", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "payload")
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	installDir := filepath.Join(t.TempDir(), "bin")
	cmd := exec.Command("/bin/sh", "install.sh")
	cmd.Dir = "."
	cmd.Env = append(os.Environ(),
		"KHA_RELEASE_BASE_URL="+server.URL,
		"KHA_INSTALL_DIR="+installDir,
		"TEST_UNAME_S=Linux",
		"TEST_UNAME_M=x86_64",
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("install.sh should fail on checksum mismatch, output: %s", out)
	}
	if _, statErr := os.Stat(filepath.Join(installDir, "kha")); !os.IsNotExist(statErr) {
		t.Errorf("binary should not be installed on checksum mismatch, stat: %v", statErr)
	}
}
