package model_test

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/raks097/quiver/internal/model"
)

// TestWithLock_SerialisesConcurrentReadModifyWrite is the regression test for
// issue #55. Without flock, N concurrent goroutines doing read-modify-write on
// the same qvr.lock would last-writer-win and only one entry would survive.
// Under WithLock, all N entries must land.
func TestWithLock_SerialisesConcurrentReadModifyWrite(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "qvr.lock")

	const N = 8
	var wg sync.WaitGroup
	wg.Add(N)
	errs := make(chan error, N)

	for i := range N {
		go func() {
			defer wg.Done()
			err := model.WithLock(lockPath, func() error {
				lock, err := model.ReadLockFile(lockPath)
				if err != nil {
					return fmt.Errorf("read: %w", err)
				}
				name := fmt.Sprintf("skill-%02d", i)
				lock.Put(&model.LockEntry{
					Name:     name,
					Registry: "test",
					Ref:      "main",
				})
				return lock.Write()
			})
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("WithLock returned error: %v", err)
	}

	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("final read: %v", err)
	}
	if got := len(lock.Skills); got != N {
		names := make([]string, 0, len(lock.Skills))
		for n := range lock.Skills {
			names = append(names, n)
		}
		t.Fatalf("expected %d entries after concurrent adds, got %d: %v", N, got, names)
	}
}

// TestWithLock_PropagatesClosureError ensures fn's error survives the unlock.
func TestWithLock_PropagatesClosureError(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "qvr.lock")

	want := fmt.Errorf("boom")
	got := model.WithLock(lockPath, func() error { return want })
	if got != want {
		t.Fatalf("expected error %v, got %v", want, got)
	}
}

// TestWithLock_CreatesParentDir ensures the lock dir doesn't need to exist
// upfront — DefaultLockPath under a fresh global location should work.
func TestWithLock_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "nested", "deeper", "qvr.lock")
	called := false
	if err := model.WithLock(lockPath, func() error { called = true; return nil }); err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	if !called {
		t.Fatal("closure was not invoked")
	}
}
