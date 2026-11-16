package commands

import (
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/MoonshotAI/kha"
	"github.com/MoonshotAI/kha/internal/creds"
	"github.com/MoonshotAI/kha/internal/tools"
	"github.com/MoonshotAI/kha/internal/worker"
)

var newLocalExecutor = func(toolTimeout time.Duration) (tools.Executor, error) {
	return tools.NewExecutor(shellTimeoutFor(toolTimeout)), nil
}

// Give the shell four-fifths of the tool timeout (minimum 1s) so it times out
// first with a deterministic shell error instead of racing the tool timeout.
func shellTimeoutFor(toolTimeout time.Duration) time.Duration {
	if d := toolTimeout * 4 / 5; d > 0 {
		return d
	}
	return time.Second
}

// Worker commands require machine credentials (ewk-/swt-), never the global
// API key or profile.
func newWorkerCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Run self-hosted environment workers (poll the work queue or execute one work item)",
	}
	cmd.AddCommand(newWorkerLoginCmd(g), newWorkerLogoutCmd(g), newWorkerPollCmd(g), newWorkerRunCmd(g))
	return cmd
}

type workerExecFlags struct {
	workdir      string
	maxIdle      time.Duration
	pollInterval time.Duration
	toolTimeout  time.Duration
	sendWindow   time.Duration
}

func (f *workerExecFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.workdir, "workdir", "", "working directory for tool execution (default: process working directory)")
	cmd.Flags().DurationVar(&f.pollInterval, "poll-interval", time.Second, "session event poll interval")
	cmd.Flags().DurationVar(&f.toolTimeout, "tool-timeout", 5*time.Minute, "per-tool execution timeout; the shell tool's own timeout is 4/5 of it (shell errors first); should be ≤ platform loop tool_timeout")
	cmd.Flags().DurationVar(&f.sendWindow, "send-window", 5*time.Minute, "retry window for tool-result writes")
}

func workerBaseURL(g *globalFlags) string {
	if g.baseURL != "" {
		return g.baseURL
	}
	if endpoint := os.Getenv("KHA_BASE_URL"); endpoint != "" {
		return endpoint
	}
	return defaultBaseURL
}

// workerLogFormat registers --log-format on a worker command and builds the
// command's logger: text keeps the default human-readable handler; json
// emits one structured line per record for log collectors. Logs always go
// to stderr — stdout stays reserved for data.
func workerLogFormat(cmd *cobra.Command, format *string) {
	cmd.Flags().StringVar(format, "log-format", "text", "log format: text or json (json emits structured records for collectors)")
	mustRegisterFlagCompletion(cmd, "log-format", completeFlagStatic("text", "json"))
}

func workerLogger(cmd *cobra.Command, format string) (*slog.Logger, error) {
	switch format {
	case "text":
		return slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(cmd.ErrOrStderr(), nil)), nil
	default:
		return nil, errUsage("invalid --log-format %q: want text or json", format)
	}
}

