// Package tunnel implements the customer-side tunnel agent used by kha.
// agentPath and the wire helpers (validAuthority, writeConnStatus, relay,
// bufferedConn) mirror the gateway's protocol surface: kha is a separate
// public module and cannot import the contract package, and drift here
// breaks every connected agent.
package tunnel

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtaci/smux/v2"

	"github.com/MoonshotAI/kha/internal/cli/output"
)

const (
	// agentPath is the gateway's agent registration CONNECT target. It is the
	// wire contract and must match the gateway; kha is a separate public
	// module and cannot import the contract package.
	agentPath       = "/v1/agent"
	defaultSessions = 2
	maxSessions     = 8
	initialBackoff  = 500 * time.Millisecond
	maxBackoff      = 30 * time.Second
	reconnectGap    = 100 * time.Millisecond
)

// Options configures an Agent. Token is kept in memory and never logged.
type Options struct {
	GatewayURL  string
	Token       string
	Sessions    int
	DialTimeout time.Duration
	CAFile      string
	Logf        func(format string, args ...any)
}

// Agent maintains independent mux sessions to the tunnel gateway.
type Agent struct {
	address     string
	token       string
	sessions    int
	dialTimeout time.Duration
	tlsConfig   *tls.Config
	logf        func(format string, args ...any)
	retrySeed   time.Duration
	ready       atomic.Int32
}

// NewAgent validates options and loads an optional private CA bundle.
func NewAgent(opts Options) (*Agent, error) {
	endpoint, err := parseGatewayURL(opts.GatewayURL)
	if err != nil {
		return nil, err
	}
	if opts.Token == "" {
		return nil, errors.New("a tunnel token is required")
	}
	sessions := opts.Sessions
	if sessions == 0 {
		sessions = defaultSessions
	}
	if sessions < 1 || sessions > maxSessions {
		return nil, fmt.Errorf("sessions must be between 1 and %d", maxSessions)
	}
	timeout := opts.DialTimeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	if timeout < 0 {
		return nil, errors.New("dial timeout must be positive")
	}
	rootCAs, err := loadRootCAs(opts.CAFile)
	if err != nil {
		return nil, err
	}
	logf := opts.Logf
	if logf == nil {
		logf = func(format string, args ...any) {
			_, _ = fmt.Fprintln(os.Stderr, output.StripEscapes(fmt.Sprintf(format, args...)))
		}
	}
	return &Agent{
		address:     endpoint.address,
		token:       opts.Token,
		sessions:    sessions,
		dialTimeout: timeout,
		tlsConfig: &tls.Config{
			RootCAs:    rootCAs,
			ServerName: endpoint.serverName,
			MinVersion: tls.VersionTLS12,
			NextProtos: []string{"http/1.1"},
		},
		logf:      logf,
		retrySeed: initialBackoff,
	}, nil
}

type gatewayEndpoint struct {
	address    string
	serverName string
}

func parseGatewayURL(raw string) (gatewayEndpoint, error) {
	if raw == "" {
		return gatewayEndpoint{}, errors.New("gateway URL is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return gatewayEndpoint{}, errors.New("gateway URL must be an https origin without a path")
	}
	hostname := u.Hostname()
	if hostname == "" {
		return gatewayEndpoint{}, errors.New("gateway URL must include a host")
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return gatewayEndpoint{}, errors.New("gateway URL has an invalid port")
	}
	return gatewayEndpoint{address: net.JoinHostPort(hostname, port), serverName: hostname}, nil
}

func loadRootCAs(path string) (*x509.CertPool, error) {
	if path == "" {
		return nil, nil
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("load system roots: %w", err)
	}
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read CA file: %w", err)
	}
	if !roots.AppendCertsFromPEM(pem) {
		return nil, errors.New("CA file contains no certificates")
	}
	return roots, nil
}

// muxConfig must match the gateway: the receive window is what bounds
// per-stream buffering under flow control.
func muxConfig() *smux.Config {
	cfg := smux.DefaultConfig()
	cfg.MaxReceiveBuffer = 1 << 20
	return cfg
}

// Ready reports whether at least one session is registered.
func (a *Agent) Ready() bool { return a.ready.Load() > 0 }

// Run maintains the configured mux sessions until ctx is canceled or a
// session hits an unrecoverable error, which is returned.
func (a *Agent) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var wg sync.WaitGroup
	for range a.sessions {
		wg.Go(func() { a.sessionWorker(ctx, cancel) })
	}
	wg.Wait()
	if err := context.Cause(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return ctx.Err()
}

