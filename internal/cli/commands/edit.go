package commands

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"slices"
	"strings"

	json "encoding/json/v2"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	kha "github.com/MoonshotAI/kha"
)

// editSpec wires one resource into the edit engine: fetch the current state,
// intersect it with the update request's top-level json fields into an
// editable YAML manifest, and send the edited manifest as the update.
// forDisplay/forRequest adapt the manifest between the API shape and what
// the user edits (memories carry content base64 in the API but as plain
// text in the editor).
type editSpec[Model, Req any] struct {
	resource   string // display name ("agent", "credential") for the header and scratch file
	id         string // resource id for the header and scratch file
	drop       []string
	fetch      func(ctx context.Context) (*Model, error)
	update     func(ctx context.Context, req Req) (*Model, error)
	print      func(cmd *cobra.Command, g *globalFlags, updated *Model) error
	forDisplay func(m map[string]any) error
	forRequest func(m map[string]any) error
}

// newEditCmd builds one `edit` subcommand. build closes over the client and
// the positional IDs, so each resource only supplies its spec.
func newEditCmd[Model, Req any](g *globalFlags, use, short string, argc int,
	complete func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective),
	build func(*kha.Client, []string) *editSpec[Model, Req]) *cobra.Command {
	cmd := &cobra.Command{
		Use:               use,
		Short:             short,
		Args:              cobra.ExactArgs(argc),
		ValidArgsFunction: complete,
	}
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		return runEdit(cmd, g, build(client, args))
	})
	return cmd
}

// runEdit drives one edit session: fetch, open the manifest in
// the editor, and loop until the update succeeds or the edit is cancelled.
// A failed parse, an invalid document, or an API 4xx reopens the file with
// the error as a comment header; saving the file unchanged or empty cancels.
func runEdit[Model, Req any](cmd *cobra.Command, g *globalFlags, spec *editSpec[Model, Req]) error {
	current, err := spec.fetch(cmd.Context())
	if err != nil {
		return err
	}
	original, err := editManifest(current, spec)
	if err != nil {
		return err
	}
	originalNorm, err := yamlToJSON(original)
	if err != nil {
		return err
	}
	// CreateTemp files are 0600 from the start; every rewrite keeps the mode.
	f, err := os.CreateTemp("", "kha-edit-"+spec.resource+"-"+spec.id+"-*.yaml")
	if err != nil {
		return fmt.Errorf("creating the edit scratch file: %w", err)
	}
	path := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(path) }()

	info := fmt.Sprintf(`# Editing %s %q — lines beginning with '#' are ignored,
# and an empty file aborts the edit. If an error occurs while saving,
# this file reopens with the error at the top.
`, spec.resource, spec.id)
	header := []byte(info)
	lastWritten := joinBytes(header, original)
	if err := os.WriteFile(path, lastWritten, 0o600); err != nil {
		return fmt.Errorf("writing the edit scratch file: %w", err)
	}
	cancel := func() error {
		note(cmd, "Edit cancelled, no changes made.")
		return nil
	}
	for {
		if err := launchEditor(path); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// reopen rewrites the scratch file with the failure as a comment
		// block above the user's own content; saving that file unchanged
		// then cancels via the bytes.Equal check above.
		reopen := func(cause error) error {
			body := data
			if bytes.HasPrefix(data, header) {
				body = data[len(header):]
			}
			msg := strings.ReplaceAll(strings.TrimSpace(apiErrorMessage(cause)), "\n", "\n# ")
			header = []byte(info + "# Edit failed: " + msg + "\n")
			lastWritten = joinBytes(header, body)
			return os.WriteFile(path, lastWritten, 0o600)
		}
		if bytes.Equal(data, lastWritten) {
			return cancel()
		}
		var edited any
		if err := yaml.Unmarshal(data, &edited); err != nil {
			if werr := reopen(err); werr != nil {
				return werr
			}
			continue
		}
		if edited == nil { // empty file or comments only
			return cancel()
		}
		if norm, err := json.Marshal(edited); err == nil && bytes.Equal(norm, originalNorm) {
			return cancel()
		}
		req, err := decodeEditRequest(edited, spec)
		if err != nil {
			if werr := reopen(err); werr != nil {
				return werr
			}
			continue
		}
		updated, err := spec.update(cmd.Context(), req)
		switch {
		case err == nil:
			return spec.print(cmd, g, updated)
		case isClientRejection(err):
			if werr := reopen(err); werr != nil {
				return werr
			}
		default:
			return err
		}
	}
}