func newWorkerPollCmd(g *globalFlags) *cobra.Command {
	var exec workerExecFlags
	var workerID, onWork, logFormat string
	var token tokenFlags
	var block, leaseTTL, reclaimOlderThan, queueMaxIdle time.Duration
	cmd := &cobra.Command{
		Use:   "poll <environment-id>",
		Short: "Claim work items from the environment work queue and execute them",
		Long: `Claim work items from the environment work queue and execute them, one at a time.

Each successful poll is acknowledged immediately with the poll's worker
identity, then dispatched: without --on-work the work runs in this process;
with --on-work the script runs synchronously in the foreground (typically
kha worker run) and receives the sanitized work item on stdin, the work
identity in KHA_* environment variables, and the session work token in a
0600 temporary file named by KHA_WORK_SECRET_FILE.

Use a work key saved by worker login, or override it with --token-file or
--token-stdin. Worker commands do not use API-key profiles.
For a single claim without ack or execution, use environments work poll.

Without --on-work, --max-idle limits only time without a claim (default 0:
no limit), while --session-max-idle controls Session idle settlement
(default 1m). Accepted idle settlement completes the work and polling
continues. Queue idle never interrupts claimed work. With --on-work,
configure Session idle with worker run --max-idle in the script.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: environmentIDCompletion,
	}
	token.register(cmd, "the environment work key (ewk-)")
	cmd.Flags().StringVar(&workerID, "worker-id", worker.NewWorkerID(), "lease identity; default unique per process start")
	cmd.Flags().StringVar(&onWork, "on-work", "", "script dispatched synchronously per work item (default: execute in-process)")
	cmd.Flags().DurationVar(&block, "block", 999*time.Millisecond, "long-poll budget (at most 999ms)")
	cmd.Flags().DurationVar(&leaseTTL, "lease-ttl", 0, "requested work-item lease TTL (default: platform 300s); also bounds recovery of spawn failures")
	cmd.Flags().DurationVar(&reclaimOlderThan, "reclaim-older-than", 0, "reclaim window for stale deliveries (default: platform 5s)")
	exec.register(cmd)
	workerLogFormat(cmd, &logFormat)
	cmd.Flags().DurationVar(&queueMaxIdle, "max-idle", 0, "exit after this long without claiming work; 0 polls forever")
	cmd.Flags().DurationVar(&exec.maxIdle, "session-max-idle", time.Minute, "session quiet limit before idle settlement for in-process work; 0 disables idle settlement")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		environmentID := args[0]
		if environmentID == "" {
			return errUsage("environment ID must not be empty")
		}
		if block < 0 || block > 999*time.Millisecond {
			return errUsage("--block must be within 0..999ms")
		}
		if workerID == "" {
			return errUsage("--worker-id must not be empty")
		}
		if onWork != "" {
			if err := worker.CheckLauncher(onWork); err != nil {
				return errUsage("%s", err)
			}
		}
		var workKey string
		var err error
		if token.explicit(cmd) {
			workKey, err = token.load(cmd, "ewk-")
		} else {
			workKey, err = loadWorkerKey(workerBaseURL(g), environmentID)
		}
		if err != nil {
			return err
		}
		// Do not retain a lease if local tools cannot be built; --on-work executes
		// tools in the dispatched process instead.
		var executor tools.Executor
		if onWork == "" {
			executor, err = newLocalExecutor(exec.toolTimeout)
			if err != nil {
				return err
			}
		}
		baseURL := workerBaseURL(g)
		pollClient, err := kha.NewPollClient(baseURL, workKey)
		if err != nil {
			return err
		}
		logger, err := workerLogger(cmd, logFormat)
		if err != nil {
			return err
		}
		poller, err := worker.NewPoller(worker.PollConfig{
			Logger:             logger,
			BaseURL:            baseURL,
			PollClient:         pollClient,
			EnvironmentID:      environmentID,
			WorkerID:           workerID,
			OnWork:             onWork,
			Executor:           executor,
			Workdir:            exec.workdir,
			BlockMs:            int(block.Milliseconds()),
			LeaseTTLMs:         int(leaseTTL.Milliseconds()),
			ReclaimOlderThanMs: int(reclaimOlderThan.Milliseconds()),
			QueueMaxIdle:       queueMaxIdle,
			SessionMaxIdle:     exec.maxIdle,
			PollInterval:       exec.pollInterval,
			ToolTimeout:        exec.toolTimeout,
			SendWindow:         exec.sendWindow,
			Stdout:             cmd.OutOrStdout(),
			Stderr:             cmd.ErrOrStderr(),
		})
		if err != nil {
			return err
		}
		return poller.Run(cmd.Context())
	})
	return cmd
}

func newWorkerRunCmd(g *globalFlags) *cobra.Command {
	var exec workerExecFlags
	var environmentID, workID, sessionID, workerID, logFormat string
	var token tokenFlags
	var generation int64
	var leaseTTL time.Duration
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Execute one claimed work item's full lease lifecycle",
		Long: `Execute one claimed work item: first heartbeat activation, session
event consumption, local tool execution, single user.tool_result writes,
and the final stop settlement.

The caller (kha worker poll or an external dispatcher) acknowledges the
delivery before spawning this command; run owns heartbeats and stop. The
identity defaults to KHA_ENVIRONMENT_ID, KHA_WORK_ID, KHA_SESSION_ID,
KHA_WORKER_ID and KHA_WORK_GENERATION, and the claimed item's lease TTL
to KHA_WORK_LEASE_TTL_MS. Explicit flags override them.
The session token file defaults to KHA_WORK_SECRET_FILE; --token-file or
--token-stdin overrides it. No secret is read from the keychain or profile.
An on-work script can simply exec kha worker run. Stdin JSON is not parsed.`,
		Args: cobra.NoArgs,
	}
	cmd.Flags().StringVar(&environmentID, "environment-id", "", "environment of the work item (default: KHA_ENVIRONMENT_ID)")
	cmd.Flags().StringVar(&workID, "work-id", "", "work item ID (default: KHA_WORK_ID)")
	cmd.Flags().StringVar(&sessionID, "session-id", "", "session the work item executes (default: KHA_SESSION_ID)")
	cmd.Flags().StringVar(&workerID, "worker-id", "", "lease identity (default: KHA_WORKER_ID); must match poll")
	cmd.Flags().Int64Var(&generation, "generation", 0, "positive fencing token (default: KHA_WORK_GENERATION)")
	cmd.Flags().DurationVar(&leaseTTL, "lease-ttl", 0, "claimed work item's lease TTL (default: KHA_WORK_LEASE_TTL_MS; zero: assume the platform default)")
	token.register(cmd, "the Session work token (swt-)")
	exec.register(cmd)
	workerLogFormat(cmd, &logFormat)
	cmd.Flags().DurationVar(&exec.maxIdle, "max-idle", time.Minute, "session quiet limit before idle settlement; 0 disables idle settlement")
	cmd.RunE = run(func(cmd *cobra.Command, _ []string) error {
		for _, input := range []struct {
			flag, env string
			value     *string
		}{
			{"environment-id", "KHA_ENVIRONMENT_ID", &environmentID},
			{"work-id", "KHA_WORK_ID", &workID},
			{"session-id", "KHA_SESSION_ID", &sessionID},
			{"worker-id", "KHA_WORKER_ID", &workerID},
		} {
			if !cmd.Flags().Changed(input.flag) {
				*input.value = os.Getenv(input.env)
			}
			if *input.value == "" {
				return errUsage("provide --%s or %s (must not be empty)", input.flag, input.env)
			}
		}
		if !cmd.Flags().Changed("generation") {
			var err error
			generation, err = strconv.ParseInt(os.Getenv("KHA_WORK_GENERATION"), 10, 64)
			if err != nil {
				return errUsage("provide --generation or KHA_WORK_GENERATION as a positive decimal integer")
			}
		}
		if generation <= 0 {
			return errUsage("--generation or KHA_WORK_GENERATION must be a positive decimal integer")
		}
		if !cmd.Flags().Changed("lease-ttl") {
			if raw := os.Getenv("KHA_WORK_LEASE_TTL_MS"); raw != "" {
				ms, err := strconv.ParseInt(raw, 10, 64)
				if err != nil || ms < 0 {
					return errUsage("KHA_WORK_LEASE_TTL_MS must be a non-negative decimal integer of milliseconds (0: platform default)")
				}
				leaseTTL = time.Duration(ms) * time.Millisecond
			}
		}
		if !token.explicit(cmd) {
			token.file = os.Getenv("KHA_WORK_SECRET_FILE")
			if token.file == "" {
				return errUsage("provide --token-file, --token-stdin or KHA_WORK_SECRET_FILE")
			}
		}
		secret, err := token.load(cmd, "swt-")
		if err != nil {
			return err
		}
		// Remove only the poller-provided file, never an unrelated user file
		// or a path named "-" when the caller selected stdin.
		if p := os.Getenv("KHA_WORK_SECRET_FILE"); !token.stdin && token.file != "-" && p != "" && p == token.file {
			_ = os.Remove(token.file)
		}
		executor, err := newLocalExecutor(exec.toolTimeout)
		if err != nil {
			return err
		}
		client, err := creds.NewClient(workerBaseURL(g), secret)
		if err != nil {
			return err
		}
		logger, err := workerLogger(cmd, logFormat)
		if err != nil {
			return err
		}
		return worker.RunOneWork(cmd.Context(), worker.RunConfig{
			Client:       client,
			Executor:     executor,
			Logger:       logger,
			Workdir:      exec.workdir,
			PollInterval: exec.pollInterval,
			ToolTimeout:  exec.toolTimeout,
			SendWindow:   exec.sendWindow,
			MaxIdle:      exec.maxIdle,
		}, worker.WorkRef{
			EnvironmentID: environmentID,
			WorkID:        workID,
			SessionID:     sessionID,
			WorkerID:      workerID,
			Generation:    generation,
			LeaseTTL:      leaseTTL,
		})
	})
	return cmd
}
