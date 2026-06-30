package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// TestBuildDSN_ReadOnlyOmitsJournalMode pins the lock-contention fix: a
// read-only open must NOT assert journal_mode(wal). Re-asserting WAL needs a
// write lock, so a reader issuing it collides with a concurrent writer (e.g.
// `qvr audit discover`) and surfaces a spurious "database is locked" that then
// pollutes the derived session outcome. WAL is persistent on the file, so a
// reader inherits it without the pragma.
func TestBuildDSN_ReadOnlyOmitsJournalMode(t *testing.T) {
	ro := buildDSN(OpenOptions{Path: "/tmp/x.db", BusyTimeoutMs: 5000, ReadOnly: true})
	if strings.Contains(ro, "journal_mode") {
		t.Errorf("read-only DSN must not assert journal_mode (would take a write lock): %s", ro)
	}
	if !strings.Contains(ro, "query_only%281%29") && !strings.Contains(ro, "query_only(1)") {
		t.Errorf("read-only DSN must set query_only(1): %s", ro)
	}

	rw := buildDSN(OpenOptions{Path: "/tmp/x.db", BusyTimeoutMs: 5000})
	if !strings.Contains(rw, "journal_mode") {
		t.Errorf("writable DSN must assert journal_mode(wal): %s", rw)
	}
	if strings.Contains(rw, "query_only") {
		t.Errorf("writable DSN must not set query_only: %s", rw)
	}
	// busy_timeout is honored on both so a brief lock window still waits, not fails.
	for _, dsn := range []string{ro, rw} {
		if !strings.Contains(dsn, "busy_timeout") {
			t.Errorf("DSN missing busy_timeout: %s", dsn)
		}
	}
}

// TestReadOnly_ReadsConcurrentlyWithOpenWriter is the behavioral guard: once a
// writer has created the WAL database, a separate ReadOnly store opens and reads
// it without erroring — the reader path no longer needs the write lock. This is
// the same cross-process shape as an audited agent's `qvr audit` subprocess
// reading the store while the loop writes it.
func TestReadOnly_ReadsConcurrentlyWithOpenWriter(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "skillops.db")

	// Writer open: applies migrations + converts the file to WAL, and stays open.
	w, err := Open(ctx, OpenOptions{Path: path})
	if err != nil {
		t.Fatalf("writer open: %v", err)
	}
	defer w.Close()

	// Reader open against the same live file must succeed (and not block on the
	// writer's connection) now that it doesn't re-assert WAL.
	r, err := Open(ctx, OpenOptions{Path: path, ReadOnly: true})
	if err != nil {
		t.Fatalf("read-only open against a live WAL db: %v", err)
	}
	defer r.Close()
}
