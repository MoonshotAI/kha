package commands

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/schollz/progressbar/v3"
)

// serveRelease stubs the OSS release layout: /v<version>/<binary> and
// /v<version>/checksums.txt, computed from the served binary content.
func serveRelease(t *testing.T, binary []byte) string {
	t.Helper()
	sum := sha256.Sum256(binary)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
			_, _ = fmt.Fprintf(w, "%x  %s\n", sum, releaseBinaryName())
		case strings.HasSuffix(r.URL.Path, "/"+releaseBinaryName()):
			_, _ = w.Write(binary)
		default:
			http.NotFound(w, r)
		}
	})
	return srv.URL
}

func TestPerformUpgrade(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		setReleaseBaseURL(t, serveRelease(t, []byte("new-binary")))
		exe := filepath.Join(t.TempDir(), releaseBinaryName())
		if err := os.WriteFile(exe, []byte("old-binary"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := performUpgrade(t.Context(), io.Discard, exe, "0.9.9"); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(exe)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "new-binary" {
			t.Fatalf("binary = %q; want new-binary", got)
		}
		if st, _ := os.Stat(exe); st.Mode().Perm() != 0o755 {
			t.Fatalf("mode = %o; want 755", st.Mode().Perm())
		}
		leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(exe), ".kha-upgrade-*"))
		if err != nil || len(leftovers) != 0 {
			t.Fatalf("staged files left behind: %v, %v", leftovers, err)
		}
	})
}

func TestPerformUpgradeChecksumMismatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sum := sha256.Sum256([]byte("other-binary"))
		srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/checksums.txt") {
				_, _ = fmt.Fprintf(w, "%x  %s\n", sum, releaseBinaryName())
				return
			}
			_, _ = w.Write([]byte("new-binary"))
		})
		setReleaseBaseURL(t, srv.URL)
		dir := t.TempDir()
		exe := filepath.Join(dir, releaseBinaryName())
		if err := os.WriteFile(exe, []byte("old-binary"), 0o755); err != nil {
			t.Fatal(err)
		}
		err := performUpgrade(t.Context(), io.Discard, exe, "0.9.9")
		if err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
			t.Fatalf("err = %v; want SHA-256 mismatch", err)
		}
		if got, _ := os.ReadFile(exe); string(got) != "old-binary" {
			t.Fatalf("binary was replaced despite mismatch: %q", got)
		}
		leftovers, _ := filepath.Glob(filepath.Join(dir, ".kha-upgrade-*"))
		if len(leftovers) != 0 {
			t.Fatalf("staged files left behind: %v", leftovers)
		}
	})
}

func TestPerformUpgradeMissingChecksumRecord(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/checksums.txt") {
				_, _ = fmt.Fprint(w, "deadbeef  kha-other\n")
				return
			}
			_, _ = w.Write([]byte("new-binary"))
		})
		setReleaseBaseURL(t, srv.URL)
		exe := filepath.Join(t.TempDir(), releaseBinaryName())
		if err := os.WriteFile(exe, []byte("old-binary"), 0o755); err != nil {
			t.Fatal(err)
		}
		err := performUpgrade(t.Context(), io.Discard, exe, "0.9.9")
		if err == nil || !strings.Contains(err.Error(), "no record") {
			t.Fatalf("err = %v; want missing-record error", err)
		}
	})
}

func TestSelectChecksum(t *testing.T) {
	checksums := "aaa  kha-linux-amd64\nbbb  kha-darwin-arm64\nccc  checksums.txt\n"
	if got, err := selectChecksum(checksums, "kha-darwin-arm64"); err != nil || got != "bbb" {
		t.Fatalf("got %q, %v; want bbb, nil", got, err)
	}
	if _, err := selectChecksum(checksums, "kha-windows-amd64.exe"); err == nil {
		t.Fatal("missing record must fail")
	}
	if _, err := selectChecksum(checksums+"bbb  kha-darwin-arm64\n", "kha-darwin-arm64"); err == nil {
		t.Fatal("duplicate record must fail")
	}
}

func TestPerformUpgradeDownloadTimeout(t *testing.T) {
	// Real time, not synctest: a stalled connection keeps a real network
	// read pending, the bubble never goes idle, and the fake clock would
	// never fire the deadline. 50ms real is fast enough.
	old := downloadTimeout
	downloadTimeout = 50 * time.Millisecond
	t.Cleanup(func() { downloadTimeout = old })
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // hang until the client gives up
	})
	setReleaseBaseURL(t, srv.URL)
	dir := t.TempDir()
	exe := filepath.Join(dir, releaseBinaryName())
	if err := os.WriteFile(exe, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := performUpgrade(t.Context(), io.Discard, exe, "0.9.9")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v; want the overall timeout error", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old-binary" {
		t.Fatalf("binary was replaced despite the stall: %q", got)
	}
}

