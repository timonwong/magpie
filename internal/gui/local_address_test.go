package gui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A local server's provider (Ollama, LM Studio, oMLX) is saved at the
// address the editor gives, each API keeping its path; a save that gives
// none keeps the URLs it has, customised ones included.
func TestProviderSaveLocalAddress(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(body)))
		return w
	}
	urls := func(id string) string {
		t.Helper()
		p, err := provider.Find(id)
		if err != nil {
			t.Fatal(err)
		}
		return p.Chat + " " + p.Responses + " " + p.Anthropic
	}

	// a bad address is refused, and nothing is added
	if w := post(`{"id":"ollama","preset":"ollama","key":"","address":"ftp://192.168.1.5","new":true}`); w.Code == 200 || !strings.Contains(w.Body.String(), "is not an address like") {
		t.Fatalf("bad address answered %d", w.Code)
	}
	if _, err := provider.Find("ollama"); err == nil {
		t.Fatal("added at a refused address")
	}

	// added at another port, as the Add form sends it: no URLs, the address
	if w := post(`{"id":"ollama","preset":"ollama","key":"","address":"127.0.0.1:1","new":true}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got := urls("ollama"); got != "http://127.0.0.1:1/v1  http://127.0.0.1:1" {
		t.Fatalf("added at %q", got)
	}
	if p, _ := provider.Find("ollama"); p.Preset != "ollama" || p.Icon != "ollama" {
		t.Fatalf("lost its preset: %+v", p)
	}

	// edited: its URLs as they are, and the address changed
	if w := post(`{"id":"ollama","from":"ollama","preset":"ollama","chat":"http://127.0.0.1:1/v1","anthropic":"http://127.0.0.1:1","address":"http://127.0.0.1:2/v1"}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got := urls("ollama"); got != "http://127.0.0.1:2/v1  http://127.0.0.1:2" {
		t.Fatalf("edited to %q", got)
	}

	// a bad address on an edit leaves it as it was
	if w := post(`{"id":"ollama","from":"ollama","preset":"ollama","chat":"http://127.0.0.1:2/v1","anthropic":"http://127.0.0.1:2","address":"my ollama box"}`); w.Code == 200 {
		t.Fatalf("bad address on an edit: %d", w.Code)
	}
	if got := urls("ollama"); got != "http://127.0.0.1:2/v1  http://127.0.0.1:2" {
		t.Fatalf("a refused edit left %q", got)
	}

	// URLs set apart (another app's import, or the CLI), saved with the
	// address untouched, stay as they are
	custom := provider.Provider{ID: "lmstudio", Name: "LM Studio", Preset: "lmstudio", Icon: "lmstudio", Chat: "http://127.0.0.1:3/api/v0"}
	if err := provider.Save(custom); err != nil {
		t.Fatal(err)
	}
	if w := post(`{"id":"lmstudio","from":"lmstudio","preset":"lmstudio","chat":"http://127.0.0.1:3/api/v0"}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got := urls("lmstudio"); got != "http://127.0.0.1:3/api/v0  " {
		t.Fatalf("kept as %q", got)
	}

	// a vendor's preset has no address to give: its URLs are the preset's
	if w := post(`{"id":"deepseek","preset":"deepseek","key":"k","address":"127.0.0.1:4","new":true}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if p, _ := provider.Find("deepseek"); strings.Contains(p.Chat, "127.0.0.1") {
		t.Fatalf("deepseek moved to %q", p.Chat)
	}
}
