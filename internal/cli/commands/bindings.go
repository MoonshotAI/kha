package commands

import (
	"regexp"
	"strings"

	"github.com/MoonshotAI/kha"
	"github.com/spf13/cobra"
)

// Resource binding shorthands. The generic --resource '{...}' flag stays the
// escape hatch; these cover the common shapes without nested YAML. Value
// syntax follows two rules used across the CLI:
//
//	id@version   pin a version (skills, plugins, agents)
//	id:modifier  attach a non-version modifier (memory access, repo branch)
type resourceBindingFlags struct {
	vaults       *strList
	memoryStores *strList
	fileIDs      *strList
	repos        *strList
	repoToken    string
	tokenStdin   bool
}

var githubRepoURL = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// addResourceBindingFlags registers the shorthand binding flags shared by
// sessions create and triggers create (both take a SessionResource list).
// Only creation-time bindings are covered: vault, memory-store, and
// repository bindings are fixed at creation; files can also be added later
// via sessions resources add.
func addResourceBindingFlags(cmd *cobra.Command) *resourceBindingFlags {
	f := &resourceBindingFlags{}
	f.vaults = addStrListFlag(cmd, "vault", "bind a Vault by id (repeatable)")
	f.memoryStores = addStrListFlag(cmd, "memory-store", "bind a Memory Store as id or id:read_only|read_write (repeatable)")
	f.fileIDs = addStrListFlag(cmd, "file-id", "bind an uploaded file by id, file_... (repeatable)")
	f.repos = addStrListFlag(cmd, "repo", "clone a GitHub repository as https://github.com/owner/repo or ...:branch (repeatable)")
	cmd.Flags().StringVar(&f.repoToken, "repo-token", "", "authorization token for private --repo URLs (visible in shell history; prefer --repo-token-stdin)")
	cmd.Flags().BoolVar(&f.tokenStdin, "repo-token-stdin", false, "read the --repo authorization token from stdin")
	cmd.MarkFlagsMutuallyExclusive("repo-token", "repo-token-stdin")
	return f
}

// resolve builds the SessionResource list from the shorthand flags. The
// repo token applies to every --repo value; per-repo tokens need --resource.
func (f *resourceBindingFlags) resolve(cmd *cobra.Command) ([]*kha.SessionResource, error) {
	var out []*kha.SessionResource
	for _, id := range f.vaults.out {
		out = append(out, kha.NewSessionResourceSessionResourceVault(&kha.SessionResourceVault{
			Type:    kha.SessionResourceVaultTypeVault,
			VaultID: id,
		}))
	}
	for _, raw := range f.memoryStores.out {
		id, access, err := splitModifier("memory-store", raw)
		if err != nil {
			return nil, err
		}
		// access is a required field on the binding; the shorthand defaults
		// to read_write (the mode an interactive Session almost always wants)
		// and :read_only pins the read-only mount.
		if access == "" {
			access = string(kha.MemoryAccessReadWrite)
		}
		switch kha.MemoryAccess(access) {
		case kha.MemoryAccessReadOnly, kha.MemoryAccessReadWrite:
		default:
			return nil, errUsage("--memory-store access must be read_only or read_write, got %q", access)
		}
		out = append(out, kha.NewSessionResourceSessionResourceMemoryStore(&kha.SessionResourceMemoryStore{
			Type:          kha.SessionResourceMemoryStoreTypeMemoryStore,
			MemoryStoreID: id,
			Access:        kha.MemoryAccess(access),
		}))
	}
	for _, id := range f.fileIDs.out {
		out = append(out, kha.NewSessionResourceSessionResourceFile(&kha.SessionResourceFile{
			Type:   kha.SessionResourceFileTypeFile,
			FileID: id,
		}))
	}
	token, err := f.resolveToken(cmd)
	if err != nil {
		return nil, err
	}
	for _, raw := range f.repos.out {
		url, branch, err := splitModifier("repo", raw)
		if err != nil {
			return nil, err
		}
		if !githubRepoURL.MatchString(url) {
			return nil, errUsage("--repo wants https://github.com/<owner>/<repo>[:branch], got %q", url)
		}
		repo := &kha.SessionResourceGitHubRepository{
			Type: kha.SessionResourceGitHubRepositoryTypeGithubRepository,
			URL:  url,
		}
		if token != "" {
			repo.AuthorizationToken = new(token)
		}
		if branch != "" {
			repo.Checkout = kha.NewRepositoryCheckoutRepositoryBranchCheckout(&kha.RepositoryBranchCheckout{
				Type: kha.RepositoryBranchCheckoutTypeBranch,
				Name: branch,
			})
		}
		out = append(out, kha.NewSessionResourceSessionResourceGitHubRepository(repo))
	}
	if token != "" && len(f.repos.out) == 0 {
		return nil, errUsage("--repo-token requires at least one --repo")
	}
	return out, nil
}

func (f *resourceBindingFlags) resolveToken(cmd *cobra.Command) (string, error) {
	if f.tokenStdin {
		return readSecret(cmd)
	}
	return f.repoToken, nil
}

// splitModifier cuts one id:modifier value. The cut happens at the last
// colon so scheme URLs ("https://github.com/org/repo:main") keep their
// "://" intact. An empty modifier ("id:") is rejected so a trailing colon
// never silently drops the modifier.
func splitModifier(flag, raw string) (id, modifier string, err error) {
	i := strings.LastIndex(raw, ":")
	if i < 0 || raw[i:] == "://" || strings.HasPrefix(raw[i+1:], "//") {
		return raw, "", nil
	}
	id, modifier = raw[:i], raw[i+1:]
	if id == "" || modifier == "" {
		return "", "", errUsage("--%s wants id or id:modifier, got %q", flag, raw)
	}
	return id, modifier, nil
}
