package commands

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/MoonshotAI/kha/internal/cli/output"
	"github.com/MoonshotAI/kha/internal/cli/tunnel"
)

func newTunnelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tunnel",
		Short: "Run customer network tunnel agents",
	}
	cmd.AddCommand(newTunnelRunCmd())
	return cmd
}

func newTunnelRunCmd() *cobra.Command {
	var opts struct {
		gateway    string
		token      string
		tokenStdin bool
		sessions   int
		timeout    time.Duration
		caFile     string
		healthAddr string
	}
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Maintain tunnel gateway sessions",
		Long: `Maintain outbound TLS mux sessions to a tunnel gateway.
Each session authenticates with the single-tunnel agent token and multiplexes
concurrent TCP streams with per-stream flow control. Sessions are reconnected
automatically after network failures. The token mode does not contact the
public REST API and does not renew the token; issue a new token before it
expires.

Use --token-stdin instead of --token to keep the token out of shell history and
the process list. --ca-file is only for a private gateway CA; public CA
certificates need no extra flag.`,
		Example: `  kha tunnel run --gateway https://tunnel-gw.example.com --token-stdin
  kha tunnel run --gateway https://tunnel-gw.example.com --token "$TUNNEL_TOKEN" --sessions 4 --health-addr 127.0.0.1:8080`,
		Args: cobra.NoArgs,
		RunE: run(func(cmd *cobra.Command, _ []string) error {
			if opts.tokenStdin {
				token, err := readSecret(cmd)
				if err != nil {
					return err
				}
				opts.token = token
			}
			if opts.sessions < 1 || opts.sessions > 8 {
				return errUsage("--sessions must be between 1 and 8")
			}
			if opts.timeout <= 0 {
				return errUsage("--timeout must be positive")
			}
			agent, err := tunnel.NewAgent(tunnel.Options{
				GatewayURL: opts.gateway, Token: opts.token, Sessions: opts.sessions,
				DialTimeout: opts.timeout, CAFile: opts.caFile,
				Logf: func(format string, args ...any) { note(cmd, format, args...) },
			})
			if err != nil {
				return err
			}
			shutdownHealth, _, err := startTunnelHealth(cmd.Context(), opts.healthAddr, agent, cmd)
			if err != nil {
				return err
			}
			defer shutdownHealth()
			note(cmd, "tunnel agent starting with %d sessions", opts.sessions)
			if err := agent.Run(cmd.Context()); err != nil {
				if cmd.Context().Err() != nil {
					return errInterrupted
				}
				return err
			}
			return nil
		}),
	}
	flags := cmd.Flags()
	flags.StringVar(&opts.gateway, "gateway", "", "gateway agent-port URL (https://host[:443], required)")
	flags.StringVar(&opts.token, "token", "", "single-tunnel agent token (prefer --token-stdin)")
	flags.BoolVar(&opts.tokenStdin, "token-stdin", false, "read the agent token from stdin")
	flags.IntVar(&opts.sessions, "sessions", 2, "number of independent mux sessions (1-8); the gateway's default per-tunnel limit is 4 — registrations beyond it are rejected")
	flags.DurationVar(&opts.timeout, "timeout", 10*time.Second, "gateway and target dial timeout")
	flags.StringVar(&opts.caFile, "ca-file", "", "PEM CA bundle for a private gateway certificate")
	flags.StringVar(&opts.healthAddr, "health-addr", "", "local health listener address (empty disables it)")
	cmd.MarkFlagsMutuallyExclusive("token", "token-stdin")
	cmd.MarkFlagsOneRequired("token", "token-stdin")
	_ = cmd.MarkFlagRequired("gateway")
	return cmd
}

func startTunnelHealth(ctx context.Context, addr string, agent *tunnel.Agent, cmd *cobra.Command) (func(), net.Addr, error) {
	if addr == "" {
		return func() {}, nil, nil
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("start health listener: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if agent.Ready() {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	note(cmd, "health listener started on %s", output.StripEscapes(listener.Addr().String()))
	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}, listener.Addr(), nil
}
