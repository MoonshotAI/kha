package commands

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

func TestSessionsSendRejectsBlankText(t *testing.T) {
	for _, source := range []string{"literal", "file", "stdin"} {
		for _, input := range []struct{ name, text string }{
			{"empty", ""},
			{"whitespace", " \t\r\n\u3000"},
		} {
			t.Run(source+"/"+input.name, func(t *testing.T) {
				isolateEnv(t)
				args := []string{"sessions", "send", "sess_1"}
				switch source {
				case "literal":
					args = append(args, input.text)
				case "file":
					path := filepath.Join(t.TempDir(), "message.md")
					if err := os.WriteFile(path, []byte(input.text), 0600); err != nil {
						t.Fatal(err)
					}
					args = append(args, "--text-file", path)
				case "stdin":
					args = append(args, "--text-file", "-")
				}
				synctest.Test(t, func(t *testing.T) {
					var requests atomic.Int64
					srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
						requests.Add(1)
						http.Error(w, "unexpected request", http.StatusBadRequest)
					})
					args = append(args, "--base-url", srv.URL, "--api-key", "sk-test")
					_, _, err := runCmd(t, input.text, args...)
					if exitCode(err) != exitUsage || requests.Load() != 0 {
						t.Fatalf("err=%v requests=%d; want usage error before API access", err, requests.Load())
					}
					if !strings.Contains(err.Error(), "Continue.") {
						t.Fatalf("error does not explain how to continue: %v", err)
					}
				})
			})
		}
	}
}