func TestInstalledSkillTargets(t *testing.T) {
	putSkill := func(root string) {
		t.Helper()
		dir := filepath.Join(root, ".agents", "skills", skillName)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("skill"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("none", func(t *testing.T) {
		if got := installedSkillTargets(t.TempDir(), t.TempDir()); len(got) != 0 {
			t.Fatalf("got %v; want none", got)
		}
	})
	t.Run("project_only", func(t *testing.T) {
		home, cwd := t.TempDir(), t.TempDir()
		putSkill(cwd)
		got := installedSkillTargets(home, cwd)
		if len(got) != 1 || got[0].dir != cwd || got[0].global {
			t.Fatalf("got %+v; want project at cwd", got)
		}
	})
	t.Run("both_and_dedup", func(t *testing.T) {
		home, cwd := t.TempDir(), t.TempDir()
		putSkill(home)
		putSkill(cwd)
		if got := installedSkillTargets(home, cwd); len(got) != 2 {
			t.Fatalf("got %+v; want project and global", got)
		}
		// cwd == home must not refresh the same directory twice.
		if got := installedSkillTargets(home, home); len(got) != 1 || got[0].global {
			t.Fatalf("dedup got %+v; want the single project entry", got)
		}
	})
}

func TestRunUpgradeDevBuildRefused(t *testing.T) {
	isolateUpdateCheck(t)
	for _, v := range []string{"0.1.0-dev", "0.0.0-20261014031558-3dfd64a7b8a4", "0.0.0-20261014031558-3dfd64a7b8a4+dirty"} {
		t.Run(v, func(t *testing.T) {
			setCLIVersion(t, v)
			_, _, err := runCmd(t, "", "upgrade")
			if exitCode(err) != 2 || !strings.Contains(err.Error(), "not an official release") {
				t.Fatalf("err=%v code=%d; want usage refusal", err, exitCode(err))
			}
		})
	}
}

func TestRunUpgradeAlreadyUpToDate(t *testing.T) {
	isolateUpdateCheck(t)
	setCLIVersion(t, "0.3.0")
	synctest.Test(t, func(t *testing.T) {
		srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/stable.txt" {
				t.Errorf("unexpected request %s", r.URL.Path)
			}
			_, _ = fmt.Fprint(w, "0.3.0\n")
		})
		setReleaseBaseURL(t, srv.URL)
		_, stderr, err := runCmd(t, "", "upgrade")
		if err != nil {
			t.Fatalf("err = %v, stderr = %q", err, stderr)
		}
		if !strings.Contains(stderr, "already up to date") {
			t.Fatalf("stderr = %q; want already-up-to-date note", stderr)
		}
	})
}

func TestRunUpgradeAheadOfLatestStable(t *testing.T) {
	isolateUpdateCheck(t)
	for _, current := range []string{"0.4.0-rc.1", "0.4.0"} {
		t.Run(current, func(t *testing.T) {
			setCLIVersion(t, current)
			synctest.Test(t, func(t *testing.T) {
				srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/stable.txt" {
						t.Errorf("unexpected request %s (no download may happen)", r.URL.Path)
					}
					_, _ = fmt.Fprint(w, "0.3.2\n")
				})
				setReleaseBaseURL(t, srv.URL)
				_, stderr, err := runCmd(t, "", "upgrade")
				if err != nil {
					t.Fatalf("err = %v, stderr = %q", err, stderr)
				}
				if !strings.Contains(stderr, "ahead of the latest stable release") {
					t.Fatalf("stderr = %q; want the ahead-of-stable note", stderr)
				}
			})
		})
	}
}

func TestRunUpgradeInvalidVersion(t *testing.T) {
	isolateUpdateCheck(t)
	setCLIVersion(t, "0.3.0")
	_, _, err := runCmd(t, "", "upgrade", "--version", "latest")
	if exitCode(err) != 2 {
		t.Fatalf("exit code = %d (err %v); want 2", exitCode(err), err)
	}
}

func TestDownloadBarSilentWithoutTerminal(t *testing.T) {
	var buf strings.Builder
	bar := newDownloadBar(&buf, "kha-test", 100)
	if _, err := bar.Write(make([]byte, 100)); err != nil {
		t.Fatal(err)
	}
	endDownloadBar(&buf, bar, true)
	if buf.Len() != 0 {
		t.Fatalf("a non-terminal writer must stay silent, got %q", buf.String())
	}
}

func TestDownloadBarRendersProgress(t *testing.T) {
	var buf strings.Builder
	bar := progressbar.NewOptions64(4, downloadBarOptions(&buf, "kha-linux-amd64", true)...)
	if _, err := bar.Write([]byte("ab")); err != nil { // first write draws: 50%
		t.Fatal(err)
	}
	if _, err := bar.Write([]byte("cd")); err != nil { // final write draws: 100%
		t.Fatal(err)
	}
	endDownloadBar(&buf, bar, true)
	out := buf.String()
	for _, want := range []string{"downloading kha-linux-amd64", "50%", "100%"} {
		if !strings.Contains(out, want) {
			t.Fatalf("bar output %q missing %q", out, want)
		}
	}
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("bar output %q must end with a newline after endDownloadBar", out)
	}
}

func TestDownloadBarFailureErases(t *testing.T) {
	var buf strings.Builder
	bar := progressbar.NewOptions64(100, downloadBarOptions(&buf, "kha-linux-amd64", true)...)
	if _, err := bar.Write([]byte("ab")); err != nil {
		t.Fatal(err)
	}
	endDownloadBar(&buf, bar, false)
	// The final frame must be blank: the cursor sits on a clean line for the
	// error that follows.
	if last := strings.TrimSpace(buf.String()[strings.LastIndex(buf.String(), "\r"):]); last != "" {
		t.Fatalf("a failed download must leave a blank line, got %q", last)
	}
}
