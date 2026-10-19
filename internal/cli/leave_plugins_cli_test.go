package cli

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/filelock"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// TestServingMagpieHolder is the app of the tests below: it takes the
// lock a magpie serving the gateway holds, in the folder it was handed,
// says so, and holds it until it is killed.
func TestServingMagpieHolder(t *testing.T) {
	if os.Getenv("MAGPIE_SERVING_HOLDER") == "" {
		t.Skip("run by the plugin host lock tests")
	}
	if !plugin.HoldHost() {
		t.Fatal("not held")
	}
	os.Stdout.WriteString("held\n")
	time.Sleep(time.Minute)
}

// servingMagpie starts the app beside the test, on the test's folder, and
// gives what kills it; it is killed when the test ends too.
func servingMagpie(t *testing.T) (kill func()) {
	t.Helper()
	holder := exec.Command(os.Args[0], "-test.run=^TestServingMagpieHolder$", "-test.count=1")
	holder.Env = append(os.Environ(), "MAGPIE_SERVING_HOLDER=1")
	out, _ := holder.StdoutPipe()
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	kill = func() { holder.Process.Kill(); holder.Wait() }
	t.Cleanup(kill)
	if line, _ := bufio.NewReader(out).ReadString('\n'); line != "held\n" {
		t.Fatalf("the serving magpie: %q", line)
	}
	return kill
}

// leaveHome is a home of the test's own; the switch and the lock this
// process takes are put back when the test ends, for the next one.
func leaveHome(t *testing.T) {
	t.Helper()
	groupsHome(t)
	t.Cleanup(plugin.LetGoOfHost)
	t.Cleanup(func() { plugin.LeaveToServer(false) })
	t.Cleanup(plugin.Settle)
}

// A command that doesn't serve the gateway leaves the plugins to the
// magpie serving this folder: `magpie --version` beside the app started a
// plugin host of its own, which loaded every plugin again, and one keeping
// a single service per data folder printed its lock error on stderr.
func TestCommandLeavesPluginsToTheServingMagpie(t *testing.T) {
	leaveHome(t)
	servingMagpie(t)
	for _, args := range [][]string{{"--version"}, {"providers"}, {"models"}} {
		plugin.LeaveToServer(false)
		if _, err := stdoutOf(t, func() error { return run(args) }); err != nil {
			t.Fatalf("magpie %s: %v", strings.Join(args, " "), err)
		}
		if !plugin.LeftToServer() {
			t.Errorf("magpie %s beside a magpie serving this folder still asks the plugins itself", strings.Join(args, " "))
		}
	}
}

// What serves the gateway, or takes it over once the one serving it
// stops, keeps its plugins: they are its own to run. Beside another
// serving magpie it goes on without the lock, never waiting on it; with
// none, it takes the lock.
func TestServingCommandHoldsTheHost(t *testing.T) {
	leaveHome(t)
	kill := servingMagpie(t)
	serving := [][]string{nil, {"tui"}, {"web"}, {"app"}, {"gui"}, {"tray"}, {"-Embedding"}, {"panel"}, {"serve"}}
	for _, args := range serving {
		plugin.LeaveToServer(true)
		leavePlugins(args)
		if plugin.LeftToServer() {
			t.Errorf("magpie %q leaves its plugins to another magpie", args)
		}
	}

	kill()
	for deadline := time.Now().Add(10 * time.Second); plugin.HostHeldElsewhere(); {
		if time.Now().After(deadline) {
			t.Fatal("still held after the serving magpie was killed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, args := range serving {
		plugin.LetGoOfHost()
		leavePlugins(args)
		f, err := os.OpenFile(filepath.Join(settings.Dir(), "plugin-host.lock"), os.O_RDWR, 0)
		if err != nil {
			t.Fatalf("magpie %q made no lock: %v", args, err)
		}
		got, err := filelock.TryLock(f)
		f.Close()
		if err != nil || got {
			t.Errorf("magpie %q serving doesn't hold the plugin host's lock (taken here: %v, %v)", args, got, err)
		}
	}
}

// With no magpie serving this folder (none ever did, the one that did
// ended, or one serves another folder), a command asks the plugins as it
// always has.
func TestCommandAsksPluginsWithNoServer(t *testing.T) {
	leaveHome(t)
	check := func(why string) {
		t.Helper()
		plugin.LeaveToServer(true)
		if err := run([]string{"--version"}); err != nil {
			t.Fatal(err)
		}
		if plugin.LeftToServer() {
			t.Errorf("magpie --version with %s leaves the plugins to a magpie that isn't there", why)
		}
	}
	check("no lock file")

	kill := servingMagpie(t)
	kill()
	for deadline := time.Now().Add(10 * time.Second); plugin.HostHeldElsewhere(); {
		if time.Now().After(deadline) {
			t.Fatal("still held after the serving magpie was killed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	check("the serving magpie gone")

	servingMagpie(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	check("another folder's magpie serving")
}

// `magpie plugins` beside the magpie serving this folder lists the
// plugins' providers as that one keeps them, with no host of its own:
// the list started a second host, and with it the plugins' services.
func TestPluginListBesideTheServingMagpie(t *testing.T) {
	leaveHome(t)
	// a host started here fails at once: there is no Bun at this path
	t.Setenv("MAGPIE_BUN", filepath.Join(t.TempDir(), "no-bun"))
	spec, err := filepath.Abs("../plugin/testdata/fake/index.js")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	list, _ := json.Marshal(plugin.List{Plugins: []plugin.Entry{{Spec: spec}}})
	if err := os.WriteFile(plugin.ListPath(), list, 0o600); err != nil {
		t.Fatal(err)
	}
	kept, _ := json.Marshal([]plugin.Provider{{ID: "fakeco", Spec: spec, Name: "FakeCo", Models: []plugin.Model{{ID: "fake-1"}, {ID: "fake-2"}}}})
	if err := os.WriteFile(filepath.Join(settings.Dir(), "plugin-providers.json"), kept, 0o600); err != nil {
		t.Fatal(err)
	}
	servingMagpie(t)
	out, err := stdoutOf(t, func() error { return run([]string{"plugins"}) })
	if err != nil {
		t.Fatalf("magpie plugins started a host of its own: %v\n%s", err, out)
	}
	if !strings.Contains(out, "(FakeCo, 2 models)") {
		t.Errorf("magpie plugins doesn't list the serving magpie's providers:\n%s", out)
	}
	if plugin.Running() {
		t.Error("magpie plugins left a plugin host running beside the serving magpie")
	}
}