// editManifest builds the YAML document the user edits: the resource's
// current state intersected with the update request's top-level json fields
// (spec.drop removes fields whose read shape does not round-trip into the
// request — an Agent's multiagent reads back resolved, not as a roster), so
// concurrency tokens like an Agent's version or a Memory's content_sha256
// ride along untouched.
func editManifest[Model, Req any](current *Model, spec *editSpec[Model, Req]) ([]byte, error) {
	data, err := json.Marshal(current)
	if err != nil {
		return nil, err
	}
	var full map[string]any
	if err := json.Unmarshal(data, &full); err != nil {
		return nil, err
	}
	fields := make(map[string]any, len(full))
	t := reflect.TypeFor[Req]()
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" || slices.Contains(spec.drop, name) {
			continue
		}
		if v, ok := full[name]; ok {
			fields[name] = v
		}
	}
	if spec.forDisplay != nil {
		if err := spec.forDisplay(fields); err != nil {
			return nil, err
		}
	}
	return yaml.Marshal(fields)
}

// decodeEditRequest strict-decodes the edited document into the update
// request: unknown fields are rejected so a typo reopens the file with an
// error instead of being silently dropped.
func decodeEditRequest[Model, Req any](edited any, spec *editSpec[Model, Req]) (Req, error) {
	var req Req
	fields, ok := edited.(map[string]any)
	if !ok {
		return req, errors.New("the edited document must be a mapping of field names to values")
	}
	if spec.forRequest != nil {
		if err := spec.forRequest(fields); err != nil {
			return req, err
		}
	}
	if err := rejectUnknownEditFields[Req](fields); err != nil {
		return req, err
	}
	data, err := json.Marshal(fields)
	if err != nil {
		return req, err
	}
	if err := json.Unmarshal(data, &req, json.RejectUnknownMembers(true)); err != nil {
		return req, err
	}
	return req, nil
}

// rejectUnknownEditFields enforces the strict-decode contract at the top
// level: the generated types' custom unmarshalers do not honor the caller's
// RejectUnknownMembers option, so a misspelled field name would otherwise
// vanish from the update without an error.
func rejectUnknownEditFields[Req any](fields map[string]any) error {
	known := make(map[string]bool)
	for f := range reflect.TypeFor[Req]().Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name != "" && name != "-" {
			known[name] = true
		}
	}
	for name := range fields {
		if !known[name] {
			return fmt.Errorf("unknown field %q", name)
		}
	}
	return nil
}

// resolveEditor picks the editor for edit commands: KHA_EDITOR wins over
// EDITOR; the fallback is vi (notepad on Windows).
func resolveEditor() string {
	if e := os.Getenv("KHA_EDITOR"); e != "" {
		return e
	}
	if e := os.Getenv("EDITOR"); e != "" {
		return e
	}
	if runtime.GOOS == "windows" {
		return "notepad"
	}
	return "vi"
}

// launchEditor runs the editor on path attached to the real terminal (never
// the cobra buffers, which tests replace). The command goes through the
// shell so configurations carrying arguments ("code --wait") work.
func launchEditor(path string) error {
	editor := resolveEditor()
	var c *exec.Cmd
	if runtime.GOOS == "windows" {
		c = exec.Command("cmd", "/C", editor+" "+path)
	} else {
		c = exec.Command("sh", "-c", editor+` "$@"`, "kha-edit", path)
	}
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("editor %q failed: %w", editor, err)
	}
	return nil
}

// isClientRejection reports whether err is an API 4xx: the edit loop reopens
// the file with the message so the user can fix and retry.
func isClientRejection(err error) bool {
	if apiErr, ok := errors.AsType[kha.HTTPError](err); ok {
		return apiErr.StatusCode() >= 400 && apiErr.StatusCode() < 500
	}
	return false
}

