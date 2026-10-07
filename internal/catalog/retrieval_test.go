package catalog

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// RetrievalID names the embedding and rerank models vendors list by their
// ids alone, and leaves every chat model a chat model.
func TestRetrievalID(t *testing.T) {
	for id, want := range map[string]string{
		"text-embedding-3-small":                 Embedding,
		"text-embedding-ada-002":                 Embedding,
		"jina-embeddings-v3":                     Embedding,
		"BAAI/bge-m3":                            Embedding,
		"bge-large-zh-v1.5":                      Embedding,
		"Pro/BAAI/bge-m3":                        Embedding,
		"nvidia/nv-embedqa-e5-v5":                Embedding,
		"intfloat/multilingual-e5-large":         Embedding,
		"Alibaba-NLP/gte-Qwen2-7B-instruct":      Embedding,
		"Qwen/Qwen3-Embedding-8B":                Embedding,
		"gemini-embedding-001":                   Embedding,
		"nomic-embed-text":                       Embedding,
		"embed-v4.0":                             Embedding,
		"voyage-3":                               Embedding,
		"voyage-code-3":                          Embedding,
		"netease-youdao/bce-embedding-base_v1":   Embedding,
		"BAAI/bge-reranker-v2-m3":                Rerank,
		"bge-reranker-v2-m3":                     Rerank,
		"jina-reranker-m0":                       Rerank,
		"rerank-v3.5":                            Rerank,
		"Qwen/Qwen3-Reranker-8B":                 Rerank,
		"rerank-2":                               Rerank,
		"gpt-5.5":                                "",
		"claude-sonnet-5":                        "",
		"deepseek-ai/DeepSeek-V3.2":              "",
		"Qwen/Qwen3-235B-A22B-Instruct-2507":     "",
		"moonshotai/Kimi-K2-Instruct":            "",
		"zai-org/GLM-4.6":                        "",
		"gemini-2.5-pro":                         "",
		"meta-llama/Llama-3.3-70B-Instruct":      "",
		"command-a-03-2025":                      "",
		"openai/gpt-oss-120b":                    "",
		"mistralai/Mistral-Small-3.2-24B":        "",
		"deepseek-r1-distill-qwen-32b":           "",
		"nvidia/llama-3.3-nemotron-super-49b-v1": "",
		"gpt-image-2":                            "",
	} {
		if got := RetrievalID(id); got != want {
			t.Errorf("RetrievalID(%q) = %q, want %q", id, got, want)
		}
	}
}

// A vendor's list keeps its embedding and rerank models, marked as such
// by the list's own type where it has one, else by their ids, while the
// models to talk to (Live) are only the chat ones; speech is still left
// out.
func TestFetchedListKeepsRetrievalModels(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// SiliconFlow's ids, Together's "type" and OpenRouter's
		// architecture side by side
		io.WriteString(w, `{"object":"list","data":[
{"id":"deepseek-ai/DeepSeek-V3.2","object":"model","created":0,"owned_by":""},
{"id":"BAAI/bge-m3","object":"model","created":0,"owned_by":""},
{"id":"BAAI/bge-reranker-v2-m3","object":"model","created":0,"owned_by":""},
{"id":"togethercomputer/m2-bert-80M-32k-retrieval","object":"model","type":"embedding"},
{"id":"Salesforce/Llama-Rank-V1","object":"model","type":"rerank"},
{"id":"openai/text-embedding-3-small","architecture":{"input_modalities":["text"],"output_modalities":["embeddings"]}},
{"id":"whisper-1","object":"model"},
{"id":"gpt-5.5","object":"model","type":"chat"}]}`)
	}))
	defer srv.Close()
	ms, err := FetchURL(t.Context(), srv.URL+"/v1/models", "k", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, m := range ms {
		kinds[m.ID] = m.Retrieval
	}
	want := map[string]string{
		"deepseek-ai/DeepSeek-V3.2":                  "",
		"BAAI/bge-m3":                                Embedding,
		"BAAI/bge-reranker-v2-m3":                    Rerank,
		"togethercomputer/m2-bert-80M-32k-retrieval": Embedding,
		"Salesforce/Llama-Rank-V1":                   Rerank,
		"openai/text-embedding-3-small":              Embedding,
		"gpt-5.5":                                    "",
	}
	if len(kinds) != len(want) {
		t.Fatalf("kept %v", kinds)
	}
	for id, k := range want {
		if got, ok := kinds[id]; !ok || got != k {
			t.Errorf("%s: kept %v as %q, want %q", id, ok, got, k)
		}
	}

	if err := SaveLive("relay", srv.URL, ms); err != nil {
		t.Fatal(err)
	}
	live, _, _ := Live("relay")
	var chat []string
	for _, m := range live {
		chat = append(chat, m.ID)
	}
	if !slices.Equal(chat, []string{"deepseek-ai/DeepSeek-V3.2", "gpt-5.5"}) {
		t.Fatalf("Live %v", chat)
	}
	var ret []string
	for _, m := range LiveRetrievers("relay") {
		ret = append(ret, m.ID+"="+m.Retrieval)
	}
	if !slices.Equal(ret, []string{"BAAI/bge-m3=embedding", "BAAI/bge-reranker-v2-m3=rerank", "togethercomputer/m2-bert-80M-32k-retrieval=embedding", "Salesforce/Llama-Rank-V1=rerank", "openai/text-embedding-3-small=embedding"}) {
		t.Fatalf("LiveRetrievers %v", ret)
	}
}

// A list saved before retrieval models were kept has none, and reads as
// it did.
func TestOldLiveFileHasNoRetrievers(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := SaveLive("old", "https://x/v1", []Model{{ID: "gpt-5.5", Name: "GPT-5.5"}, {ID: "gpt-image-2", Draws: true}}); err != nil {
		t.Fatal(err)
	}
	if ms, _, ok := Live("old"); !ok || len(ms) != 1 || ms[0].ID != "gpt-5.5" {
		t.Fatalf("Live %v %v", ms, ok)
	}
	if rs := LiveRetrievers("old"); len(rs) != 0 {
		t.Fatalf("LiveRetrievers %v", rs)
	}
}
