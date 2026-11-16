package commands

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
)

// TestFilesUploadFilename verifies multipart filenames, including multibyte UTF-8.
func TestFilesUploadFilename(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		want  string
		exit  int
	}{
		{"default", nil, "local.bin", 0},
		{"override", []string{"--filename", "分析 report.pdf"}, "分析 report.pdf", 0},
		{"empty", []string{"--filename="}, "", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateEnv(t)
			path := filepath.Join(t.TempDir(), "local.bin")
			content := []byte("file bytes\x00\xff\n")
			if err := os.WriteFile(path, content, 0600); err != nil {
				t.Fatal(err)
			}
			synctest.Test(t, func(t *testing.T) {
				requests := make(chan struct{}, 1)
				srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
					requests <- struct{}{}
					if r.Method != http.MethodPost || r.URL.Path != "/v1/files" {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					}
					reader, err := r.MultipartReader()
					if err != nil {
						t.Error(err)
						return
					}
					var filename, purpose string
					var got []byte
					for {
						part, err := reader.NextPart()
						if err == io.EOF {
							break
						}
						if err != nil {
							t.Error(err)
							return
						}
						data, err := io.ReadAll(part)
						if err != nil {
							t.Error(err)
							return
						}
						switch part.FormName() {
						case "file":
							filename, got = part.FileName(), data
						case "purpose":
							purpose = string(data)
						}
					}
					if filename != tc.want || purpose != "file-extract" || !bytes.Equal(got, content) {
						t.Errorf("multipart filename=%q purpose=%q content=%q", filename, purpose, got)
					}
					w.WriteHeader(http.StatusCreated)
					_, _ = fmt.Fprint(w, `{"id":"file_1","filename":"result.pdf"}`)
				})
				args := []string{"files", "upload", "--file", path, "--purpose", "file-extract", "--base-url", srv.URL, "--api-key", "sk-test"}
				args = append(args, tc.flags...)
				_, _, err := runCmd(t, "", args...)
				if code := exitCode(err); code != tc.exit {
					t.Fatalf("exit=%d err=%v", code, err)
				}
				if tc.exit != 0 && len(requests) != 0 {
					t.Fatal("invalid filename sent a request")
				}
			})
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, content) {
				t.Fatalf("local file changed: %q, %v", got, err)
			}
		})
	}
}