func joinBytes(a, b []byte) []byte {
	out := make([]byte, 0, len(a)+len(b))
	return append(append(out, a...), b...)
}

// memoryContentForDisplay decodes the manifest's base64 content into the
// plain text the user edits.
func memoryContentForDisplay(m map[string]any) error {
	s, ok := m["content"].(string)
	if !ok {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return fmt.Errorf("decoding the memory's current content: %w", err)
	}
	m["content"] = string(raw)
	return nil
}

// memoryContentForRequest re-encodes the edited plain-text content for the
// API.
func memoryContentForRequest(m map[string]any) error {
	if s, ok := m["content"].(string); ok {
		m["content"] = base64.StdEncoding.EncodeToString([]byte(s))
	}
	return nil
}

func newAgentsEditCmd(g *globalFlags) *cobra.Command {
	return newEditCmd(g, "edit <id>", "Edit an Agent in your editor", 1, agentIDCompletion,
		func(c *kha.Client, args []string) *editSpec[kha.Agent, kha.UpdateAgentRequest] {
			return &editSpec[kha.Agent, kha.UpdateAgentRequest]{
				resource: "agent",
				id:       args[0],
				drop:     []string{"multiagent"},
				fetch: func(ctx context.Context) (*kha.Agent, error) {
					aResult, err := c.Agents.Get(ctx, kha.GetAgentRequest{ID: args[0]})
					return aResult.Body, err
				},
				update: func(ctx context.Context, req kha.UpdateAgentRequest) (*kha.Agent, error) {
					aResult, err := c.Agents.Update(ctx, kha.UpdateAgentHTTPRequest{ID: args[0], Body: &req})
					return aResult.Body, err
				},
				print: func(cmd *cobra.Command, g *globalFlags, a *kha.Agent) error {
					note(cmd, "agent %s updated (new version %s)", a.ID, a.Version)
					return printOne(cmd, g, agentHeaders, agentRow(a), a)
				},
			}
		})
}

func newSessionsEditCmd(g *globalFlags) *cobra.Command {
	return newEditCmd(g, "edit <id>", "Edit a Session in your editor", 1, sessionIDCompletion,
		func(c *kha.Client, args []string) *editSpec[kha.Session, kha.UpdateSessionRequest] {
			return &editSpec[kha.Session, kha.UpdateSessionRequest]{
				resource: "session",
				id:       args[0],
				fetch: func(ctx context.Context) (*kha.Session, error) {
					hResult, err := c.Sessions.Get(ctx, kha.GetSessionRequest{ID: args[0]})
					return hResult.Body, err
				},
				update: func(ctx context.Context, req kha.UpdateSessionRequest) (*kha.Session, error) {
					hResult, err := c.Sessions.Update(ctx, kha.UpdateSessionHTTPRequest{ID: args[0], Body: &req})
					return hResult.Body, err
				},
				print: func(cmd *cobra.Command, g *globalFlags, s *kha.Session) error {
					note(cmd, "session %s updated", s.ID)
					return printOne(cmd, g, sessionHeaders, sessionRow(s), s)
				},
			}
		})
}

func newVaultsEditCmd(g *globalFlags) *cobra.Command {
	return newEditCmd(g, "edit <id>", "Edit a Vault in your editor", 1, vaultIDCompletion,
		func(c *kha.Client, args []string) *editSpec[kha.Vault, kha.UpdateVaultRequest] {
			return &editSpec[kha.Vault, kha.UpdateVaultRequest]{
				resource: "vault",
				id:       args[0],
				fetch: func(ctx context.Context) (*kha.Vault, error) {
					vResult, err := c.Vaults.Get(ctx, kha.GetVaultRequest{ID: args[0]})
					return vResult.Body, err
				},
				update: func(ctx context.Context, req kha.UpdateVaultRequest) (*kha.Vault, error) {
					vResult, err := c.Vaults.Update(ctx, kha.UpdateVaultHTTPRequest{ID: args[0], Body: &req})
					return vResult.Body, err
				},
				print: func(cmd *cobra.Command, g *globalFlags, v *kha.Vault) error {
					note(cmd, "vault %s updated", v.ID)
					return printOne(cmd, g, vaultHeaders, vaultRow(v), v)
				},
			}
		})
}

