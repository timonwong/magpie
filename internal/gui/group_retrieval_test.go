package gui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The group editor offers a vendor's embedding and rerank models, each
// marked with its kind, beside its chat models; the pickers of models to
// talk to (a title's, a describer's) don't have them, nor a group of them.
func TestGroupEditorOffersRetrievalModels(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"object":"list","data":[{"id":"deepseek-ai/DeepSeek-V3.2"},{"id":"BAAI/bge-m3"},{"id":"BAAI/bge-reranker-v2-m3"}]}`)
	}))
	defer up.Close()
	p := provider.Provider{ID: "sf", Name: "SiliconFlow", Key: "k", Chat: up.URL + "/v1"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "emb", Name: "Embeddings", Members: []string{"sf/BAAI/bge-m3"}}); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, m := range groupsState().Models {
		kinds[m.ID] = m.Kind
	}
	for id, k := range map[string]string{"sf/deepseek-ai/DeepSeek-V3.2": "", "sf/BAAI/bge-m3": "embedding", "sf/BAAI/bge-reranker-v2-m3": "rerank"} {
		if got, ok := kinds[id]; !ok || got != k {
			t.Errorf("%s: offered %v as %q, want %q", id, ok, got, k)
		}
	}
	st := settingsState()
	for _, m := range append(st.TitleModels, st.VisionModels...) {
		if strings.Contains(m.ID, "bge") || m.ID == "group/emb" {
			t.Errorf("a model to talk to: %s", m.ID)
		}
	}
}
