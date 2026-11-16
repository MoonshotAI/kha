package commands

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/MoonshotAI/kha/internal/cli/tunnel"
)

func TestTunnelRunValidation(t *testing.T) {
	isolateEnv(t)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "missing gateway", args: []string{"tunnel", "run", "--token", "token"}, want: `required flag(s) "gateway" not set`},
		{name: "missing token", args: []string{"tunnel", "run", "--gateway", "https://gateway"}, want: "at least one of the flags in the group [token token-stdin] is required"},
		{name: "token input conflict", args: []string{"tunnel", "run", "--gateway", "https://gateway", "--token", "token", "--token-stdin"}, want: "none of the others can be"},
		{name: "sessions too small", args: []string{"tunnel", "run", "--gateway", "https://gateway", "--token", "token", "--sessions", "0"}, want: "--sessions"},
		{name: "sessions too large", args: []string{"tunnel", "run", "--gateway", "https://gateway", "--token", "token", "--sessions", "9"}, want: "--sessions"},
		{name: "invalid timeout", args: []string{"tunnel", "run", "--gateway", "https://gateway", "--token", "token", "--timeout", "0s"}, want: "--timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runCmd(t, "", tc.args...)
			if err == nil {
				t.Fatal("expected an error")
			}
			if code := exitCode(err); code != exitUsage {
				t.Fatalf("exitCode = %d, want %d: %v", code, exitUsage, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestTunnelRunHelp(t *testing.T) {
	isolateEnv(t)
	stdout, _, err := runCmd(t, "", "tunnel", "run", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--token-stdin", "--sessions", "--health-addr", "does not renew the token"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help missing %q:\n%s", want, stdout)
		}
	}
}

func TestTunnelHealthEndpoints(t *testing.T) {
	isolateEnv(t)
	agent, err := tunnel.NewAgent(tunnel.Options{GatewayURL: "https://gateway", Token: "token"})
	if err != nil {
		t.Fatal(err)
	}
	root := NewRootCommand()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	shutdown, addr, err := startTunnelHealth(t.Context(), "127.0.0.1:0", agent, root)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdown()
	base := "http://" + addr.String()
	resp, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz returned %d", resp.StatusCode)
	}
	resp, err = http.Get(base + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("/readyz returned %d before registration", resp.StatusCode)
	}
}
