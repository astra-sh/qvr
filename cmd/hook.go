package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/ops"
	"github.com/raks097/quiver/internal/ops/store"
	"github.com/raks097/quiver/internal/privacy"
	"github.com/spf13/cobra"

	// Side-effect import: registers the generic adapter with ops.
	_ "github.com/raks097/quiver/internal/ops/adapter"
)

var hookCmd = &cobra.Command{
	Use:    "_hook <agent> <hook_type>",
	Short:  "Ingest a hook event (internal use)",
	Hidden: true,
	Args:   cobra.ExactArgs(2),
	RunE:   runHook,
}

func init() {
	rootCmd.AddCommand(hookCmd)
}

// runHook is the entry point for `qvr _hook <agent> <hook_type>`.
// It reads the hook's JSON payload from stdin and feeds it through
// the SkillOps funnel. The funnel is a silent no-op when ops is
// disabled in config — exit 0, no error, no DB created.
func runHook(cmd *cobra.Command, args []string) error {
	agent := args[0]
	hookType := args[1]

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !ops.Enabled(cfg) {
		return nil // silent no-op — the documented disabled behaviour
	}
	ops.ApplyDefaults(cfg)

	adapter, err := ops.GetAdapter(agent)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "qvr _hook: %v\n", err)
		return nil // don't fail the agent's hook pipeline; just drop
	}

	raw, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	// Empty stdin used to be treated as a silent success (return nil), which
	// made capture a no-op while every status surface still reported the hook
	// VALID. Codex CLI in `codex exec` mode is the real-world trigger: it
	// fires the hook command but does not pipe the JSON payload to its stdin,
	// so qvr is invoked for a genuine event with nothing on stdin. Don't drop
	// it. Warn on stderr (hooks discard stderr in normal operation, so this
	// only shows on a hand-run) and hand the empty payload to the adapter:
	// codex synthesises a minimal event keyed off the hook type on argv;
	// stricter adapters surface a parse error via self_audit. Either way the
	// broken trail is no longer invisible.
	if len(raw) == 0 {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"qvr _hook: %s %s received empty stdin (agent fired the hook but delivered no payload)\n",
			agent, hookType)
	}

	// Open the store; run migrations if this is the first _hook call
	// since `qvr ops enable`.
	s, err := store.Open(cmd.Context(), store.OpenOptions{Path: ops.DBPath(cfg)})
	if err != nil {
		return fmt.Errorf("open ops store: %w", err)
	}
	defer s.Close()

	// Build the resolver from global + (optional) local lockfiles.
	resolver, err := buildResolver(cfg)
	if err != nil {
		return fmt.Errorf("build resolver: %w", err)
	}

	checker, err := privacy.Default(cfg.Ops.Privacy.SensitivePaths, cfg.Ops.Privacy.RedactPatterns)
	if err != nil {
		return fmt.Errorf("build privacy checker: %w", err)
	}

	funnel, err := ops.NewFunnel(ops.FunnelDeps{
		Config:   cfg,
		Adapter:  adapter,
		Resolver: resolver,
		Privacy:  checker,
		Store:    storeSessionAdapter{s},
		// Surface drops/provisional records on stderr so a broken trail is
		// no longer silent (#137). Hooks discard stderr in normal operation,
		// so this only shows when _hook is run by hand.
		Notify: func(msg string) { fmt.Fprintf(cmd.ErrOrStderr(), "qvr _hook: %s\n", msg) },
	})
	if err != nil {
		return err
	}
	return funnel.Ingest(cmd.Context(), hookType, raw)
}

// buildResolver assembles the lockfile-backed Resolver. It always reads
// the global lockfile at $QUIVER_HOME/qvr.lock; if the process's cwd also
// has a lockfile, it's appended so local entries shadow global.
func buildResolver(cfg *config.Config) (ops.Resolver, error) {
	_ = cfg // reserved for future per-config overrides
	global := filepath.Join(config.Dir(), model.LockFileName)

	paths := []string{global}
	if cwd, err := os.Getwd(); err == nil {
		local := filepath.Join(cwd, model.LockFileName)
		if local != global {
			paths = append(paths, local)
		}
	}
	return ops.NewResolver(paths...)
}

// storeSessionAdapter bridges the concrete store.Store to the narrower
// ops.SessionStore interface the funnel consumes. It exists solely to
// break the import cycle (internal/ops can't import internal/ops/store).
// Every method is a one-liner translation.
type storeSessionAdapter struct{ inner store.Store }

func (a storeSessionAdapter) SaveEvent(ctx context.Context, e *ops.Event) error {
	return a.inner.SaveEvent(ctx, e)
}

func (a storeSessionAdapter) GetSession(ctx context.Context, id uuid.UUID) (*ops.Session, error) {
	return a.inner.GetSession(ctx, id)
}

func (a storeSessionAdapter) UpsertSession(ctx context.Context, s *ops.Session) error {
	return a.inner.UpsertSession(ctx, s)
}

func (a storeSessionAdapter) BackfillSkill(ctx context.Context, sessionID uuid.UUID, skill string) (int64, error) {
	return a.inner.BackfillSkill(ctx, sessionID, skill)
}

func (a storeSessionAdapter) DeleteSession(ctx context.Context, id uuid.UUID) (int64, error) {
	return a.inner.DeleteSession(ctx, id)
}

func (a storeSessionAdapter) DeleteSkilllessSessions(ctx context.Context, olderThan time.Time) (int64, error) {
	return a.inner.DeleteSkilllessSessions(ctx, olderThan)
}

func (a storeSessionAdapter) AppendSelfAudit(ctx context.Context, entry *ops.SelfAuditEntry) error {
	if entry == nil {
		return errors.New("nil self-audit entry")
	}
	return a.inner.AppendSelfAudit(ctx, &store.SelfAudit{
		ID:        entry.ID,
		Timestamp: entry.Timestamp,
		Action:    entry.Action,
		Actor:     entry.Actor,
		Result:    entry.Result,
		ErrorMsg:  entry.ErrorMsg,
		Details:   entry.Details,
	})
}