func (a *Agent) sessionWorker(ctx context.Context, cancel context.CancelCauseFunc) {
	backoff := a.retrySeed
	for ctx.Err() == nil {
		registered, err := a.runSession(ctx)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errTokenRejected) {
			cancel(err)
			return
		}
		if err != nil {
			a.logf("tunnel session ended: %v", err)
		}
		delay := backoff
		if registered {
			delay = reconnectGap
			backoff = a.retrySeed
		} else {
			backoff = min(backoff*2, maxBackoff)
		}
		if !sleepContext(ctx, jitter(delay)) {
			return
		}
	}
}

// jitter spreads reconnects after a shared gateway or network failure.
func jitter(delay time.Duration) time.Duration {
	return delay + time.Duration(rand.Float64()*float64(delay)/4)
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// errTokenRejected reports a 401 from the gateway: the token itself was
// rejected (expired or revoked). Token mode cannot re-mint, so retrying
// would spin on a dead credential forever; the agent exits instead.
var errTokenRejected = errors.New("tunnel: gateway rejected the agent token (401), re-issue a token")

// runSession registers one mux session and serves its streams until the
// session ends. The bool reports whether registration succeeded, which
// selects the reconnect backoff: session interruptions reconnect quickly,
// registration failures back off.
func (a *Agent) runSession(ctx context.Context) (bool, error) {
	dialer := &net.Dialer{Timeout: a.dialTimeout, KeepAlive: 30 * time.Second}
	tlsDialer := &tls.Dialer{NetDialer: dialer, Config: a.tlsConfig}
	conn, err := tlsDialer.DialContext(ctx, "tcp", a.address)
	if err != nil {
		return false, fmt.Errorf("dial gateway: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(a.dialTimeout))
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: agentPath}, Host: a.address, Header: http.Header{"Authorization": {"Bearer " + a.token}}}
	if err := req.Write(conn); err != nil {
		return false, fmt.Errorf("register agent: %w", err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return false, fmt.Errorf("read registration response: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized {
			return false, errTokenRejected
		}
		// Other rejections stay retryable: a disabled tunnel (403) may be
		// re-enabled, and 429/5xx are capacity or transient faults.
		return false, fmt.Errorf("gateway rejected registration with status %d", resp.StatusCode)
	}
	a.ready.Add(1)
	defer a.ready.Add(-1)
	_ = conn.SetDeadline(time.Time{})

	mux, err := smux.Client(&bufferedConn{Conn: conn, reader: reader}, muxConfig())
	if err != nil {
		return true, fmt.Errorf("start mux session: %w", err)
	}
	defer func() { _ = mux.Close() }()
	for {
		stream, err := mux.AcceptStream()
		if err != nil {
			return true, fmt.Errorf("mux session ended: %w", err)
		}
		go a.serveStream(ctx, stream)
	}
}

// serveStream relays one mux stream to its target inside the customer
// network.
func (a *Agent) serveStream(ctx context.Context, stream *smux.Stream) {
	defer func() { _ = stream.Close() }()
	_ = stream.SetDeadline(time.Now().Add(a.dialTimeout))
	reader := bufio.NewReader(stream)
	work, err := http.ReadRequest(reader)
	if err != nil {
		return
	}
	authority := work.Host
	if work.Method != http.MethodConnect || !validAuthority(authority) {
		_ = writeConnStatus(stream, http.StatusBadRequest)
		return
	}
	dialer := &net.Dialer{Timeout: a.dialTimeout, KeepAlive: 30 * time.Second}
	target, err := dialer.DialContext(ctx, "tcp", authority)
	if err != nil {
		_ = writeConnStatus(stream, http.StatusBadGateway)
		a.logf("tunnel stream to %s failed: %v", authority, err)
		return
	}
	defer func() { _ = target.Close() }()
	if err := writeConnStatus(stream, http.StatusOK); err != nil {
		return
	}
	_ = stream.SetDeadline(time.Time{})
	_ = target.SetDeadline(time.Time{})
	agent := &bufferedConn{Conn: stream, reader: reader}
	target = &bufferedConn{Conn: target, reader: bufio.NewReader(target)}
	relay(agent, target)
}

func validAuthority(authority string) bool {
	host, port, err := net.SplitHostPort(authority)
	if err != nil || host == "" || strings.ContainsAny(host, " \t\r\n/?#@") {
		return false
	}
	number, err := strconv.Atoi(port)
	return err == nil && number > 0 && number <= 65535
}

func writeConnStatus(conn net.Conn, status int) error {
	_, err := fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\n\r\n", status, http.StatusText(status))
	return err
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func (c *bufferedConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Close()
}

func closeWrite(conn net.Conn) {
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = conn.Close()
}

func relay(client, target net.Conn) {
	var wg sync.WaitGroup
	wg.Go(func() {
		_, _ = io.Copy(target, client)
		closeWrite(target)
	})
	_, _ = io.Copy(client, target)
	closeWrite(client)
	wg.Wait()
}
