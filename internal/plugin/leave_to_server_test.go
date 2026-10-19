package plugin

import (
	"context"
	"encoding/json"
	"os"
	"sync/atomic"
	"testing"
)

// A magpie that doesn't serve the gateway, while one serving its folder
// runs, reads the plugins' providers as that one keeps them and starts no
// host for them: every CLI command started a second host, and a plugin
// keeping one service per data folder (a lock file, a port) failed in it
// with "provider.models: …" on the command's stderr.
func TestCachedLeavesTheHostToTheServer(t *testing.T) {
	var asks atomic.Int64
	refreshSandbox(t, func(context.Context, uint64) error {
		asks.Add(1)
		return nil
	})
	spec := refreshAbsPath(t, "testdata/fake/index.js")
	// what the serving magpie keeps, and nothing kept in this process
	b, err := json.Marshal([]Provider{{ID: "fakeco", Spec: spec, Name: "FakeCo as served", Models: []Model{{ID: "fake-1"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(providersPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	UseCached(nil)
	LeaveToServer(true)
	t.Cleanup(func() { LeaveToServer(false) })

	for range 3 {
		ps := Cached()
		if len(ps) != 1 || ps[0].Name != "FakeCo as served" || len(ps[0].Models) != 1 {
			t.Fatalf("Cached = %+v, want the serving magpie's list from plugin-providers.json", ps)
		}
	}
	Refreshed()
	if n := asks.Load(); n != 0 {
		t.Fatalf("the plugins were asked %d times with another magpie serving this folder, want none", n)
	}
	if Running() {
		t.Fatal("a plugin host is running with another magpie serving this folder")
	}

	// none serves it, or whether one does isn't known: asked as before
	LeaveToServer(false)
	Cached()
	Refreshed()
	if n := asks.Load(); n != 1 {
		t.Fatalf("the plugins were asked %d times with no magpie serving this folder, want 1", n)
	}
}
