package commands

import (
	"io"

	"github.com/MoonshotAI/kha"

	"github.com/spf13/cobra"

	"github.com/MoonshotAI/kha/internal/cli/output"
)

var sessionFSHeaders = []string{"PATH", "SIZE", "TYPE"}

// sessionFSRow renders one filesystem node; directories carry no size.
func sessionFSRow(n *kha.SessionFSNode) []string {
	if n.IsDir {
		return []string{n.Path, "-", "dir"}
	}
	return []string{n.Path, output.Bytes(n.SizeBytes), "file"}
}

func newSessionsFsCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fs",
		Short: "Browse and download files in a Session's filesystem",
	}
	cmd.AddCommand(
		newSessionsFsListCmd(g),
		newSessionsFsDownloadCmd(g),
	)
	return cmd
}

func newSessionsFsListCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "list <session-id>",
		Short:             "List one directory of a Session's filesystem (not paginated, not recursive)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: sessionIDCompletion,
	}
	var prefix string
	cmd.Flags().StringVar(&prefix, "prefix", "", "directory to list, relative to the Session file root (default: the root; uploaded user files live under upload/)")
	mustRegisterFlagCompletion(cmd, "prefix", sessionFSDirFlagCompletion)
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		params := kha.ListSessionFilesystemRequest{ID: args[0]}
		if prefix != "" {
			params.Prefix = new(prefix)
		}
		listResult, err := client.Sessions.ListFilesystem(cmd.Context(), params)
		if err != nil {
			return err
		}
		list := listResult.Body
		list.Items = nonNilItems(list.Items)

		rows := make([][]string, 0, len(list.Items))
		for i := range list.Items {
			rows = append(rows, sessionFSRow(list.Items[i]))
		}
		return printList(cmd, g, sessionFSHeaders, rows, list)
	})
	return cmd
}

// newSessionsFsDownloadCmd streams one Session file's bytes to stdout, or to
// a file with --out (mirroring skills content / files download).
func newSessionsFsDownloadCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "download <session-id> <path>",
		Short:             "Download one file from a Session's filesystem (stdout or --out file)",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: sessionFSPathCompletion,
		Example: `  kha sessions fs download sess_xxx output/report.md --out report.md
  kha sessions fs download sess_xxx output/data.csv | head`,
	}
	var outPath string
	// --out has no -o shorthand: the global -o is --output, and a local
	// shorthand would collide with it at flag-merge time.
	cmd.Flags().StringVar(&outPath, "out", "", "destination file (default: the file bytes go to stdout)")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		bodyResult, err := client.Sessions.DownloadFilesystemFile(cmd.Context(), kha.DownloadSessionFilesystemFileRequest{ID: args[0], Path: args[1]})
		if err != nil {
			return err
		}
		body := bodyResult.Body

		defer func() { _ = body.Close() }()
		if outPath == "" {
			_, err := io.Copy(cmd.OutOrStdout(), body)
			return err
		}
		// --out is an explicitly user-chosen path: overwrite it (curl -o
		// semantics); the --force guard exists for server-chosen names.
		n, err := writeToFile(outPath, body, true)
		if err != nil {
			return err
		}
		note(cmd, "session %s file %s downloaded to %s (%s)", args[0], args[1], outPath, output.Bytes(n))
		return nil
	})
	return cmd
}
