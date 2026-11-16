package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	jsonv2 "encoding/json/v2"

	kha "github.com/MoonshotAI/kha"
	"github.com/MoonshotAI/kha/internal/creds"
	"github.com/MoonshotAI/kha/internal/tools"
)

// PollConfig configures a Poller.
type PollConfig struct {
	BaseURL string
	// PollClient must use an environment work key (ewk-) and disable automatic
	// retries (kha.NewPollClient): blind retries can claim work twice.
	PollClient    *kha.Client
	EnvironmentID string
	WorkerID      string
	// Empty OnWork selects in-process execution and requires Executor.
	OnWork   string
	Executor tools.Executor
	Workdir  string
	// LeaseTTLMs also controls reclaim after a post-Ack spawn failure or crash.
	// Zero durations and reclaim windows use platform defaults.
	// BlockMs ranges from 0 to 999.
	BlockMs            int
	LeaseTTLMs         int
	ReclaimOlderThanMs int
	// QueueMaxIdle limits time without a claim; zero disables it.
	// SessionMaxIdle limits Session inactivity during in-process execution.
	QueueMaxIdle   time.Duration
	SessionMaxIdle time.Duration
	PollInterval   time.Duration
	ToolTimeout    time.Duration
	SendWindow     time.Duration
	Logger         *slog.Logger
	// Nil streams inherit the process streams. worker run writes only structured
	// data to stdout.
	Stdout io.Writer
	Stderr io.Writer

	backoffInitial time.Duration
	backoffMax     time.Duration
}

// Poller consumes an environment work queue one item at a time.
type Poller struct {
	cfg PollConfig
	log *slog.Logger
}

// NewPoller validates cfg, including launcher and executor requirements.
func NewPoller(cfg PollConfig) (*Poller, error) {
	if cfg.PollClient == nil {
		return nil, errors.New("kha worker: poll client is required")
	}
	if cfg.EnvironmentID == "" {
		return nil, errors.New("kha worker: environment id is required")
	}
	if cfg.WorkerID == "" {
		return nil, errors.New("kha worker: worker id is required")
	}
	if cfg.OnWork != "" {
		if err := CheckLauncher(cfg.OnWork); err != nil {
			return nil, err
		}
	} else if cfg.Executor == nil {
		return nil, errors.New("kha worker: in-process mode requires a tool executor")
	}
	if cfg.BlockMs < 0 || cfg.BlockMs > 999 {
		return nil, fmt.Errorf("kha worker: block ms %d out of range 0..999", cfg.BlockMs)
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	log = log.With("environment_id", cfg.EnvironmentID, "worker_id", cfg.WorkerID)
	if cfg.backoffInitial <= 0 {
		cfg.backoffInitial = pollBackoffInitial
	}
	if cfg.backoffMax <= 0 {
		cfg.backoffMax = pollBackoffMax
	}
	return &Poller{cfg: cfg, log: log}, nil
}

// Run exits on queue idle timeout, invalid environment credentials, or ctx cancellation.
// Cancellation stops new Polls and abandons current work without Stop; the sweeper
// reclaims the lease.
func (p *Poller) Run(ctx context.Context) error {
	delay := p.cfg.backoffInitial
	lastClaim := time.Now()
	for ctx.Err() == nil {
		if p.cfg.QueueMaxIdle > 0 && time.Since(lastClaim) > p.cfg.QueueMaxIdle {
			p.log.InfoContext(ctx, "no work claimed within max-idle, worker exits", "queue_max_idle", p.cfg.QueueMaxIdle)
			return nil
		}
		result, err := p.cfg.PollClient.WorkItems.Poll(ctx, p.pollParams())
		if err != nil {
			if isLeaseLost(err) {
				return fmt.Errorf("kha worker: poll rejected: %w", ErrLeaseLost)
			}
			if !retryable(err) {
				return fmt.Errorf("kha worker: poll failed: %w", err)
			}
			p.log.WarnContext(ctx, "work poll failed", "error", err, "retry_in", delay)
			if err := wait(ctx, jitter(delay)); err != nil {
				return err
			}
			delay = min(delay*2, p.cfg.backoffMax)
			continue
		}
		if result.Body == nil {
			if err := wait(ctx, jitter(delay)); err != nil {
				return err
			}
			delay = min(delay*2, p.cfg.backoffMax)
			continue
		}
		item := result.Body
		delay = p.cfg.backoffInitial
		if err := p.serve(ctx, item); err != nil {
			return err
		}
		// Claim idleness starts when work execution finishes: execution time does not
		// count toward max-idle, or the poller would wrongly exit after any work item
		// longer than max-idle.
		lastClaim = time.Now()
	}
	return ctx.Err()
}

func (p *Poller) pollParams() kha.PollWorkItemsRequest {
	params := kha.PollWorkItemsRequest{EnvironmentID: p.cfg.EnvironmentID, WorkerID: p.cfg.WorkerID}
	if p.cfg.BlockMs > 0 {
		params.BlockMs = new(int32(p.cfg.BlockMs))
	}
	if p.cfg.LeaseTTLMs > 0 {
		params.LeaseTTLMs = new(int32(p.cfg.LeaseTTLMs))
	}
	if p.cfg.ReclaimOlderThanMs > 0 {
		params.ReclaimOlderThanMs = new(int32(p.cfg.ReclaimOlderThanMs))
	}
	return params
}

// Ack immediately after delivery and before starting the executor. Unacknowledged
// deliveries are reclaimed after the fixed 5s Ack window.
func (p *Poller) serve(ctx context.Context, item *kha.WorkItem) error {
	ref := WorkRef{
		EnvironmentID: item.EnvironmentID,
		WorkID:        item.ID,
		SessionID:     item.SessionID,
		WorkerID:      p.cfg.WorkerID,
		Generation:    item.Generation,
		LeaseTTL:      time.Duration(item.LeaseTTLMs) * time.Millisecond,
	}
	log := p.log.With(ref.logAttrs()...)
	if p.cfg.OnWork != "" {
		if err := CheckLauncher(p.cfg.OnWork); err != nil {
			return err
		}
	}
	swt, err := decodeSessionsToken(item.Secret)
	if err != nil {
		log.WarnContext(ctx, "invalid delivery secret, dropping delivery", "error", err)
		return nil
	}
	client, err := creds.NewClient(p.cfg.BaseURL, swt)
	if err != nil {
		return fmt.Errorf("kha worker: build session client: %w", err)
	}
	if _, err := client.WorkItems.Ack(ctx, kha.AckWorkItemHTTPRequest{EnvironmentID: p.cfg.EnvironmentID, ID: item.ID, Body: &kha.AckWorkItemRequest{
		WorkerID:   p.cfg.WorkerID,
		Generation: item.Generation,
	}}); err != nil {
		log.WarnContext(ctx, "ack failed, dropping delivery", "error", err)
		return nil
	}
	if p.cfg.OnWork != "" {
		return p.dispatchScript(ctx, log, item, swt)
	}
	err = RunOneWork(ctx, RunConfig{
		Client:       client,
		Executor:     p.cfg.Executor,
		Workdir:      p.cfg.Workdir,
		PollInterval: p.cfg.PollInterval,
		ToolTimeout:  p.cfg.ToolTimeout,
		SendWindow:   p.cfg.SendWindow,
		MaxIdle:      p.cfg.SessionMaxIdle,
		Logger:       p.log,
	}, ref)
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return ctx.Err()
	default:
		log.WarnContext(ctx, "work abandoned", "error", err)
		return nil
	}
}

