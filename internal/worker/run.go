package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	kha "github.com/MoonshotAI/kha"
	"github.com/MoonshotAI/kha/internal/tools"
)

var errStopRequested = errors.New("work item stopping")

// WorkRef identifies a work item's lease. Heartbeat and Stop CAS operations
// validate (work_id, worker_id, generation); worker_id must match the Poll value.
type WorkRef struct {
	EnvironmentID string
	WorkID        string
	SessionID     string
	WorkerID      string
	Generation    int64
	// LeaseTTL is the lease duration returned by Poll delivery. Zero starts with
	// the platform default lease, then converges to ttl_seconds from the first
	// Heartbeat response (worker run accepts no local lease parameter).
	LeaseTTL time.Duration
}

func (ref WorkRef) logAttrs() []any {
	return []any{
		"environment_id", ref.EnvironmentID,
		"work_item_id", ref.WorkID,
		"session_id", ref.SessionID,
		"worker_id", ref.WorkerID,
		"generation", ref.Generation,
	}
}

// RunConfig configures one work item. Client must use the delivery's Session work
// token (swt-) to bind lifecycle operations to its Session and generation.
type RunConfig struct {
	Client       *kha.Client
	Executor     tools.Executor
	Workdir      string
	PollInterval time.Duration
	ToolTimeout  time.Duration
	SendWindow   time.Duration
	MaxIdle      time.Duration // 0 = do not settle on idleness
	Logger       *slog.Logger
}

// RunOneWork returns ErrLeaseLost after abandoning a 401/412 lease loss without
// overwriting the new owner. Parent cancellation (SIGTERM) returns ctx.Err()
// without Stop; the sweeper reclaims the lease. Accepted idle settlement,
// stop-request cleanup, and Session termination return nil.
func RunOneWork(ctx context.Context, cfg RunConfig, ref WorkRef) error {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	log = log.With(ref.logAttrs()...)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	runCtx, stopRunner := context.WithCancelCause(ctx)
	defer stopRunner(nil)
	r := newRunner(cfg.Client, ref.SessionID, cfg.Executor, cfg.Workdir, log,
		cfg.PollInterval, cfg.ToolTimeout, cfg.MaxIdle, cfg.SendWindow, stopRunner)
	hb := &heartbeat{client: cfg.Client, ref: ref, log: log}
	ready := make(chan struct{})
	go hb.loop(ctx, cancel, ready, stopRunner)
	// Tool execution requires the active lease established by the first heartbeat.
	select {
	case <-ready:
	case <-ctx.Done():
		if err := hb.failure(); err != nil {
			return err
		}
		return ctx.Err()
	}
	for {
		runErr := r.run(runCtx)
		stopping := errors.Is(context.Cause(runCtx), errStopRequested)
		if ctx.Err() != nil {
			if err := hb.failure(); err != nil {
				return err
			}
			return ctx.Err()
		}
		if runErr != nil && !errors.Is(runErr, errIdleTimeout) && (!stopping || !errors.Is(runErr, context.Canceled)) {
			if errors.Is(runErr, ErrLeaseLost) {
				return runErr
			}
			return fmt.Errorf("kha worker: runner failed, abandoning work item: %w", runErr)
		}
		err := settle(ctx, cfg.Client, ref)
		if errors.Is(runErr, errIdleTimeout) && isNotDormant(err) {
			continue // Session still busy: keep the lease and dedup state, and restart the idle timer.
		}
		if err != nil {
			// After successful settlement, a heartbeat may observe the terminal state.
			// Settlement failure still exits with the existing outcome; the sweeper
			// reclaims an unacknowledged work item.
			log.WarnContext(ctx, "work item settlement failed", "error", err)
		}
		if ctx.Err() != nil {
			if ferr := hb.failure(); ferr != nil {
				return ferr
			}
			return ctx.Err()
		}
		if isLeaseLost(err) {
			return fmt.Errorf("kha worker: %w", ErrLeaseLost)
		}
		return nil
	}
}

type heartbeat struct {
	client *kha.Client
	ref    WorkRef
	log    *slog.Logger

	mu  sync.Mutex
	err error
}

func (h *heartbeat) setFailure(err error) {
	h.mu.Lock()
	h.err = err
	h.mu.Unlock()
}

func (h *heartbeat) failure() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.err
}

// Record lease rejection so callers can distinguish it from parent cancellation.
func (h *heartbeat) loop(ctx context.Context, cancel context.CancelFunc, ready chan<- struct{}, stopRunner context.CancelCauseFunc) {
	defer cancel()
	interval := max(h.ref.LeaseTTL/2, minHeartbeatInterval)
	if h.ref.LeaseTTL <= 0 {
		interval = defaultLeaseTTL / 2
	}
	for ctx.Err() == nil {
		result, err := h.client.WorkItems.Heartbeat(ctx, kha.HeartbeatWorkItemHTTPRequest{EnvironmentID: h.ref.EnvironmentID, ID: h.ref.WorkID, Body: &kha.HeartbeatWorkItemRequest{
			WorkerID:   h.ref.WorkerID,
			Generation: h.ref.Generation,
		}})
		hb := result.Body
		if err != nil && !retryable(err) {
			h.log.WarnContext(ctx, "heartbeat rejected, abandoning work item", "error", err)
			if isLeaseLost(err) {
				err = ErrLeaseLost
			}
			h.setFailure(err)
			return
		}
		var delay time.Duration
		if err != nil {
			h.log.WarnContext(ctx, "heartbeat failed, quick retry", "error", err)
			delay = max(interval/8, heartbeatQuickRetry)
		} else {
			if hb.TTLSeconds > 0 {
				interval = max(time.Duration(hb.TTLSeconds)*time.Second/2, minHeartbeatInterval)
			}
			// Convergence takes effect immediately: this beat waits using the new
			// interval. Otherwise, when LeaseTTL is unknown (on-work child), the second
			// beat is delayed by the platform-default estimate.
			delay = interval
			if hb.State == kha.WorkItemStatusStopping {
				stopRunner(errStopRequested)
			}
			if ready != nil {
				close(ready)
				ready = nil
			}
		}
		if wait(ctx, delay) != nil {
			return
		}
	}
}

// settle retries Stop for 15s. A 409 means the Session is still busy, so the
// caller keeps the lease; after timeout, the sweeper reclaims it.
func settle(ctx context.Context, client *kha.Client, ref WorkRef) error {
	body := &kha.StopWorkItemRequest{
		WorkerID:    ref.WorkerID,
		Generation:  ref.Generation,
		FinalStatus: new(kha.StopWorkItemRequestFinalStatusCompleted),
	}
	return retryWithWindow(ctx, settleWindow, time.Second, 4*time.Second, func(ctx context.Context) error {
		_, err := client.WorkItems.Stop(ctx, kha.StopWorkItemHTTPRequest{EnvironmentID: ref.EnvironmentID, ID: ref.WorkID, Body: body})
		return err
	})
}
