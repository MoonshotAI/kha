package tunnel

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtaci/smux/v2"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func check[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseGatewayURL(t *testing.T) {
	for _, tc := range []struct {
		name, raw, address, serverName string
		wantErr                        bool
	}{
		{name: "origin with port", raw: "https://gateway.example:9443", address: "gateway.example:9443", serverName: "gateway.example"},
		{name: "default port", raw: "https://gateway.example", address: "gateway.example:443", serverName: "gateway.example"},
		{name: "host without scheme", raw: "gateway.example:9443", address: "gateway.example:9443", serverName: "gateway.example"},
		{name: "IPv6", raw: "https://[2001:db8::1]:9443", address: "[2001:db8::1]:9443", serverName: "2001:db8::1"},
		{name: "http rejected", raw: "http://gateway.example", wantErr: true},
		{name: "path rejected", raw: "https://gateway.example/agent", wantErr: true},
		{name: "query rejected", raw: "https://gateway.example?x=1", wantErr: true},
		{name: "missing", raw: "", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseGatewayURL(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			must(t, err)
			check(t, got.address, tc.address)
			check(t, got.serverName, tc.serverName)
		})
	}
}

func TestAgentEndToEnd(t *testing.T) {
	authority := newEchoServer(t)
	gateway := newFakeGateway(t, authority, "token-secret")
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	must(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: gateway.server.Certificate().Raw}), 0o600))
	agent, err := NewAgent(Options{
		GatewayURL: gateway.server.URL, Token: "token-secret", Sessions: 1,
		DialTimeout: time.Second, CAFile: caFile,
		Logf: func(format string, args ...any) {
			if strings.Contains(fmt.Sprintf(format, args...), "token-secret") {
				t.Error("token leaked into agent log")
			}
		},
	})
	must(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- agent.Run(ctx) }()
	select {
	case <-gateway.ready:
	case <-time.After(3 * time.Second):
		t.Fatal("agent did not establish a session")
	}
	// The gateway signals readiness as soon as it answers the registration;
	// the agent's own readiness flag lags that response.
	deadline := time.Now().Add(3 * time.Second)
	for !agent.Ready() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	check(t, agent.Ready(), true)

	conn, reader, status := dialTLSConnect(t, gateway.server, authority)
	check(t, status, http.StatusOK)
	must(t, conn.SetDeadline(time.Now().Add(2*time.Second)))
	banner := make([]byte, len("ready\n"))
	_, err = io.ReadFull(reader, banner)
	must(t, err)
	check(t, string(banner), "ready\n")
	_, err = conn.Write([]byte("payload"))
	must(t, err)
	payload := make([]byte, len("payload"))
	_, err = io.ReadFull(reader, payload)
	must(t, err)
	check(t, string(payload), "payload")
	_ = conn.Close()
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("agent returned %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("agent did not stop after cancellation")
	}
}

