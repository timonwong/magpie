package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// leaveHome is a home of the test's own with a magpie gateway serving
// its folder at MAGPIE_ADDR, as the app does while a command runs beside
// it. The switch is put back when the test ends, for the next one.
func leaveHome(t *testing.T) {
	t.Helper()
	groupsHome(t)
	gw := httptest.NewServer(gateway.New().Handler())
	t.Cleanup(gw.Close)
	t.Setenv("MAGPIE_ADDR", strings.TrimPrefix(gw.URL, "http://"))
	t.Cleanup(func() { plugin.LeaveToServer(false) })
	t.Cleanup(plugin.Settle)
}

// A command that doesn't serve the gateway leaves the plugins to the
// magpie serving this folder: `magpie --version` beside the app started a
// plugin host of its own, which loaded every plugin again, and one keeping
// a single service per data folder printed its lock error on stderr.
func TestCommandLeavesPluginsToTheServingMagpie(t *testing.T) {
	leaveHome(t)
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
// stops, keeps its plugins: they are its own to run.
func TestServingCommandKeepsItsPlugins(t *testing.T) {
	leaveHome(t)
	for _, args := range [][]string{nil, {"tui"}, {"web"}, {"app"}, {"gui"}, {"tray"}, {"-Embedding"}, {"panel"}, {"serve"}, {"daemon", "status"}} {
		plugin.LeaveToServer(true)
		leavePlugins(args)
		if plugin.LeftToServer() {
			t.Errorf("magpie %q leaves its plugins to another magpie", args)
		}
	}
}

// With no magpie serving this folder (none answering, or one keeping
// another folder), a command asks the plugins as it always has.
func TestCommandAsksPluginsWithNoServer(t *testing.T) {
	groupsHome(t)
	t.Cleanup(func() { plugin.LeaveToServer(false) })
	t.Cleanup(plugin.Settle)
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:1") // nothing answers
	plugin.LeaveToServer(true)
	if err := run([]string{"--version"}); err != nil {
		t.Fatal(err)
	}
	if plugin.LeftToServer() {
		t.Error("magpie --version with no gateway leaves the plugins to a magpie that isn't there")
	}

	// a magpie keeping another folder: its providers aren't this folder's
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"name":"magpie","version":"dev","folder":"0123456789ab"}`))
	}))
	defer other.Close()
	t.Setenv("MAGPIE_ADDR", strings.TrimPrefix(other.URL, "http://"))
	plugin.LeaveToServer(true)
	if err := run([]string{"--version"}); err != nil {
		t.Fatal(err)
	}
	if plugin.LeftToServer() {
		t.Error("magpie --version leaves the plugins to a magpie keeping another folder")
	}
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
	list, _ := json.Marshal(plugin.List{Plugins: []plugin.Entry{{Spec: spec}}})
	if err := os.WriteFile(plugin.ListPath(), list, 0o600); err != nil {
		t.Fatal(err)
	}
	kept, _ := json.Marshal([]plugin.Provider{{ID: "fakeco", Spec: spec, Name: "FakeCo", Models: []plugin.Model{{ID: "fake-1"}, {ID: "fake-2"}}}})
	if err := os.WriteFile(filepath.Join(settings.Dir(), "plugin-providers.json"), kept, 0o600); err != nil {
		t.Fatal(err)
	}
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
