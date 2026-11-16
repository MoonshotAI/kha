package commands

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	json "encoding/json/v2"

	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"
)

// defaultReleaseBaseURL is the OSS download site the release job publishes
// to. KHA_RELEASE_BASE_URL overrides it for GitHub Release downloads or
// mirrors; tests replace it with an httptest server.
const defaultReleaseBaseURL = "https://ha-static.moonshot.cn/kha"

var releaseBaseURL = defaultReleaseBaseURL

// updateCheckInterval bounds how often the CLI queries the release site for
// the latest stable version; updateCheckTimeout caps the one blocking
// request a stale cache may trigger.
const (
	updateCheckInterval = 24 * time.Hour
	updateCheckTimeout  = 1500 * time.Millisecond
)

// updateCheckCache records the last version check. CheckedAt stamps both
// hits and misses, so an unreachable release site does not retry on every
// invocation.
type updateCheckCache struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
}

// maybeNotifyUpdate prints a one-line stderr hint when the running CLI is
// behind the latest stable release. It never fails a command, writes to
// stdout, or spends more than updateCheckTimeout on the network. The check
// is skipped for non-release builds (development, pseudo-version), the
// upgrade command itself, and when KHA_NO_UPDATE_CHECK or CI is set
// (scripts and sandboxed workers should not see human-facing hints).
func maybeNotifyUpdate(cmd *cobra.Command) {
	current := cliVersion()
	if !isReleaseVersion(current) || cmd.Name() == "upgrade" {
		return
	}
	if os.Getenv("KHA_NO_UPDATE_CHECK") != "" || os.Getenv("CI") != "" {
		return
	}
	latest := latestVersion(cmd.Context())
	if latest == "" || compareVersions(current, latest) >= 0 {
		return
	}
	note(cmd, "kha %s is available (installed: %s) — run 'kha upgrade' to update", latest, current)
}

// latestVersion resolves the latest stable version, answering from a cache
// younger than updateCheckInterval and otherwise fetching the marker from
// the release site. Every failure — missing marker, timeout, unreadable
// cache — yields "" and stays silent.
func latestVersion(ctx context.Context) string {
	path := updateCheckCachePath()
	if path == "" {
		return fetchLatestVersion(ctx)
	}
	cache, _ := readUpdateCheckCache(path)
	if !cache.CheckedAt.IsZero() && time.Since(cache.CheckedAt) < updateCheckInterval {
		return cache.Latest
	}
	latest := fetchLatestVersion(ctx)
	if latest == "" {
		latest = cache.Latest // keep the last known version on fetch failure
	}
	writeUpdateCheckCache(path, updateCheckCache{CheckedAt: time.Now(), Latest: latest})
	return latest
}

func releaseBase() string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("KHA_RELEASE_BASE_URL")), "/"); v != "" {
		return v
	}
	return releaseBaseURL
}

// fetchLatestVersion GETs the version marker the release job writes last into
// kha/. Before the first stable release the marker does not
// exist; that and every other failure return "".
func fetchLatestVersion(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseBase()+"/stable.txt", nil)
	if err != nil {
		return ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(data))
	if !semver.IsValid("v" + v) {
		return ""
	}
	return v
}

// updateCheckCachePath locates the check cache under the user cache dir; ""
// disables caching when the platform cannot name one.
func updateCheckCachePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "kha", "update-check.json")
}

func readUpdateCheckCache(path string) (updateCheckCache, error) {
	var cache updateCheckCache
	data, err := os.ReadFile(path)
	if err != nil {
		return cache, err
	}
	return cache, json.Unmarshal(data, &cache)
}

func writeUpdateCheckCache(path string, cache updateCheckCache) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

// isReleaseVersion reports whether v is an official release version, as
// opposed to a development build: the "0.1.0-dev" default (also what
// "(devel)" builds fall back to), build metadata ("+dirty"), and Go
// pseudo-versions (timestamp+commit prerelease) are not releases —
// replacing a repo-built or go-run binary would silently discard the
// checkout. A published prerelease such as 0.4.0-dev.1 IS a release: the
// check stays precise so it never refuses an artifact the pipeline
// actually shipped.
func isReleaseVersion(v string) bool {
	if v == "0.1.0-dev" || strings.Contains(v, "+") {
		return false
	}
	if !semver.IsValid("v" + v) {
		return false
	}
	return !pseudoVersionSuffix.MatchString(v)
}

// pseudoVersionSuffix matches the tail of a Go module pseudo-version:
// -yyyymmddhhmmss-abcdefabcdef (12 lowercase hex commit chars).
var pseudoVersionSuffix = regexp.MustCompile(`-\d{14}-[0-9a-f]{12}$`)

// compareVersions orders two release versions by semver precedence, -1/0/+1.
// An unparseable side compares equal: the conservative outcome for the
// update check is no notification.
func compareVersions(a, b string) int {
	if !semver.IsValid("v"+a) || !semver.IsValid("v"+b) {
		return 0
	}
	return semver.Compare("v"+a, "v"+b)
}
