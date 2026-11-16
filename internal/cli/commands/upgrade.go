package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/schollz/progressbar/v3"
	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"
)

// newUpgradeCmd replaces the running binary with the latest (or a requested)
// release from the OSS download site, verifying the SHA-256 checksum before
// swapping anything.
func newUpgradeCmd() *cobra.Command {
	var target string
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade the kha CLI to the latest (or a given) release",
		Long: `Upgrade the kha CLI in place: resolve the target release (the latest
stable one by default), download its binary for this platform from the
release site, verify it against the published SHA-256 checksums, and
atomically swap the running executable.

After the swap, any agent skill previously installed by 'kha install-skill'
(./.agents/skills or ~/.agents/skills) is refreshed by the new binary, so
the skill matches the upgraded CLI.`,
		Example: `  kha upgrade                     # upgrade to the latest stable release
  kha upgrade --version 0.3.0   # switch to a specific release`,
		Args: cobra.NoArgs,
	}
	cmd.Flags().StringVar(&target, "version", "", "target version (default: the latest stable release)")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		return runUpgrade(cmd, target)
	})
	return cmd
}

func runUpgrade(cmd *cobra.Command, target string) error {
	current := cliVersion()
	if !isReleaseVersion(current) {
		return errUsage("this build (%s) is not an official release; install one first — see https://ha-static.moonshot.cn/kha/install.sh", current)
	}
	if target == "" {
		target = fetchLatestVersion(cmd.Context())
		if target == "" {
			return fmt.Errorf("could not resolve the latest release from %s; check the network or pass --version", releaseBase())
		}
		// A prerelease can be ahead of the latest stable marker: switching
		// to it would be a downgrade, which a bare `upgrade` must never do.
		// Explicit --version remains the escape hatch.
		if compareVersions(current, target) > 0 {
			note(cmd, "installed %s is ahead of the latest stable release (%s); nothing to do — pass --version to switch anyway", current, target)
			return nil
		}
	}
	if !semver.IsValid("v" + target) {
		return errUsage("invalid version %q: want 0.X.Y, optionally with a prerelease suffix", target)
	}
	if target == current {
		note(cmd, "kha is already up to date (%s)", current)
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating the running binary: %w", err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return fmt.Errorf("resolving the running binary: %w", err)
	}
	cleanStaleBinaries(exe)
	if err := performUpgrade(cmd.Context(), cmd.ErrOrStderr(), exe, target); err != nil {
		return err
	}
	note(cmd, "kha upgraded: %s → %s", current, target)
	refreshInstalledSkills(cmd, exe)
	return nil
}

// downloadTimeout bounds the whole upgrade download (checksums + binary)
// as one overall budget: a hung connection must never wait forever. Tests
// shrink it.
var downloadTimeout = 5 * time.Minute

// performUpgrade downloads the target release next to the running binary,
// verifies its checksum, and atomically swaps it in. The staging file lives
// in the binary's own directory so the final rename never crosses
// filesystems. The download renders a progress bar on progress when it is a
// terminal; any other writer stays silent.
func performUpgrade(ctx context.Context, progress io.Writer, exe, target string) error {
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	base := fmt.Sprintf("%s/v%s", releaseBase(), target)
	name := releaseBinaryName()
	dir := filepath.Dir(exe)
	staged, err := os.CreateTemp(dir, ".kha-upgrade-*")
	if err != nil {
		return fmt.Errorf("cannot write next to %s: %w\nreinstall into a writable directory with install.sh / install.ps1 instead", exe, err)
	}
	stagedPath := staged.Name()
	defer func() { _ = os.Remove(stagedPath) }()
	var checksums strings.Builder
	if err := download(ctx, base+"/checksums.txt", &checksums); err != nil {
		_ = staged.Close()
		return timeoutError(ctx, fmt.Errorf("downloading checksums: %w", err))
	}
	want, err := selectChecksum(checksums.String(), name)
	if err != nil {
		_ = staged.Close()
		return err
	}
	body, total, err := openDownload(ctx, base+"/"+name)
	if err != nil {
		_ = staged.Close()
		return timeoutError(ctx, fmt.Errorf("downloading %s: %w", name, err))
	}
	sum := sha256.New()
	bar := newDownloadBar(progress, name, total)
	_, copyErr := io.Copy(io.MultiWriter(staged, sum, bar), body)
	_ = body.Close()
	endDownloadBar(progress, bar, copyErr == nil)
	if copyErr != nil {
		_ = staged.Close()
		return timeoutError(ctx, fmt.Errorf("downloading %s: %w", name, copyErr))
	}
	if err := staged.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != want {
		return fmt.Errorf("SHA-256 mismatch for %s: got %s, checksums.txt says %s; the release directory is incomplete or tampered, nothing was installed", name, got, want)
	}
	if err := os.Chmod(stagedPath, 0o755); err != nil {
		return err
	}
	if err := replaceExecutable(exe, stagedPath); err != nil {
		return fmt.Errorf("replacing %s: %w", exe, err)
	}
	return nil
}

// timeoutError maps an expired overall download deadline onto a readable
// error; a user cancellation (Ctrl-C) keeps the original one.
func timeoutError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("download timed out after %s; check the network and retry", downloadTimeout)
	}
	return err
}

