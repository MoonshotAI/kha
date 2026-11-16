package commands

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// skillFiles walks the embedded skill tree and asserts every file exists
// under dir with identical content.
func assertSkillTree(t *testing.T, dir string) {
	t.Helper()
	err := fs.WalkDir(skillFS, skillRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		want, err := skillFS.ReadFile(path)
		if err != nil {
			return err
		}
		have, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(path, skillRoot+"/"))))
		if err != nil {
			t.Errorf("installed %s missing: %v", path, err)
			return nil
		}
		if string(have) != string(want) {
			t.Errorf("installed %s differs from the embedded skill", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSkillMarkdownFrontmatter(t *testing.T) {
	data, err := skillFS.ReadFile("skill/kha/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{"---\nname: kha\n", "description:", "kha docs", "references/workflows.md", "references/operations.md"} {
		if !strings.Contains(s, want) {
			t.Errorf("embedded SKILL.md missing %q", want)
		}
	}
}

func TestInstallSkillToDir(t *testing.T) {
	isolateEnv(t)
	dir := t.TempDir()
	stdout, stderr, err := runCmd(t, "", "install-skill", "--dir", dir)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty (notes go to stderr)", stdout)
	}
	assertSkillTree(t, filepath.Join(dir, "kha"))
	if !strings.Contains(stderr, "skill installed: "+filepath.Join(dir, "kha")) {
		t.Errorf("stderr = %q, want the installed note", stderr)
	}
}

func TestInstallSkillScopes(t *testing.T) {
	isolateEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	t.Chdir(cwd)
	if _, _, err := runCmd(t, "", "install-skill"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".agents", "skills", "kha", "SKILL.md")); err != nil {
		t.Errorf("project install missing: %v", err)
	}
	if _, _, err := runCmd(t, "", "install-skill", "-g"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".agents", "skills", "kha", "SKILL.md")); err != nil {
		t.Errorf("global install missing: %v", err)
	}
}

func TestInstallSkillIdempotentThenUpdate(t *testing.T) {
	isolateEnv(t)
	dir := t.TempDir()
	if _, _, err := runCmd(t, "", "install-skill", "--dir", dir); err != nil {
		t.Fatal(err)
	}
	_, stderr, err := runCmd(t, "", "install-skill", "--dir", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "already up to date") {
		t.Errorf("second run stderr = %q, want already up to date", stderr)
	}
	stale := filepath.Join(dir, "kha", "references", "workflows.md")
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, err = runCmd(t, "", "install-skill", "--dir", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "skill updated: "+filepath.Join(dir, "kha")) {
		t.Errorf("third run stderr = %q, want the updated note", stderr)
	}
	assertSkillTree(t, filepath.Join(dir, "kha"))
}

func TestInstallSkillUsageErrors(t *testing.T) {
	isolateEnv(t)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"global with dir", []string{"install-skill", "--dir", "x", "-g"}, "--global has no effect with --dir"},
		{"agent flag removed", []string{"install-skill", "--agent", "kimi-code"}, "unknown flag: --agent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runCmd(t, "", tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if code := exitCode(err); code != exitUsage {
				t.Errorf("exitCode = %d, want %d", code, exitUsage)
			}
		})
	}
}

func TestInstallSkillRejectsSymlinks(t *testing.T) {
	for _, relative := range []string{
		".agents", ".agents/skills", ".agents/skills/kha",
		".agents/skills/kha/references", ".agents/skills/kha/SKILL.md",
	} {
		t.Run(relative, func(t *testing.T) {
			isolateEnv(t)
			project := t.TempDir()
			t.Chdir(project)
			outside := t.TempDir()
			external := filepath.Join(outside, "keep")
			if err := os.WriteFile(external, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(project, filepath.FromSlash(relative))
			if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
				t.Fatal(err)
			}
			target := outside
			if strings.HasSuffix(relative, "SKILL.md") {
				target = external
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			_, _, err := runCmd(t, "", "install-skill")
			if err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("error = %v, want symlink rejection", err)
			}
			got, err := os.ReadFile(external)
			if err != nil || string(got) != "keep" {
				t.Fatalf("external file = %q, %v; want unchanged", got, err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 1 {
				t.Fatalf("external directory changed: %v, %v", entries, err)
			}
		})
	}
}
