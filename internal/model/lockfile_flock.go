package model

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

// LockSentinelSuffix is the suffix appended to a lock file's path to derive
// the flock sentinel — qvr.lock → qvr.lock.lock. The sentinel sits next to
// the lock file so cross-process exclusion doesn't fight LockFile.Write's
// atomic tmp+rename (which would invalidate any flock held on qvr.lock
// itself once the file inode is replaced).
const LockSentinelSuffix = ".lock"

// WithLock acquires an exclusive, blocking flock on the sentinel beside path,
// runs fn, then releases. Concurrent callers serialise — the second writer
// waits for the first to finish its read-modify-write before observing the
// lock file. This matches uv's behaviour for uv.lock and fixes the
// last-writer-wins race documented in issue #55, where parallel `qvr add`
// invocations would all report success but only the last writer's lockfile
// entry would survive.
//
// Callers should perform the entire read-modify-write inside fn:
//
//	model.WithLock(lockPath, func() error {
//	    lock, err := model.ReadLockFile(lockPath)
//	    if err != nil { return err }
//	    lock.Put(entry)
//	    return lock.Write()
//	})
func WithLock(path string, fn func() error) error {
	if path == "" {
		return fmt.Errorf("lock path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create lock dir: %w", err)
	}
	fl := flock.New(path + LockSentinelSuffix)
	if err := fl.Lock(); err != nil {
		return fmt.Errorf("acquire %s%s: %w", filepath.Base(path), LockSentinelSuffix, err)
	}
	defer func() { _ = fl.Unlock() }()
	return fn()
}
