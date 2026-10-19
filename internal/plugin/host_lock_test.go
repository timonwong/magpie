package plugin

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestHostLockHolder is the serving magpie of TestHostHeldElsewhere: it
// takes the lock, says so, and holds it until it is killed.
func TestHostLockHolder(t *testing.T) {
	if os.Getenv("MAGPIE_HOST_LOCK_HOLDER") == "" {
		t.Skip("run by TestHostHeldElsewhere")
	}
	if !HoldHost() {
		t.Fatal("not held")
	}
	os.Stdout.WriteString("held\n")
	time.Sleep(time.Minute)
}

// lockHome is a magpie folder of the test's own.
func lockHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Cleanup(LetGoOfHost)
}

// A command beside the magpie serving its folder sees the lock that one
// holds, and once that one ends, however it ends (killed here, as a Mac
// shut down with magpie running ends it), sees it free: nothing left
// behind looks held.
func TestHostHeldElsewhere(t *testing.T) {
	lockHome(t)
	if HostHeldElsewhere() {
		t.Fatal("held with no magpie ever having served this folder")
	}
	if _, err := os.Stat(filepath.Dir(hostLockPath())); err == nil {
		t.Fatal("looking for the serving magpie made the magpie folder")
	}

	holder := exec.Command(os.Args[0], "-test.run=^TestHostLockHolder$", "-test.count=1")
	holder.Env = append(os.Environ(), "MAGPIE_HOST_LOCK_HOLDER=1")
	out, _ := holder.StdoutPipe()
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { holder.Process.Kill(); holder.Wait() })
	if line, _ := bufio.NewReader(out).ReadString('\n'); line != "held\n" {
		t.Fatalf("the serving magpie: %q", line)
	}
	if !HostHeldElsewhere() {
		t.Fatal("not held while another magpie serves this folder")
	}
	// a serving magpie started beside it goes on without the lock
	if HoldHost() {
		t.Fatal("taken while another magpie holds it")
	}

	holder.Process.Kill()
	holder.Wait()
	// the system lets go for it; Windows may take a moment to
	for deadline := time.Now().Add(10 * time.Second); HostHeldElsewhere(); {
		if time.Now().After(deadline) {
			t.Fatal("still held after the serving magpie was killed")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// this one serves now: it holds the lock, and doesn't take its own
	// for another's
	if !HoldHost() {
		t.Fatal("not taken with no other magpie serving")
	}
	if HostHeldElsewhere() {
		t.Fatal("this magpie's own lock taken for another's")
	}
}