func newVaultsCredentialsEditCmd(g *globalFlags) *cobra.Command {
	return newEditCmd(g, "edit <vault-id> <credential-id>", "Edit a credential in your editor", 2, vaultCredentialIDCompletion,
		func(c *kha.Client, args []string) *editSpec[kha.VaultCredential, kha.UpdateVaultCredentialRequest] {
			return &editSpec[kha.VaultCredential, kha.UpdateVaultCredentialRequest]{
				resource: "credential",
				id:       args[1],
				fetch: func(ctx context.Context) (*kha.VaultCredential, error) {
					credResult, err := c.Vaults.GetCredential(ctx, kha.GetVaultCredentialRequest{ID: args[0], CredentialID: args[1]})
					return credResult.Body, err
				},
				update: func(ctx context.Context, req kha.UpdateVaultCredentialRequest) (*kha.VaultCredential, error) {
					credResult, err := c.Vaults.UpdateCredential(ctx, kha.UpdateVaultCredentialHTTPRequest{ID: args[0], CredentialID: args[1], Body: &req})
					return credResult.Body, err
				},
				print: func(cmd *cobra.Command, g *globalFlags, cred *kha.VaultCredential) error {
					note(cmd, "credential %s updated", cred.ID)
					return printOne(cmd, g, credentialHeaders, credentialRow(cred), cred)
				},
			}
		})
}

func newMemoryStoresEditCmd(g *globalFlags) *cobra.Command {
	return newEditCmd(g, "edit <id>", "Edit a Memory Store in your editor", 1, memoryStoreIDCompletion,
		func(c *kha.Client, args []string) *editSpec[kha.MemoryStore, kha.UpdateMemoryStoreRequest] {
			return &editSpec[kha.MemoryStore, kha.UpdateMemoryStoreRequest]{
				resource: "memory-store",
				id:       args[0],
				fetch: func(ctx context.Context) (*kha.MemoryStore, error) {
					mResult, err := c.MemoryStores.Get(ctx, kha.GetMemoryStoreRequest{ID: args[0]})
					return mResult.Body, err
				},
				update: func(ctx context.Context, req kha.UpdateMemoryStoreRequest) (*kha.MemoryStore, error) {
					mResult, err := c.MemoryStores.Update(ctx, kha.UpdateMemoryStoreHTTPRequest{ID: args[0], Body: &req})
					return mResult.Body, err
				},
				print: func(cmd *cobra.Command, g *globalFlags, m *kha.MemoryStore) error {
					note(cmd, "memory store %s updated", m.ID)
					return printOne(cmd, g, memoryStoreHeaders, memoryStoreRow(m), m)
				},
			}
		})
}

func newMemoriesEditCmd(g *globalFlags) *cobra.Command {
	return newEditCmd(g, "edit <store-id> <memory-id>", "Edit a Memory's path or content in your editor", 2, memoryIDCompletion,
		func(c *kha.Client, args []string) *editSpec[kha.Memory, kha.UpdateMemoryRequest] {
			return &editSpec[kha.Memory, kha.UpdateMemoryRequest]{
				resource:   "memory",
				id:         args[1],
				forDisplay: memoryContentForDisplay,
				forRequest: memoryContentForRequest,
				fetch: func(ctx context.Context) (*kha.Memory, error) {
					mResult, err := c.MemoryStores.GetMemory(ctx, kha.GetMemoryRequest{ID: args[0], MemoryID: args[1]})
					return mResult.Body, err
				},
				update: func(ctx context.Context, req kha.UpdateMemoryRequest) (*kha.Memory, error) {
					mResult, err := c.MemoryStores.UpdateMemory(ctx, kha.UpdateMemoryHTTPRequest{ID: args[0], MemoryID: args[1], Body: &req})
					return mResult.Body, err
				},
				print: func(cmd *cobra.Command, g *globalFlags, m *kha.Memory) error {
					note(cmd, "memory %s updated", m.ID)
					return printOne(cmd, g, memoryHeaders, memoryRow(m), m)
				},
			}
		})
}