// TestAgentMultiplexesStreams proves one session carries several concurrent
// streams to the target network.
func TestAgentMultiplexesStreams(t *testing.T) {
	authority := newEchoServer(t)
	gateway := newFakeGateway(t, authority, "token-secret")
	agent, err := NewAgent(Options{
		GatewayURL: gateway.server.URL, Token: "token-secret", Sessions: 1,
		DialTimeout: time.Second,
		Logf:        func(string, ...any) {},
	})
	must(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(gateway.server.Certificate())
	agent.tlsConfig.RootCAs = roots
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() { _ = agent.Run(ctx) }()
	select {
	case <-gateway.ready:
	case <-time.After(3 * time.Second):
		t.Fatal("agent did not establish a session")
	}

	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Go(func() {
			conn, reader, status := dialTLSConnect(t, gateway.server, authority)
			if status != http.StatusOK {
				errs <- fmt.Errorf("status %d", status)
				return
			}
			defer func() { _ = conn.Close() }()
			if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
				errs <- err
				return
			}
			banner := make([]byte, len("ready\n"))
			if _, err := io.ReadFull(reader, banner); err != nil {
				errs <- err
				return
			}
			if _, err := conn.Write([]byte("ping")); err != nil {
				errs <- err
				return
			}
			echo := make([]byte, len("ping"))
			if _, err := io.ReadFull(reader, echo); err != nil {
				errs <- err
				return
			}
			if string(echo) != "ping" {
				errs <- fmt.Errorf("echo %q", echo)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		must(t, err)
	}
}

func TestAgentRetriesRegistration(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	agent, err := NewAgent(Options{GatewayURL: server.URL, Token: "token", Sessions: 1, DialTimeout: 100 * time.Millisecond})
	must(t, err)
	agent.tlsConfig.RootCAs = roots
	agent.retrySeed = time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- agent.Run(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for attempts.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("agent did not stop")
	}
	if attempts.Load() < 2 {
		t.Fatalf("got %d registration attempts, want at least 2", attempts.Load())
	}
}

// A 401 rejects the token itself, which token mode cannot re-mint: the agent
// exits with errTokenRejected instead of retrying a dead credential forever.
func TestAgentFatalOnRejectedToken(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	agent, err := NewAgent(Options{GatewayURL: server.URL, Token: "token", Sessions: 2, DialTimeout: 100 * time.Millisecond})
	must(t, err)
	agent.tlsConfig.RootCAs = roots
	done := make(chan error, 1)
	go func() { done <- agent.Run(t.Context()) }()
	select {
	case err := <-done:
		if !errors.Is(err, errTokenRejected) {
			t.Fatalf("agent returned %v, want errTokenRejected", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("agent kept retrying a rejected token")
	}
}

// fakeGateway mimics the real gateway's session data plane: registrations are
// hijacked into a mux session, and each later CONNECT is forwarded as one mux
// stream carrying the target authority.
type fakeGateway struct {
	server *httptest.Server
	target string
	token  string
	ready  chan struct{}
	once   sync.Once
	mu     sync.Mutex
	mux    *smux.Session
}

func newFakeGateway(t *testing.T, target, token string) *fakeGateway {
	t.Helper()
	g := &fakeGateway{target: target, token: token, ready: make(chan struct{})}
	g.server = httptest.NewTLSServer(http.HandlerFunc(g.serveHTTP))
	t.Cleanup(g.server.Close)
	return g
}

func (g *fakeGateway) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	if r.RequestURI == agentPath {
		g.serveAgent(w, r)
		return
	}
	g.serveConnect(w, r)
}

func (g *fakeGateway) serveAgent(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+g.token {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	agent := &bufferedConn{Conn: conn, reader: rw.Reader}
	if err := writeConnStatus(agent, http.StatusOK); err != nil {
		_ = conn.Close()
		return
	}
	mux, err := smux.Server(agent, muxConfig())
	if err != nil {
		_ = conn.Close()
		return
	}
	g.mu.Lock()
	g.mux = mux
	g.mu.Unlock()
	g.once.Do(func() { close(g.ready) })
}

func (g *fakeGateway) serveConnect(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	mux := g.mux
	g.mu.Unlock()
	if mux == nil {
		http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
		return
	}
	stream, err := mux.OpenStream()
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
		return
	}
	streamConn := &bufferedConn{Conn: stream, reader: bufio.NewReader(stream)}
	work := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: g.target}, Host: g.target, Header: make(http.Header)}
	if err := work.Write(streamConn); err != nil {
		_ = stream.Close()
		http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
		return
	}
	resp, err := http.ReadResponse(streamConn.reader, work)
	if err != nil || resp.StatusCode != http.StatusOK {
		_ = stream.Close()
		http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
		return
	}
	_ = resp.Body.Close()
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = stream.Close()
		return
	}
	client, rw, err := hijacker.Hijack()
	if err != nil {
		_ = stream.Close()
		return
	}
	clientConn := &bufferedConn{Conn: client, reader: rw.Reader}
	_ = writeConnStatus(clientConn, http.StatusOK)
	relay(clientConn, streamConn)
}

func dialTLSConnect(t *testing.T, server *httptest.Server, authority string) (net.Conn, *bufio.Reader, int) {
	t.Helper()
	u, err := url.Parse(server.URL)
	must(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	conn, err := tls.Dial("tcp", u.Host, &tls.Config{RootCAs: roots, ServerName: u.Hostname()})
	must(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: authority}, Host: authority, Header: make(http.Header)}
	must(t, req.Write(conn))
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	must(t, err)
	_ = resp.Body.Close()
	return conn, reader, resp.StatusCode
}

func newEchoServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_, _ = conn.Write([]byte("ready\n"))
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().String()
}

func TestAgentOptionsValidation(t *testing.T) {
	for _, opts := range []Options{
		{GatewayURL: "", Token: "token"},
		{GatewayURL: "http://gateway", Token: "token"},
		{GatewayURL: "https://gateway", Token: ""},
		{GatewayURL: "https://gateway", Token: "token", Sessions: 9},
		{GatewayURL: "https://gateway", Token: "token", DialTimeout: -time.Second},
	} {
		if _, err := NewAgent(opts); err == nil {
			t.Fatalf("expected options to fail: %+v", opts)
		}
	}
}

func TestAgentErrorDoesNotIncludeToken(t *testing.T) {
	agent, err := NewAgent(Options{GatewayURL: "https://127.0.0.1:1", Token: "very-secret-token", Sessions: 1, DialTimeout: 10 * time.Millisecond})
	must(t, err)
	var logs strings.Builder
	agent.logf = func(format string, args ...any) {
		_, _ = fmt.Fprintf(&logs, format, args...)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	_ = agent.Run(ctx)
	if logs.Len() == 0 {
		t.Fatal("expected a connection error log")
	}
	if strings.Contains(logs.String(), "very-secret-token") {
		t.Fatal("token leaked into agent log")
	}
}
