package kha

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"os"
	"runtime/debug"
	"strings"
	"time"
)

// DefaultAPIVersion is sent as the Kimi-Api-Version header unless overridden.
const (
	DefaultAPIVersion = "2026-09-01-beta"
	defaultBaseURL    = "https://api.moonshot.cn"
	apiKeyEnv         = "KIMI_API_KEY"
)

// WithBaseURL points the client at a different API root.
func WithBaseURL(baseURL string) ClientOption {
	return func(cfg *clientConfig) {
		if baseURL != "" {
			cfg.BaseURL = baseURL
		}
	}
}

// WithAnonymous clears authentication, including New's KIMI_API_KEY fallback.
// Subsequent authentication options can install a new credential hook.
func WithAnonymous() ClientOption { return WithAuthFunc(nil) }

// WithAPIVersion overrides the Kimi-Api-Version request header.
func WithAPIVersion(version string) ClientOption {
	if version == "" {
		version = DefaultAPIVersion
	}
	return func(cfg *clientConfig) {
		if cfg.Header == nil {
			cfg.Header = http.Header{}
		}
		cfg.Header.Set("Kimi-Api-Version", version)
	}
}

// WithUserAgent overrides the User-Agent request header.
func WithUserAgent(value string) ClientOption {
	if value == "" {
		value = defaultUserAgent()
	}
	return func(cfg *clientConfig) {
		if cfg.Header == nil {
			cfg.Header = http.Header{}
		}
		cfg.Header.Set("User-Agent", value)
	}
}

// New returns the generated Client with Hosted Agents defaults. An empty key
// uses KIMI_API_KEY. It supplies authentication, API-version headers, and product
// retry policy. GET and HEAD may retry except WorkItems.Poll; explicitly
// protected JSON writes use one idempotency key per call and, after a transient
// failure, keep backing off on in-flight 409 conflicts until the stored response
// can be replayed. Upload bodies are never replayed. WithMaxRetries sets the
// product retry budget (default three).
// Use NewClient for the generated client's protocol defaults instead.
func New(apiKey string, opts ...ClientOption) (*Client, error) {
	if apiKey == "" {
		apiKey = os.Getenv(apiKeyEnv)
	}
	cfg := clientConfig{BaseURL: defaultBaseURL, MaxRetries: 3}
	WithAPIVersion(DefaultAPIVersion)(&cfg)
	WithUserAgent(defaultUserAgent())(&cfg)
	if apiKey != "" {
		WithBearerToken(apiKey)(&cfg)
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	parsed, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("kha: invalid base URL: %w", err)
	}
	hc := &http.Client{}
	if cfg.HTTPClient != nil {
		*hc = *cfg.HTTPClient
	}
	transport := hc.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	hc.Transport = &productTransport{
		base:       transport,
		maxRetries: max(cfg.MaxRetries, 0),
		basePath:   strings.TrimRight(parsed.JoinPath().EscapedPath(), "/"),
	}
	cfg.HTTPClient = hc
	// The product transport owns retries, including the Poll exclusion.
	cfg.MaxRetries = 0
	return NewClient(cfg.BaseURL, func(target *clientConfig) { *target = cfg })
}

// NewPollClient returns a Client for the work queue's Poll with retries
// disabled: one Poll call issues exactly one HTTP request, so a lost
// response cannot trigger a blind retry that claims another item. baseURL
// may be empty for the production API root. secret is the environment work
// key and must be given explicitly — an empty secret is an error rather
// than falling back to KIMI_API_KEY.
func NewPollClient(baseURL, secret string) (*Client, error) {
	if secret == "" {
		return nil, errors.New("kha: worker credential is empty")
	}
	return New(secret, WithBaseURL(baseURL), WithMaxRetries(0))
}

// Wait cadence and bounds, shared by the polled resource waiters: the whole
// wait is a context deadline (default 10 minutes) returning ctx's error on
// expiry, and the waiters fetch on a fixed interval (default 2 seconds —
// first fetch immediately, no jitter, no backoff; Retry-After may extend an
// interval).
const (
	defaultWaitTimeout  = 10 * time.Minute
	defaultWaitInterval = 2 * time.Second
)

// WaitOption customizes VaultsClient.WaitOAuthSession,
// TriggersClient.WaitForTerminal, and SessionsClient.SendMessageAndWait.
type WaitOption func(*waitConfig)

type waitConfig struct {
	timeout  time.Duration
	interval time.Duration
}

// WithTimeout bounds the whole wait (default 10 minutes), counted from the
// first poll or, for SendMessageAndWait, when the message is accepted.
// The timeout is a context deadline: on expiry the returned
// error is context.DeadlineExceeded, and cancelling the caller's ctx always
// ends the wait immediately. Non-positive values fall back to the default.
func WithTimeout(d time.Duration) WaitOption {
	return func(c *waitConfig) { c.timeout = d }
}

// WithInterval sets the cadence between polls (default 2 seconds); the
// first poll always runs immediately. Non-positive values fall back to the
// default. SendMessageAndWait is event-driven and ignores this option.
func WithInterval(d time.Duration) WaitOption {
	return func(c *waitConfig) { c.interval = d }
}

// newWaitConfig applies opts over the defaults.
func newWaitConfig(opts []WaitOption) waitConfig {
	cfg := waitConfig{timeout: defaultWaitTimeout, interval: defaultWaitInterval}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.timeout <= 0 {
		cfg.timeout = defaultWaitTimeout
	}
	if cfg.interval <= 0 {
		cfg.interval = defaultWaitInterval
	}
	return cfg
}