func newMemoryStoresDreamPolicyEditCmd(g *globalFlags) *cobra.Command {
	return newEditCmd(g, "edit <store-id>", "Edit a Memory Store's Dream policy in your editor", 1, memoryStoreIDCompletion,
		func(c *kha.Client, args []string) *editSpec[kha.DreamPolicy, kha.UpdateDreamPolicyRequest] {
			return &editSpec[kha.DreamPolicy, kha.UpdateDreamPolicyRequest]{
				resource: "dream-policy",
				id:       args[0],
				fetch: func(ctx context.Context) (*kha.DreamPolicy, error) {
					pResult, err := c.MemoryStores.GetDreamPolicy(ctx, kha.GetDreamPolicyRequest{MemoryStoreID: args[0]})
					return pResult.Body, err
				},
				update: func(ctx context.Context, req kha.UpdateDreamPolicyRequest) (*kha.DreamPolicy, error) {
					pResult, err := c.MemoryStores.UpdateDreamPolicy(ctx, kha.UpdateDreamPolicyHTTPRequest{MemoryStoreID: args[0], Body: &req})
					return pResult.Body, err
				},
				print: func(cmd *cobra.Command, g *globalFlags, p *kha.DreamPolicy) error {
					note(cmd, "dream policy of %s replaced", args[0])
					return printOne(cmd, g, dreamPolicyHeaders, dreamPolicyRow(p), p)
				},
			}
		})
}

func newTriggersEditCmd(g *globalFlags) *cobra.Command {
	return newEditCmd(g, "edit <id>", "Edit a Trigger in your editor", 1, triggerIDCompletion,
		func(c *kha.Client, args []string) *editSpec[kha.Trigger, kha.UpdateTriggerRequest] {
			return &editSpec[kha.Trigger, kha.UpdateTriggerRequest]{
				resource: "trigger",
				id:       args[0],
				fetch: func(ctx context.Context) (*kha.Trigger, error) {
					tResult, err := c.Triggers.Get(ctx, kha.GetTriggerRequest{ID: args[0]})
					return tResult.Body, err
				},
				update: func(ctx context.Context, req kha.UpdateTriggerRequest) (*kha.Trigger, error) {
					tResult, err := c.Triggers.Update(ctx, kha.UpdateTriggerHTTPRequest{ID: args[0], Body: &req})
					return tResult.Body, err
				},
				print: func(cmd *cobra.Command, g *globalFlags, t *kha.Trigger) error {
					note(cmd, "trigger %s updated", t.ID)
					return printOne(cmd, g, triggerHeaders, triggerRow(t), t)
				},
			}
		})
}

func newEnvironmentsEditCmd(g *globalFlags) *cobra.Command {
	return newEditCmd(g, "edit <id>", "Edit an Environment in your editor", 1, environmentIDCompletion,
		func(c *kha.Client, args []string) *editSpec[kha.Environment, kha.UpdateEnvironmentRequest] {
			return &editSpec[kha.Environment, kha.UpdateEnvironmentRequest]{
				resource: "environment",
				id:       args[0],
				fetch: func(ctx context.Context) (*kha.Environment, error) {
					hResult, err := c.Environments.Get(ctx, kha.GetEnvironmentRequest{ID: args[0]})
					return hResult.Body, err
				},
				update: func(ctx context.Context, req kha.UpdateEnvironmentRequest) (*kha.Environment, error) {
					hResult, err := c.Environments.Update(ctx, kha.UpdateEnvironmentHTTPRequest{ID: args[0], Body: &req})
					return hResult.Body, err
				},
				print: func(cmd *cobra.Command, g *globalFlags, e *kha.Environment) error {
					note(cmd, "environment %s updated", e.ID)
					return printOne(cmd, g, environmentHeaders, environmentRow(e), e)
				},
			}
		})
}
