package commands

import (
	"encoding/base64"
	"fmt"
	"io"
	"time"

	"github.com/MoonshotAI/kha"

	"github.com/spf13/cobra"

	"github.com/MoonshotAI/kha/internal/cli/output"
)

var memoryHeaders = []string{"ID", "PATH", "SIZE", "UPDATED"}

func memoryRow(m *kha.Memory) []string {
	return []string{m.ID, m.Path, output.Bytes(m.ContentSizeBytes), output.Time(m.UpdatedAt)}
}

var memoryVersionHeaders = []string{"ID", "OPERATION", "REVISION", "SOURCE", "CREATED", "REDACTED"}

func memoryVersionRow(v *kha.MemoryVersion) []string {
	revision := "-"
	if v.Revision != nil {
		revision = fmt.Sprint(*v.Revision)
	}
	return []string{v.ID, string(v.Operation), revision, string(v.Source), output.Time(v.CreatedAt), output.Time(valueOr(v.RedactedAt, time.Time{}))}
}

// contentFlags are the memory-content input flags shared by memories
// create/update. The API carries content base64-encoded; the flags take raw
// bytes and encode them. --content-stdin is an alias for --content-file -.
type contentFlags struct {
	text         *textFlags
	contentStdin bool
}

func addContentFlags(cmd *cobra.Command) *contentFlags {
	c := &contentFlags{text: addTextFlags(cmd, "content", "file content (the CLI base64-encodes it)")}
	cmd.Flags().BoolVar(&c.contentStdin, "content-stdin", false, "read the raw file content from stdin (alias for --content-file -)")
	cmd.MarkFlagsMutuallyExclusive("content", "content-file", "content-stdin")
	return c
}

// resolve returns the base64-encoded content, or nil when no content flag
// was given.
func (c *contentFlags) resolve(cmd *cobra.Command) (*string, error) {
	var raw []byte
	switch {
	case c.contentStdin:
		data, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return nil, err
		}
		raw = data
	default:
		text, err := c.text.resolve(cmd)
		if err != nil || text == nil {
			return nil, err
		}
		raw = []byte(*text)
	}
	encoded := base64.StdEncoding.EncodeToString(raw)
	return &encoded, nil
}

func newMemoriesCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "memories",
		Short: "Manage a Memory Store's files and their versions",
	}
	cmd.AddCommand(
		newMemoriesListCmd(g),
		newMemoriesCreateCmd(g),
		newMemoriesGetCmd(g),
		newMemoriesUpdateCmd(g),
		newMemoriesEditCmd(g),
		newMemoriesDeleteCmd(g),
		newMemoryVersionsCmd(g),
	)
	return cmd
}

func newMemoriesListCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "list <store-id>",
		Short:             "List a store's current files (content omitted)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: memoryStoreIDCompletion,
	}
	lf := addListFlags(cmd)
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		if err := lf.validate(); err != nil {
			return err
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		params := kha.ListMemoriesRequest{ID: args[0], PageSize: lf.pageSize(), PageToken: lf.token()}
		if lf.all {
			return printAll(cmd, g, memoryHeaders, client.MemoryStores.AllMemories(cmd.Context(), params), memoryRow)
		}
		itemsResult, err := client.MemoryStores.ListMemories(cmd.Context(), params)
		if err != nil {
			return err
		}
		page := itemsResult.Body
		page.Items = nonNilItems(page.Items)
		page.NextPageToken = nonEmptyPageToken(page.NextPageToken)

		rows := make([][]string, 0, len(page.Items))
		for _, m := range page.Items {
			rows = append(rows, memoryRow(m))
		}
		return printList(cmd, g, memoryHeaders, rows, page)
	})
	return cmd
}

func newMemoriesCreateCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "create <store-id>",
		Short:             "Create a file in a Memory Store",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: memoryStoreIDCompletion,
		Example: `  kha memory-stores memories create mstr_xxx --path notes/todo.md --content "buy milk"
  gzip -c dump.sql | kha memory-stores memories create mstr_xxx --path dumps/dump.sql.gz --content-file -`,
	}
	var path string
	cmd.Flags().StringVar(&path, "path", "", "relative POSIX path of the file inside the store")
	content := addContentFlags(cmd)
	body := addBodyFlag(cmd, "path", "content", "content-file", "content-stdin")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		var req kha.CreateMemoryRequest
		if cmd.Flags().Changed("file") {
			if err := body.decode(cmd, &req); err != nil {
				return err
			}
		} else {
			if path == "" {
				return errUsage("memories create requires --path (or --file)")
			}
			req.Path = path
			encoded, err := content.resolve(cmd)
			if err != nil {
				return err
			}
			req.Content = encoded
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		memoryResult, err := client.MemoryStores.CreateMemory(cmd.Context(), kha.CreateMemoryHTTPRequest{ID: args[0], Body: &req})
		if err != nil {
			return err
		}
		memory := memoryResult.Body

		note(cmd, "memory %s created (%s)", memory.ID, memory.Path)
		return printOne(cmd, g, memoryHeaders, memoryRow(memory), memory)
	})
	return cmd
}

func newMemoriesGetCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "get <store-id> <memory-id>",
		Short:             "Get one Memory (content included, base64-encoded in JSON output)",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: memoryIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			memoryResult, err := client.MemoryStores.GetMemory(cmd.Context(), kha.GetMemoryRequest{ID: args[0], MemoryID: args[1]})
			if err != nil {
				return err
			}
			memory := memoryResult.Body

			return printOne(cmd, g, memoryHeaders, memoryRow(memory), memory)
		}),
	}
}

func newMemoriesUpdateCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "update <store-id> <memory-id>",
		Short:             "Replace a Memory's content, move its path, or both",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: memoryIDCompletion,
	}
	var path, sha string
	cmd.Flags().StringVar(&path, "path", "", "new path")
	cmd.Flags().StringVar(&sha, "sha256", "", "current content_sha256 for the optimistic-concurrency check")
	content := addContentFlags(cmd)
	body := addBodyFlag(cmd, "path", "sha256", "content", "content-file", "content-stdin")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		var req kha.UpdateMemoryRequest
		if cmd.Flags().Changed("file") {
			if err := body.decode(cmd, &req); err != nil {
				return err
			}
		} else {
			changed := false
			if cmd.Flags().Changed("path") {
				req.Path = new(path)
				changed = true
			}
			encoded, err := content.resolve(cmd)
			if err != nil {
				return err
			}
			if encoded != nil {
				req.Content = encoded
				changed = true
			}
			if cmd.Flags().Changed("sha256") {
				req.ContentSha256 = new(sha)
			}
			if !changed {
				return errUsage("nothing to update: pass --path, a content flag, or --file")
			}
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		memoryResult, err := client.MemoryStores.UpdateMemory(cmd.Context(), kha.UpdateMemoryHTTPRequest{ID: args[0], MemoryID: args[1], Body: &req})
		if err != nil {
			return err
		}
		memory := memoryResult.Body

		note(cmd, "memory %s updated", memory.ID)
		return printOne(cmd, g, memoryHeaders, memoryRow(memory), memory)
	})
	return cmd
}

func newMemoriesDeleteCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "delete <store-id> <memory-id>",
		Short:             "Delete a Memory (recorded as a deleted version)",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: memoryIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			if _, err := client.MemoryStores.DeleteMemory(cmd.Context(), kha.DeleteMemoryRequest{ID: args[0], MemoryID: args[1]}); err != nil {
				return err
			}
			note(cmd, "memory %s deleted", args[1])
			return nil
		}),
	}
}

func newMemoryVersionsCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "versions",
		Short: "Browse and redact a Memory's version history",
	}
	cmd.AddCommand(
		newMemoryVersionsListCmd(g),
		newMemoryVersionsGetCmd(g),
		newMemoryVersionsRedactCmd(g),
	)
	return cmd
}

func newMemoryVersionsListCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "list <store-id> <memory-id>",
		Short:             "List a Memory's versions (metadata only, oldest first)",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: memoryIDCompletion,
	}
	lf := addListFlags(cmd)
	var order string
	cmd.Flags().StringVar(&order, "order", "", "sort order: asc (default, oldest first) or desc")
	mustRegisterFlagCompletion(cmd, "order", completeFlagStatic("asc", "desc"))
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		if err := lf.validate(); err != nil {
			return err
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		params := kha.ListMemoryVersionsRequest{ID: args[0], MemoryID: args[1], PageSize: lf.pageSize(), PageToken: lf.token()}
		if order != "" {
			params.Order = new(kha.MemoryVersionOrder(order))
		}
		if lf.all {
			return printAll(cmd, g, memoryVersionHeaders, client.MemoryStores.AllMemoryVersions(cmd.Context(), params), memoryVersionRow)
		}
		itemsResult, err := client.MemoryStores.ListMemoryVersions(cmd.Context(), params)
		if err != nil {
			return err
		}
		page := itemsResult.Body
		page.Items = nonNilItems(page.Items)
		page.NextPageToken = nonEmptyPageToken(page.NextPageToken)

		rows := make([][]string, 0, len(page.Items))
		for _, v := range page.Items {
			rows = append(rows, memoryVersionRow(v))
		}
		return printList(cmd, g, memoryVersionHeaders, rows, page)
	})
	return cmd
}

func newMemoryVersionsGetCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "get <store-id> <memory-id> <version-id>",
		Short:             "Get one Memory version with its historical content",
		Args:              cobra.ExactArgs(3),
		ValidArgsFunction: memoryVersionIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			versionResult, err := client.MemoryStores.GetMemoryVersionContent(cmd.Context(), kha.GetMemoryVersionContentRequest{ID: args[0], MemoryID: args[1], VersionID: args[2]})
			if err != nil {
				return err
			}
			version := versionResult.Body

			return printOne(cmd, g, memoryVersionHeaders, memoryVersionRow(version), version)
		}),
	}
}

func newMemoryVersionsRedactCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "redact <store-id> <memory-id> <version-id>",
		Short:             "Redact one Memory version's historical content",
		Args:              cobra.ExactArgs(3),
		ValidArgsFunction: memoryVersionIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			versionResult, err := client.MemoryStores.RedactMemoryVersion(cmd.Context(), kha.RedactMemoryVersionRequest{ID: args[0], MemoryID: args[1], VersionID: args[2]})
			if err != nil {
				return err
			}
			version := versionResult.Body

			note(cmd, "memory version %s redacted", version.ID)
			return printOne(cmd, g, memoryVersionHeaders, memoryVersionRow(version), version)
		}),
	}
}
