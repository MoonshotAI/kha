package commands

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// skillName is the installed directory name (<skills-dir>/kha/) and the
// skill's frontmatter name.
const skillName = "kha"

// skillRoot is the skill package's directory inside skillFS (its contents
// install as <skills-dir>/kha/).
const skillRoot = "skill/kha"

// skillFS is the agent skill package shipped inside the binary — SKILL.md
// plus its references/ files — teaching a coding agent to drive the Kimi
// Hosted Agents API through this CLI, so `install-skill` needs no network
// or credentials. Re-running the command after a CLI upgrade is the skill's
// update path.
//
//go:embed skill
var skillFS embed.FS

func newInstallSkillCmd() *cobra.Command {
	var global bool
	var dir string
	cmd := &cobra.Command{
		Use:   "install-skill",
		Short: "Install the kha agent skill into a coding agent's skills directory (local; never touches the API)",
		Long: `Install the kha agent skill — the SKILL.md that teaches a coding agent
to drive this CLI — into an agent's skills directory. The skill ships
inside the binary; re-run after upgrading the CLI to update it.

By default the skill installs into ./.agents/skills; pass --global for
~/.agents/skills or --dir for an explicit directory. The skill lands in
<dir>/kha/SKILL.md; an up-to-date destination is left untouched.`,
		Example: `  kha install-skill                   # project: ./.agents/skills/kha/
  kha install-skill -g                # global: ~/.agents/skills/kha/
  kha install-skill --dir ~/shared/skills   # explicit directory`,
		Args: cobra.NoArgs,
	}
	cmd.Flags().BoolVarP(&global, "global", "g", false, "install to the per-user ~/.agents/skills instead of the project one")
	cmd.Flags().StringVar(&dir, "dir", "", "install into an explicit skills directory")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		return runInstallSkill(cmd, global, dir)
	})
	return cmd
}

func runInstallSkill(cmd *cobra.Command, global bool, dir string) error {
	if dir != "" && global {
		return errUsage("--global has no effect with --dir (the directory is already exact)")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	target := filepath.Join(cwd, ".agents", "skills")
	switch {
	case dir != "":
		target = expandHome(dir, home)
	case global:
		target = filepath.Join(home, ".agents", "skills")
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return err
	}
	// Project and home trees can contain links from a cloned repository.
	// An explicit destination outside those trees is chosen by the user.
	base := target
	for _, candidate := range []string{cwd, home} {
		if relative, err := filepath.Rel(candidate, target); err == nil && filepath.IsLocal(relative) {
			base = candidate
			break
		}
	}
	return installSkill(cmd, base, target)
}

// installSkill writes the embedded skill tree to <dir>/kha/. Identical
// files are left alone; differing or missing ones are overwritten (the
// skill is CLI-owned, so overwriting a stale copy is the update, not data
// loss).
func installSkill(cmd *cobra.Command, base, dir string) error {
	dest := filepath.Join(dir, skillName)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	relativeDest, err := filepath.Rel(base, dest)
	if err != nil {
		return err
	}
	// Root bounds filesystem access even if a path changes after validation.
	// Check the entire embedded tree before updating any file, including its
	// existing parent directories. A link must never redirect the installation.
	if err := fs.WalkDir(skillFS, skillRoot, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative := strings.TrimPrefix(path, skillRoot)
		target := "."
		for part := range strings.SplitSeq(filepath.Join(relativeDest, filepath.FromSlash(relative)), string(filepath.Separator)) {
			target = filepath.Join(target, part)
			info, err := root.Lstat(target)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			if err == nil && info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing to install through symlink %s", target)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	changed := 0
	existed := false
	err = fs.WalkDir(skillFS, skillRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		target := filepath.Join(relativeDest, filepath.FromSlash(strings.TrimPrefix(path, skillRoot+"/")))
		want, err := skillFS.ReadFile(path)
		if err != nil {
			return err
		}
		have, readErr := root.ReadFile(target)
		if readErr == nil {
			existed = true
			if bytes.Equal(have, want) {
				return nil
			}
		} else if !errors.Is(readErr, fs.ErrNotExist) {
			return fmt.Errorf("reading %s: %w", target, readErr)
		}
		if err := root.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := root.WriteFile(target, want, 0o644); err != nil {
			return err
		}
		changed++
		return nil
	})
	if err != nil {
		return err
	}
	switch {
	case changed == 0:
		note(cmd, "%s already up to date", dest)
	case existed:
		note(cmd, "skill updated: %s (%d file(s))", dest, changed)
	default:
		note(cmd, "skill installed: %s (%d file(s))", dest, changed)
	}
	return nil
}

func expandHome(path, home string) string {
	switch {
	case path == "~":
		return home
	case strings.HasPrefix(path, "~/"):
		return filepath.Join(home, path[2:])
	}
	return path
}
