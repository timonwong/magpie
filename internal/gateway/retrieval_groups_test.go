package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// stacks is a vendor whose /v1/models lists chat models beside embedding
// and rerank ones, as SiliconFlow's and an OpenAI relay's do; what each
// path was sent is counted.
type stacks struct {
	name, list string
	mu         sync.Mutex
	asked      map[string]int
}

func (v *stacks) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	v.mu.Lock()
	if v.asked == nil {
		v.asked = map[string]int{}
	}
	v.asked[r.URL.Path]++
	v.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/v1/models":
		io.WriteString(w, v.list)
	case "/v1/embeddings":
		io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.5]}],"model":"`+v.name+`","usage":{"prompt_tokens":2,"total_tokens":2}}`)
	case "/v1/rerank":
		io.WriteString(w, `{"model":"`+v.name+`","results":[{"index":0,"relevance_score":0.7}],"usage":{"total_tokens":3}}`)
	case "/v1/chat/completions":
		io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	default:
		w.WriteHeader(404)
	}
}

func (v *stacks) count(path string) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.asked[path]
}

// Embedding and rerank models a vendor lists are kept with its list, so
// routing groups can have them: the group editor offers them, and one
// two providers serve is a group magpie finds, which /v1/embeddings and
// /v1/rerank route over. Agents are never offered them, or a group of
// them, as models to talk to, and a chat asked of one is refused before
// any vendor is.
func TestRetrievalModelsInGroups(t *testing.T) {
	fresh(t)
	sf := &stacks{name: "sf", list: `{"object":"list","data":[
{"id":"deepseek-ai/DeepSeek-V3.2","object":"model","created":0,"owned_by":""},
{"id":"BAAI/bge-m3","object":"model","created":0,"owned_by":""},
{"id":"BAAI/bge-reranker-v2-m3","object":"model","created":0,"owned_by":""},
{"id":"Qwen/Qwen3-Embedding-8B","object":"model","created":0,"owned_by":""}]}`}
	oa := &stacks{name: "oa", list: `{"object":"list","data":[
{"id":"gpt-5.5","object":"model","owned_by":"openai"},
{"id":"text-embedding-3-small","object":"model","owned_by":"openai"},
{"id":"bge-m3","object":"model","owned_by":"relay"},
{"id":"bge-reranker-v2-m3","object":"model","owned_by":"relay"}]}`}
	for _, v := range []*stacks{sf, oa} {
		up := httptest.NewServer(v)
		t.Cleanup(up.Close)
		p := provider.Provider{ID: v.name, Name: strings.ToUpper(v.name), Chat: up.URL + "/v1", Key: "key"}
		if v == oa {
			// the old way round: an embedding model typed in among the picks
			p.Models = []string{"gpt-5.5", "text-embedding-3-small"}
		}
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Fetch(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	retrieval := func(s string) bool {
		s = strings.ToLower(s)
		return strings.Contains(s, "bge") || strings.Contains(s, "embedding") || strings.Contains(s, "rerank")
	}

	// agents' lists: OpenAI's, Anthropic's and Gemini's shapes
	s := New()
	for _, path := range []string{"/v1/models", "/v1beta/models"} {
		for _, h := range []map[string]string{nil, {"anthropic-version": "2023-06-01"}} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", path, nil)
			for k, v := range h {
				req.Header.Set(k, v)
			}
			s.Handler().ServeHTTP(rec, req)
			if b := rec.Body.String(); rec.Code != 200 || retrieval(b) || !strings.Contains(b, "gpt-5.5") || !strings.Contains(b, "DeepSeek-V3.2") {
				t.Errorf("%s %v: %d %s", path, h, rec.Code, b)
			}
		}
	}
	for _, e := range provider.Catalog() {
		if retrieval(e.ID) || e.Retrieval != "" {
			t.Errorf("agents are offered %s", e.ID)
		}
	}
	for _, id := range []string{"sf", "oa"} {
		p, _ := provider.Find(id)
		for _, m := range append(p.Exposed(), p.Available()...) {
			if retrieval(m.ID) {
				t.Errorf("%s's chat models have %s", id, m.ID)
			}
		}
	}

	// groups: every retrieval model can be a member, marked
	kinds := map[string]string{}
	for _, e := range provider.Served() {
		if e.Group == "" {
			kinds[e.ID] = e.Retrieval
		}
	}
	for id, k := range map[string]string{
		"sf/BAAI/bge-m3": "embedding", "sf/BAAI/bge-reranker-v2-m3": "rerank", "sf/Qwen/Qwen3-Embedding-8B": "embedding",
		"oa/text-embedding-3-small": "embedding", "oa/bge-m3": "embedding", "oa/bge-reranker-v2-m3": "rerank",
		"oa/gpt-5.5": "", "sf/deepseek-ai/DeepSeek-V3.2": "",
	} {
		if got, ok := kinds[id]; !ok || got != k {
			t.Errorf("%s: served %v as %q, want %q", id, ok, got, k)
		}
	}
	groups := map[string][]string{}
	for _, g := range provider.Groups() {
		groups[g.ID] = g.Members
	}
	if m := groups["auto-bge-m3"]; !slices.Equal(m, []string{"sf/BAAI/bge-m3", "oa/bge-m3"}) {
		t.Fatalf("auto-bge-m3: %v (groups %v)", m, groups)
	}
	if m := groups["auto-bge-reranker-v2-m3"]; len(m) != 2 {
		t.Fatalf("auto-bge-reranker-v2-m3: %v", m)
	}

	// the found groups serve the retrieval APIs
	if code, raw := postRetrieval(t, s, "/v1/embeddings", `{"model":"group/auto-bge-m3","input":"hi"}`); code != 200 || !strings.Contains(raw, `"embedding":[0.5]`) {
		t.Fatalf("embeddings: %d %s", code, raw)
	}
	if code, raw := postRetrieval(t, s, "/v1/rerank", `{"model":"group/auto-bge-reranker-v2-m3","query":"q","documents":["a"]}`); code != 200 || !strings.Contains(raw, `"relevance_score":0.7`) {
		t.Fatalf("rerank: %d %s", code, raw)
	}
	// a group of the user's, in turn, asks each member alike
	if err := provider.SaveGroup(provider.Group{ID: "emb", Name: "Embeddings", Members: []string{"sf/BAAI/bge-m3", "oa/bge-m3"}, Routing: "rotate"}); err != nil {
		t.Fatal(err)
	}
	sf0, oa0 := sf.count("/v1/embeddings"), oa.count("/v1/embeddings")
	for range 4 {
		if code, raw := postRetrieval(t, s, "/v1/embeddings", `{"model":"group/emb","input":"hi"}`); code != 200 {
			t.Fatalf("group/emb: %d %s", code, raw)
		}
	}
	if a, b := sf.count("/v1/embeddings")-sf0, oa.count("/v1/embeddings")-oa0; a != 2 || b != 2 {
		t.Fatalf("in turn: sf asked %d, oa %d", a, b)
	}
	for _, e := range provider.Catalog() {
		if e.ID == "group/emb" || e.ID == "group/auto-bge-m3" || e.ID == "group/auto-bge-reranker-v2-m3" {
			t.Errorf("agents are offered %s", e.ID)
		}
	}

	// a chat asked of one is refused, and no vendor is asked
	for _, m := range []string{"group/emb", "group/auto-bge-m3", "bge-m3", "sf/BAAI/bge-m3", "oa/text-embedding-3-small"} {
		code, raw := post(t, "/v1/chat/completions", `{"model":"`+m+`","messages":[{"role":"user","content":"hi"}]}`)
		if code != 400 || !strings.Contains(raw, "/v1/embeddings") {
			t.Errorf("chat with %s: %d %s", m, code, raw)
		}
	}
	if n := sf.count("/v1/chat/completions") + oa.count("/v1/chat/completions"); n != 0 {
		t.Fatalf("a chat went to a vendor %d times", n)
	}
	// a chat model still chats
	if code, raw := post(t, "/v1/chat/completions", `{"model":"oa/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`); code != 200 {
		t.Fatalf("chat: %d %s", code, raw)
	}
}
