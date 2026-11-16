package commands

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/MoonshotAI/kha"

	"github.com/spf13/cobra"

	"github.com/MoonshotAI/kha/internal/cli/output"
)

var fileHeaders = []string{"ID", "FILENAME", "SIZE", "PURPOSE", "STATUS", "CREATED"}

func fileRow(f *kha.FileMetadata) []string {
	purpose := "-"
	if f.Purpose != nil {
		purpose = string(*f.Purpose)
	}
	return []string{string(f.ID), f.Filename, output.Bytes(f.SizeBytes), purpose, string(f.ExtractStatus), output.Time(f.CreatedAt)}
}

func newFilesCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "files",
		Short: "Upload, browse, download, and delete user files",
	}
	cmd.AddCommand(
		newFilesListCmd(g),
		newFilesGetCmd(g),
		newFilesDeleteCmd(g),
		newFilesUploadCmd(g),
		newFilesDownloadCmd(g),
	)
	return cmd
}

func newFilesListCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List uploaded files (newest first)",
		Args:  cobra.NoArgs,
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
		params := kha.ListFilesRequest{PageSize: lf.pageSize(), PageToken: lf.token()}
		if lf.all {
			return printAll(cmd, g, fileHeaders, client.Files.All(cmd.Context(), params), fileRow)
		}
		itemsResult, err := client.Files.List(cmd.Context(), params)
		if err != nil {
			return err
		}
		page := itemsResult.Body
		page.Items = nonNilItems(page.Items)
		page.NextPageToken = nonEmptyPageToken(page.NextPageToken)

		rows := make([][]string, 0, len(page.Items))
		for _, f := range page.Items {
			rows = append(rows, fileRow(f))
		}
		return printList(cmd, g, fileHeaders, rows, page)
	})
	return cmd
}

func newFilesGetCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "get <id>",
		Short:             "Get one file's metadata",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: fileIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			fileResult, err := client.Files.Get(cmd.Context(), kha.GetFileRequest{ID: args[0]})
			if err != nil {
				return err
			}
			file := fileResult.Body

			return printOne(cmd, g, fileHeaders, fileRow(file), file)
		}),
	}
}

func newFilesDeleteCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "delete <id>",
		Short:             "Delete a file",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: fileIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			if _, err := client.Files.Delete(cmd.Context(), kha.DeleteFileRequest{ID: args[0]}); err != nil {
				return err
			}
			note(cmd, "file %s deleted", args[0])
			return nil
		}),
	}
}

func newFilesUploadCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "upload",
		Short:   "Upload a file",
		Args:    cobra.NoArgs,
		Example: `  kha files upload --file report.pdf --purpose file-extract`,
	}
	var path, purpose, filename string
	cmd.Flags().StringVarP(&path, "file", "f", "", "path of the local file to upload (streamed from disk — large files are not loaded into memory)")
	cmd.Flags().StringVar(&filename, "filename", "", "remote file name (default: local basename)")
	cmd.Flags().StringVarP(&purpose, "purpose", "p", "", "how the file is processed: file-extract, image, video, batch (server default when omitted)")
	mustRegisterFlagCompletion(cmd, "purpose", completeFlagStatic("file-extract", "image", "video", "batch"))
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		if path == "" {
			return errUsage("files upload requires --file")
		}
		if cmd.Flags().Changed("filename") && filename == "" {
			return errUsage("--filename must not be empty")
		}
		if filename == "" {
			filename = filepath.Base(path)
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		fi, err := f.Stat()
		if err != nil {
			return err
		}
		var purposePtr *kha.FilePurpose
		if purpose != "" {
			purposePtr = new(kha.FilePurpose(purpose))
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		note(cmd, "uploading %s (%s)...", filepath.Base(path), output.Bytes(fi.Size()))
		fileResult, err := client.Files.Upload(cmd.Context(), kha.UploadFileRequest{Body: &kha.UploadFileBody{File: kha.File{Filename: filename, Body: f}, Purpose: purposePtr}})
		if err != nil {
			return err
		}
		file := fileResult.Body

		note(cmd, "file %s uploaded", file.ID)
		return printOne(cmd, g, fileHeaders, fileRow(file), file)
	})
	return cmd
}

func newFilesDownloadCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "download <id>",
		Short:             "Download a file's content",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: fileIDCompletion,
	}
	var outPath string
	var force bool
	// --out has no -o shorthand: the global -o is --output, and a local
	// shorthand would collide with it at flag-merge time.
	cmd.Flags().StringVar(&outPath, "out", "", "destination path (default: the file's name in the current directory)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite the destination file if it already exists")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		dest := outPath
		if dest == "" {
			fileResult, err := client.Files.Get(cmd.Context(), kha.GetFileRequest{ID: args[0]})
			if err != nil {
				return err
			}
			file := fileResult.Body

			// The server-provided name is untrusted: strip any directory
			// components so a hostile name cannot escape the current
			// directory.
			dest = filepath.Base(file.Filename)
		}
		rcResult, err := client.Files.GetContent(cmd.Context(), kha.GetFileContentRequest{ID: args[0]})
		if err != nil {
			return err
		}
		rc := rcResult.Body

		defer func() { _ = rc.Close() }()
		n, err := writeToFile(dest, rc, force)
		if err != nil {
			return err
		}
		note(cmd, "file %s downloaded to %s (%s)", args[0], dest, output.Bytes(n))
		return nil
	})
	return cmd
}

// writeToFile streams r into a file at path, returning the byte count. An
// existing destination is never silently overwritten: without force the
// open is exclusive and fails with a --force hint.
func writeToFile(path string, r io.Reader, force bool) (int64, error) {
	if path == "" {
		return 0, fmt.Errorf("empty destination path")
	}
	var f *os.File
	var err error
	if force {
		f, err = os.CreateTemp(filepath.Dir(path), ".kha-download-*")
	} else {
		f, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	}
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return 0, fmt.Errorf("%s already exists (pass --force to overwrite)", path)
		}
		return 0, err
	}
	if force {
		defer func() { _ = os.Remove(f.Name()) }()
	}
	n, err := io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		// Don't leave a partial file behind: a failed download that leaves
		// a truncated body on disk looks like a completed one.
		_ = os.Remove(f.Name())
		return 0, fmt.Errorf("writing %s: %w", path, err)
	}
	if force {
		if err := os.Rename(f.Name(), path); err != nil {
			return 0, fmt.Errorf("replacing %s: %w", path, err)
		}
	}
	return n, nil
}