// WaitFailedError reports a Vault OAuth flow that reached a terminal failure
// state. It is a domain outcome, not an HTTP error, and carries no status code.
type WaitFailedError struct {
	// Resource names the waited-on resource kind (e.g. "vault oauth
	// session").
	Resource string `json:"resource"`
	// ID identifies the failed resource.
	ID string `json:"id"`
	// Status is the terminal status observed (e.g. "failed"), an open
	// vocabulary kept verbatim.
	Status string `json:"status"`
	// Reason is the server-provided failure description when the resource
	// model carries one (a Vault OAuth session's error); empty otherwise.
	Reason string `json:"reason,omitempty"`
}

// Error implements error.
func (e *WaitFailedError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "kha: %s %s reached terminal status %q", e.Resource, e.ID, e.Status)
	if e.Reason != "" {
		fmt.Fprintf(&b, ": %s", e.Reason)
	}
	return b.String()
}

// sleepCtx waits for d or until ctx is done. False means ctx fired.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// poll drives one resource waiter: fetch the resource, test it for a final
// state, sleep one interval, repeat. terminal reports (done, err) for a
// snapshot: done ends the wait, and a non-nil err is returned together with
// the snapshot that produced it (a terminal failure state is data too).
//
// Fetch errors are classified: 4xx errors other than 408 and 429 return
// immediately; 408, 429, 5xx and network errors are transient and keep the
// wait going, still bounded by the timeout. On a fetch error or an expired
// wait the returned snapshot is the zero value; the timeout and ctx
// cancellation surface as ctx's error. Retry-After may extend the poll
// interval.
func poll[T any](ctx context.Context, cfg waitConfig, fetch func(context.Context) (T, error), terminal func(T) (bool, error)) (T, error) {
	var zero T
	wctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	for {
		delay := cfg.interval
		v, err := fetch(wctx)
		if err != nil {
			if wctx.Err() != nil {
				return zero, wctx.Err()
			}
			if IsProtocolError(err) {
				return zero, err // deterministic caller/contract violation: retrying cannot help
			}
			if status, header, ok := httpErrorMetadata(err); ok {
				if status >= 400 && status < 500 && status != 408 && status != 429 {
					return zero, err
				}
				delay = max(delay, retryAfter(header.Get("Retry-After")))
			}
			// Transient failures keep polling within the same deadline.
		} else if done, err := terminal(v); done || err != nil {
			return v, err
		}
		if !sleepCtx(wctx, delay) {
			return zero, wctx.Err()
		}
	}
}

// LoadSecretFile reads a secret file and trims surrounding whitespace. A
// path of "-" reads from r (the caller's stdin); r is unused otherwise.
// Error messages name the path, never the contents.
func LoadSecretFile(r io.Reader, path string) (string, error) {
	var raw []byte
	var err error
	if path == "-" {
		raw, err = io.ReadAll(r)
	} else {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		name := path
		if path == "-" {
			name = "stdin"
		}
		return "", fmt.Errorf("read secret file %q: %w", name, err)
	}
	secret := strings.TrimSpace(string(raw))
	if secret == "" {
		return "", errors.New("secret file is empty")
	}
	return secret, nil
}

// sdkModulePath is the SDK's public module path, used to read its version
// from the calling binary's build information.
const sdkModulePath = "github.com/MoonshotAI/kha"

// defaultUserAgent returns the User-Agent sent when WithUserAgent is not
// set: "kha-go/<sdk version>" from the binary's build information, plain
// "kha-go" when no usable version is recorded (development builds).
func defaultUserAgent() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "kha-go"
	}
	if v, ok := sdkVersionFromBuildInfo(info); ok {
		return "kha-go/" + v
	}
	return "kha-go"
}

// sdkVersionFromBuildInfo extracts the SDK module version: the dependency
// version for binaries linking the SDK, or the main-module version for
// binaries built from within the module. The second result is false when no
// usable version is recorded — development builds stamp "(devel)".
func sdkVersionFromBuildInfo(info *debug.BuildInfo) (string, bool) {
	if info.Main.Path == sdkModulePath || strings.HasPrefix(info.Main.Path, sdkModulePath+"/") {
		if v := trimModuleVersion(info.Main.Version); v != "" {
			return v, true
		}
	}
	for _, dep := range info.Deps {
		if dep.Path == sdkModulePath {
			if v := trimModuleVersion(dep.Version); v != "" {
				return v, true
			}
		}
	}
	return "", false
}

// trimModuleVersion normalizes a build-info module version for the
// User-Agent: it drops the leading "v" and rejects the "(devel)" placeholder.
func trimModuleVersion(v string) string {
	if v == "(devel)" {
		return ""
	}
	return strings.TrimPrefix(v, "v")
}

func paginate[T any](ctx context.Context, initial *string, fetch func(context.Context, *string) ([]*T, *string, error)) iter.Seq2[*T, error] {
	return func(yield func(*T, error) bool) {
		token := initial
		seen := make(map[string]bool)
		if token != nil {
			seen[*token] = true
		}
		for {
			if err := ctx.Err(); err != nil {
				yield(nil, err)
				return
			}
			items, next, err := fetch(ctx, token)
			if err != nil {
				yield(nil, err)
				return
			}
			for _, item := range items {
				if item != nil && !yield(item, nil) {
					return
				}
			}
			if next == nil || *next == "" {
				return
			}
			if seen[*next] {
				yield(nil, &InvalidCallError{Reason: "pagination token did not advance"})
				return
			}
			seen[*next] = true
			token = next
		}
	}
}