// releaseBinaryName matches the artifact names built by deploy/release.py.
func releaseBinaryName() string {
	name := fmt.Sprintf("kha-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// selectChecksum picks the single checksums.txt record for name. A missing
// or duplicate record means the release directory is incomplete — install.sh
// applies the same rule before installing anything.
func selectChecksum(checksums, name string) (string, error) {
	digest := ""
	for line := range strings.Lines(checksums) {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != name {
			continue
		}
		if digest != "" {
			return "", fmt.Errorf("checksums.txt has duplicate records for %s", name)
		}
		digest = fields[0]
	}
	if digest == "" {
		return "", fmt.Errorf("checksums.txt has no record for %s", name)
	}
	return digest, nil
}

// replaceExecutable swaps the binary at target for the staged file. Both
// live in the same directory, so the rename is atomic. Windows refuses to
// overwrite a running executable but allows renaming it: the old binary
// moves aside under a per-pid name (a still-running previous `.old-*` copy
// must not block this rename) and is removed — now or, while it is still
// locked, by a later upgrade.
func replaceExecutable(target, staged string) error {
	if runtime.GOOS != "windows" {
		return os.Rename(staged, target)
	}
	stale := fmt.Sprintf("%s.old-%d", target, os.Getpid())
	if err := os.Rename(target, stale); err != nil {
		return fmt.Errorf("moving the current binary aside: %w", err)
	}
	if err := os.Rename(staged, target); err != nil {
		_ = os.Rename(stale, target)
		return fmt.Errorf("installing the new binary: %w", err)
	}
	_ = os.Remove(stale)
	return nil
}

// cleanStaleBinaries removes previous upgrades' `.old-*` leftovers next to
// the running binary. Best effort: a still-locked copy stays for the next
// run.
func cleanStaleBinaries(exe string) {
	if runtime.GOOS != "windows" {
		return
	}
	stales, err := filepath.Glob(exe + ".old-*")
	if err != nil {
		return
	}
	for _, stale := range stales {
		_ = os.Remove(stale)
	}
}

// refreshInstalledSkills re-installs the embedded skill wherever a previous
// install-skill put it: this process still carries the OLD skill, so the
// refresh must run the new binary as a subprocess. Locations without an
// installed copy are skipped — upgrade never installs fresh copies.
func refreshInstalledSkills(cmd *cobra.Command, exe string) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	targets := installedSkillTargets(home, cwd)
	if len(targets) == 0 {
		note(cmd, "no installed agent skill found; run 'kha install-skill' (project) or 'kha install-skill -g' (global) to install it")
		return
	}
	for _, t := range targets {
		args := []string{"install-skill"}
		if t.global {
			args = append(args, "-g")
		}
		child := exec.CommandContext(cmd.Context(), exe, args...)
		child.Stdout = cmd.OutOrStdout()
		child.Stderr = cmd.ErrOrStderr()
		if err := child.Run(); err != nil {
			note(cmd, "skill refresh for %s failed: %v; re-run 'kha %s'", t.dir, err, strings.Join(args, " "))
		}
	}
}

// skillRefreshTarget is one previously installed skill copy to refresh.
type skillRefreshTarget struct {
	dir    string // the .agents/skills parent (project or home)
	global bool
}

// installedSkillTargets detects existing skill installations under the two
// conventional roots, deduplicated (cwd can equal home).
func installedSkillTargets(home, cwd string) []skillRefreshTarget {
	var targets []skillRefreshTarget
	seen := map[string]bool{}
	for _, probe := range []skillRefreshTarget{
		{dir: cwd, global: false},
		{dir: home, global: true},
	} {
		if probe.dir == "" {
			continue
		}
		skillsDir := filepath.Join(probe.dir, ".agents", "skills")
		if seen[skillsDir] || !isFile(filepath.Join(skillsDir, skillName, "SKILL.md")) {
			continue
		}
		seen[skillsDir] = true
		targets = append(targets, probe)
	}
	return targets
}

// download streams url into w, following the release site's responses; any
// non-200 status is an error.
func download(ctx context.Context, url string, w io.Writer) error {
	body, _, err := openDownload(ctx, url)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	_, err = io.Copy(w, body)
	return err
}

// openDownload GETs url and returns the response body plus its total size
// (-1 when the server sent no Content-Length); any non-200 status is an
// error.
func openDownload(ctx context.Context, url string) (io.ReadCloser, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, 0, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp.Body, resp.ContentLength, nil
}

// newDownloadBar renders the binary download as a single-line stderr
// progress bar — description, percentage, byte counters and rate. When
// progress is not a terminal the bar is invisible: writes still count bytes
// but render nothing, keeping piped stderr clean (the same rule
// waitProgress follows). An unknown or zero length degrades the bar to a
// spinner; the release site always sends a Content-Length.
func newDownloadBar(w io.Writer, name string, total int64) *progressbar.ProgressBar {
	if total <= 0 {
		total = -1
	}
	return progressbar.NewOptions64(total, downloadBarOptions(w, name, terminalFile(w) != nil)...)
}

// downloadBarOptions carries the bar's one place of configuration; tests
// force visible to exercise the rendering without a terminal.
func downloadBarOptions(w io.Writer, name string, visible bool) []progressbar.Option {
	return []progressbar.Option{
		progressbar.OptionSetDescription("downloading " + name),
		progressbar.OptionSetWriter(w),
		progressbar.OptionSetVisibility(visible),
		progressbar.OptionShowBytes(true),
		progressbar.OptionShowCount(),
		progressbar.OptionShowTotalBytes(true),
		progressbar.OptionUseIECUnits(true),
		progressbar.OptionSetWidth(15),
		progressbar.OptionThrottle(100 * time.Millisecond),
	}
}

// endDownloadBar stops the bar and leaves stderr on a fresh line so notes
// and errors never print over it. On success the completed bar stays; on
// failure the partial bar is erased — the error that follows says why.
// Finish also stops the spinner-mode render ticker.
func endDownloadBar(w io.Writer, bar *progressbar.ProgressBar, success bool) {
	if !bar.IsStarted() { // invisible bars never start
		return
	}
	_ = bar.Finish()
	if success {
		_, _ = fmt.Fprintln(w)
		return
	}
	_ = bar.Clear()
}

func isFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}
