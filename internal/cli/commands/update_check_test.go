package commands

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

func setCLIVersion(t *testing.T, v string) {
	t.Helper()
	old := version
	version = v
	t.Cleanup(func() { version = old })
}

func setReleaseBaseURL(t *testing.T, url string) {
	t.Helper()
	old := releaseBaseURL
	releaseBaseURL = url
	t.Cleanup(func() { releaseBaseURL = old })
}

// isolateUpdateCheck gives the update check a private cache dir and clears
// its opt-out environment.
func isolateUpdateCheck(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("KHA_NO_UPDATE_CHECK", "")
	t.Setenv("CI", "")
	t.Setenv("KHA_RELEASE_BASE_URL", "")
}

func TestIsReleaseVersion(t *testing.T) {
	for _, tc := range []struct {
		v    string
		want bool
	}{
		{"0.3.0", true},
		{"0.3.0-rc.1", true},
		{"0.4.0-dev.1", true}, // a published prerelease is a release
		{"1.20.3", true},
		{"0.1.0-dev", false},
		{"(devel)", false},
		{"0.0.0-20261014031558-3dfd64a7b8a4", false},
		{"0.0.0-20261014031558-3dfd64a7b8a4+dirty", false},
		{"garbage", false},
	} {
		t.Run(tc.v, func(t *testing.T) {
			if got := isReleaseVersion(tc.v); got != tc.want {
				t.Fatalf("isReleaseVersion(%q) = %v; want %v", tc.v, got, tc.want)
			}
		})
	}
}

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"0.3.0", "0.3.0", 0},
		{"0.3.1", "0.3.0", 1},
		{"0.3.0", "0.3.1", -1},
		{"0.4.0", "0.10.0", -1},
		{"1.0.0", "0.99.99", 1},
		{"0.3.0-rc.1", "0.3.0", -1},
		{"0.3.0", "0.3.0-rc.1", 1},
		{"0.3.0-rc.1", "0.3.0-rc.1", 0},
		{"0.3.0-rc.1", "0.3.0-rc.2", -1},
		{"0.3.0-rc.2", "0.3.0-rc.10", -1},
		{"0.3.0-rc.1", "0.3.0-rc.1.1", -1},
		{"0.3.0-alpha", "0.3.0-alpha.1", -1},
		{"0.3.0-alpha", "0.3.0-beta", -1},
		{"0.3.0-1", "0.3.0-alpha", -1},
		{"0.1.0-dev", "0.1.0", -1},
		{"garbage", "0.3.0", 0},
		{"0.3.0", "garbage", 0},
	} {
		t.Run(tc.a+"/"+tc.b, func(t *testing.T) {
			if got := compareVersions(tc.a, tc.b); got != tc.want {
				t.Fatalf("compareVersions(%q, %q) = %d; want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestFetchLatestVersion(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       string
	}{
		{"marker", "0.4.0\n", http.StatusOK, "0.4.0"},
		{"prerelease_marker", "0.4.0-rc.1\n", http.StatusOK, "0.4.0-rc.1"},
		{"missing", "", http.StatusNotFound, ""},
		{"malformed", "latest\n", http.StatusOK, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateUpdateCheck(t)
			synctest.Test(t, func(t *testing.T) {
				srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/stable.txt" {
						t.Errorf("unexpected path %s", r.URL.Path)
					}
					w.WriteHeader(tc.status)
					_, _ = fmt.Fprint(w, tc.body)
				})
				setReleaseBaseURL(t, srv.URL)
				if got := fetchLatestVersion(t.Context()); got != tc.want {
					t.Fatalf("fetchLatestVersion() = %q; want %q", got, tc.want)
				}
			})
		})
	}
}

func TestLatestVersionCachesHitsAndMisses(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       string
	}{
		{"hit", "0.4.0\n", http.StatusOK, "0.4.0"},
		{"miss", "", http.StatusNotFound, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateUpdateCheck(t)
			synctest.Test(t, func(t *testing.T) {
				var requests atomic.Int64
				srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					w.WriteHeader(tc.status)
					_, _ = fmt.Fprint(w, tc.body)
				})
				setReleaseBaseURL(t, srv.URL)
				for i := range 3 {
					if got := latestVersion(t.Context()); got != tc.want {
						t.Fatalf("call %d: latestVersion() = %q; want %q", i, got, tc.want)
					}
				}
				if requests.Load() != 1 {
					t.Fatalf("requests = %d; want 1 (later calls answered from cache)", requests.Load())
				}
			})
		})
	}
}

func TestMaybeNotifyUpdate(t *testing.T) {
	for _, tc := range []struct {
		name, cliVer, marker string
		env                  map[string]string
		wantNote             bool
	}{
		{"behind", "0.3.1", "0.4.0", nil, true},
		{"up_to_date", "0.4.0", "0.4.0", nil, false},
		{"ahead", "0.5.0-rc.1", "0.4.0", nil, false},
		{"dev_build", "0.1.0-dev", "0.4.0", nil, false},
		{"pseudo_version", "0.0.0-20261014031558-3dfd64a7b8a4", "0.4.0", nil, false},
		{"opt_out", "0.3.1", "0.4.0", map[string]string{"KHA_NO_UPDATE_CHECK": "1"}, false},
		{"ci", "0.3.1", "0.4.0", map[string]string{"CI": "true"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateUpdateCheck(t)
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			setCLIVersion(t, tc.cliVer)
			synctest.Test(t, func(t *testing.T) {
				srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
					_, _ = fmt.Fprint(w, tc.marker+"\n")
				})
				setReleaseBaseURL(t, srv.URL)
				_, stderr, err := runCmd(t, "", "version")
				if err != nil {
					t.Fatal(err)
				}
				note := "kha " + tc.marker + " is available"
				if strings.Contains(stderr, note) != tc.wantNote {
					t.Fatalf("stderr = %q; want note %q present=%v", stderr, note, tc.wantNote)
				}
			})
		})
	}
}

func TestMaybeNotifyUpdateSkipsUpgradeCommand(t *testing.T) {
	isolateUpdateCheck(t)
	setCLIVersion(t, "0.3.1")
	synctest.Test(t, func(t *testing.T) {
		srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, "0.4.0\n")
		})
		setReleaseBaseURL(t, srv.URL)
		// upgrade with an invalid --version stops before replacing anything,
		// and PersistentPreRun must not have printed the update note first.
		_, stderr, err := runCmd(t, "", "upgrade", "--version", "bogus")
		if exitCode(err) != 2 {
			t.Fatalf("exit code = %d (err %v); want 2", exitCode(err), err)
		}
		if strings.Contains(stderr, "is available") {
			t.Fatalf("upgrade itself must not print the update note: %q", stderr)
		}
	})
}
