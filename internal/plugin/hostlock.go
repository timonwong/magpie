package plugin

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/yetone/magpie/internal/filelock"
	"github.com/yetone/magpie/internal/settings"
)

func hostLockPath() string { return filepath.Join(settings.Dir(), "plugin-host.lock") }

// heldHost is the open lock file HoldHost took, and the path it took it
// at; it stays open, so locked, until this process ends.
var heldHost struct {
	sync.Mutex
	f    *os.File
	path string
}

// HoldHost takes, for as long as this magpie runs, the lock that says it
// serves the gateway and runs the plugins of this folder; false when
// another magpie holds it, or there is no lock to be had (a file system
// without locks), and this one then goes on as it would without it. It is
// a flock (LockFileEx on Windows), which the system lets go when its
// holder ends however it ends, so a magpie killed, or a Mac shut down
// with it running, leaves nothing behind that looks held.
func HoldHost() bool {
	heldHost.Lock()
	defer heldHost.Unlock()
	path := hostLockPath()
	if heldHost.f != nil {
		return heldHost.path == path
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false
	}
	if got, err := filelock.TryLock(f); err != nil || !got {
		f.Close()
		return false
	}
	heldHost.f, heldHost.path = f, path
	return true
}

// HostHeldElsewhere is whether another magpie holds the lock HoldHost
// takes for this folder. Only a lock surely held counts: no file (no
// magpie has served this folder), one that can't be opened, or a file
// system without locks is unknown, and the plugins are asked as before.
// A lock this takes is let go at once, so the magpie serving next gets it.
func HostHeldElsewhere() bool {
	heldHost.Lock()
	defer heldHost.Unlock()
	path := hostLockPath()
	if heldHost.f != nil && heldHost.path == path {
		return false
	}
	// not made when it isn't there: looking leaves the folder as it was
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	got, err := filelock.TryLock(f)
	if err != nil {
		return false
	}
	if got {
		filelock.Unlock(f)
		return false
	}
	return true
}

// LetGoOfHost lets go of the lock HoldHost took: for tests, whose folders
// go when they end.
func LetGoOfHost() {
	heldHost.Lock()
	defer heldHost.Unlock()
	if heldHost.f != nil {
		filelock.Unlock(heldHost.f)
		heldHost.f.Close()
		heldHost.f, heldHost.path = nil, ""
	}
}