// CheckLauncher resolves bare names through PATH and requires explicit paths to
// be executable files.
func CheckLauncher(script string) error {
	if !strings.ContainsRune(script, os.PathSeparator) {
		if _, err := exec.LookPath(script); err != nil {
			return fmt.Errorf("kha worker: on-work launcher %q not found in PATH: %w", script, err)
		}
		return nil
	}
	info, err := os.Stat(script)
	if err != nil {
		return fmt.Errorf("kha worker: on-work launcher %q: %w", script, err)
	}
	if info.IsDir() {
		return fmt.Errorf("kha worker: on-work launcher %q is a directory", script)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("kha worker: on-work launcher %q is not executable", script)
	}
	return nil
}

// dispatchScript keeps the Session work token out of argv and the environment,
// using a user-only temporary file removed after the child exits. The launcher is
// synchronous. A non-zero exit continues polling; a spawn failure leaves the
// Acked work for lease-timeout reclaim.
func (p *Poller) dispatchScript(ctx context.Context, log *slog.Logger, item *kha.WorkItem, swt string) error {
	sanitized := *item
	sanitized.Secret = nil
	workJSON, err := jsonv2.Marshal(sanitized)
	if err != nil {
		return fmt.Errorf("kha worker: marshal work item: %w", err)
	}
	secretFile, err := os.CreateTemp("", "kha-work-secret-*")
	if err != nil {
		return fmt.Errorf("kha worker: create work secret file: %w", err)
	}
	secretPath := secretFile.Name()
	defer func() { _ = os.Remove(secretPath) }()
	if err := secretFile.Chmod(0o600); err != nil {
		_ = secretFile.Close()
		return fmt.Errorf("kha worker: protect work secret file: %w", err)
	}
	if _, err := secretFile.WriteString(swt); err != nil {
		_ = secretFile.Close()
		return fmt.Errorf("kha worker: write work secret file: %w", err)
	}
	if err := secretFile.Close(); err != nil {
		return fmt.Errorf("kha worker: close work secret file: %w", err)
	}

	// On ctx cancellation (SIGTERM), kill the child without proactively submitting
	// Stop; the sweeper reclaims the lease, matching in-process shutdown discipline.
	cmd := exec.CommandContext(ctx, p.cfg.OnWork)
	cmd.Stdin = bytes.NewReader(workJSON)
	cmd.Env = append(os.Environ(),
		"KHA_BASE_URL="+p.cfg.BaseURL,
		"KHA_WORK_ID="+item.ID,
		"KHA_ENVIRONMENT_ID="+item.EnvironmentID,
		"KHA_SESSION_ID="+item.SessionID,
		"KHA_WORK_GENERATION="+strconv.FormatInt(item.Generation, 10),
		"KHA_WORK_LEASE_TTL_MS="+strconv.FormatInt(item.LeaseTTLMs, 10),
		"KHA_WORKER_ID="+p.cfg.WorkerID,
		"KHA_WORK_SECRET_FILE="+secretPath,
	)
	stdout, stderr := p.cfg.Stdout, p.cfg.Stderr
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	log.InfoContext(ctx, "dispatching work to on-work script", "script", p.cfg.OnWork)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			// Non-zero exit: log and continue; unsettled work is reclaimed on lease timeout.
			log.ErrorContext(ctx, "on-work script failed", "exit_code", exitErr.ExitCode())
			return nil
		}
		return fmt.Errorf("kha worker: start on-work script %q: %w", p.cfg.OnWork, err)
	}
	return nil
}
