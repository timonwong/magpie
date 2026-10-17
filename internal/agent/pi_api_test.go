package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"gopkg.in/yaml.v3"
)

// Pi's provider is on openai-responses, and each model is asked on an API
// its provider serves it on, so the gateway relays the request as it is: one
// best on Messages goes on anthropic-messages at the gateway; one its
// provider serves on Responses at all, a relay's or a preset's as much as
// OpenAI's, takes the provider's api; one served on Chat alone, by its URLs
// or by its list, goes on openai-completions. A group with a member on
// Responses takes the provider's api (#1594: one Chat member put a GPT group
// on Chat, and its tool-call arguments were held for a Chat answer); a group
// with none stays on Chat. omp, given the same groups, still asks a mixed
// one on Chat. Sign-ins: TestServedOn.
func TestPiModelsAskedOnTheirNativeAPI(t *testing.T) {
	home := syncHome(t)
	groq := provider.Preset("groq")
	if groq == nil || groq.Chat == "" || groq.Responses == "" {
		t.Fatalf("groq preset: %+v", groq)
	}
	// its list serves glm-5 on Chat alone
	if err := catalog.SaveLive("listed", "http://127.0.0.1:1/v1", []catalog.Model{{ID: "glm-5", APIs: []string{"chat"}}}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []provider.Provider{
		{ID: "resp", Name: "Resp", Key: "k", Responses: "http://127.0.0.1:1/v1", Models: []string{"grok-5", "gpt-5.5"}},
		{ID: "openai", Name: "OpenAI", Key: "k", Chat: "https://api.openai.com/v1", Responses: "https://api.openai.com/v1", Models: []string{"gpt-5.5", "whisper-x"}},
		{ID: "anth", Name: "Anth", Key: "k", Anthropic: "http://127.0.0.1:1", Models: []string{"claude-sonnet-5", "kimi-k3"}},
		{ID: "both", Name: "Both", Key: "k", Chat: "http://127.0.0.1:1/v1", Responses: "http://127.0.0.1:1/v1", Models: []string{"gpt-5.4", "grok-4.6"}},
		{ID: "groq", Name: "Groq", Preset: "groq", Key: "k", Chat: groq.Chat, Responses: groq.Responses, Models: []string{"llama-4"}},
		{ID: "listed", Name: "Listed", Key: "k", Chat: "http://127.0.0.1:1/v1", Responses: "http://127.0.0.1:1/v1", Models: []string{"glm-5"}},
		{ID: "every", Name: "Every", Key: "k", Chat: "http://127.0.0.1:1/v1", Responses: "http://127.0.0.1:1/v1", Anthropic: "http://127.0.0.1:1", Models: []string{"claude-sonnet-5", "kimi-k3"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	// a group goes on Responses only when every member is best served there
	// (#1594): one with a Chat member, or of Messages ones, stays on Chat
	for _, g := range []provider.Group{
		{ID: "gpts", Name: "GPTs", Members: []string{"resp/gpt-5.5", "openai/gpt-5.5", "resp/grok-5"}},
		{ID: "mixed", Name: "Mixed", Members: []string{"resp/grok-5", "relay/glm-4.6"}},
		{ID: "claudes", Name: "Claudes", Members: []string{"anth/claude-sonnet-5", "anth/kimi-k3"}},
		{ID: "outer", Name: "Outer", Members: []string{"group/gpts", "openai/gpt-5.5"}},
		{ID: "chats", Name: "Chats", Members: []string{"relay/glm-4.6", "listed/glm-5"}},
		{ID: "relaymix", Name: "Relay mix", Members: []string{"both/grok-4.6", "relay/glm-4.6"}},
		{ID: "nested", Name: "Nested", Members: []string{"group/mixed", "anth/kimi-k3"}},
		{ID: "gpt-mix", Name: "GPT mix", Members: []string{"resp/gpt-5.5", "relay/glm-4.6"}},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	if err := pi(home).Field("model").Set("magpie/resp/grok-5"); err != nil {
		t.Fatal(err)
	}
	var file map[string]any
	if err := json.Unmarshal([]byte(readFile(filepath.Join(home, ".pi", "agent", "models.json"))), &file); err != nil {
		t.Fatal(err)
	}
	pm := file["providers"].(map[string]any)["magpie"].(map[string]any)
	if pm["api"] != "openai-responses" || pm["baseUrl"] != gatewayV1() {
		t.Fatalf("provider: %v", pm)
	}
	apis, entries := map[string]any{}, map[string]map[string]any{}
	for _, raw := range pm["models"].([]any) {
		m := raw.(map[string]any)
		apis[m["id"].(string)], entries[m["id"].(string)] = m["api"], m
	}
	for id, want := range map[string]any{
		"resp/grok-5":           nil,
		"resp/gpt-5.5":          nil,
		"openai/gpt-5.5":        nil,
		"openai/whisper-x":      nil,
		"relay/glm-4.6":         "openai-completions",
		"both/gpt-5.4":          nil,
		"both/grok-4.6":         nil,
		"groq/llama-4":          nil,
		"listed/glm-5":          "openai-completions",
		"every/claude-sonnet-5": "anthropic-messages",
		"every/kimi-k3":         nil,
		"group/relaymix":        nil,
		"anth/claude-sonnet-5":  "anthropic-messages",
		"anth/kimi-k3":          "anthropic-messages",
		"group/auto-gpt-5-5":    nil,
		"group/gpts":            nil,
		"group/outer":           nil,
		"group/mixed":           nil,
		"group/nested":          nil,
		"group/gpt-mix":         nil,
		"group/claudes":         "openai-completions",
		"group/chats":           "openai-completions",
	} {
		got, ok := apis[id]
		if !ok {
			t.Errorf("%s not listed: %v", id, apis)
		} else if got != want {
			t.Errorf("%s: api %v, want %v", id, got, want)
		}
	}
	// Anthropic's SDK posts to baseUrl + /v1/messages; only the Claude
	// that refuses a thinking budget is told to think adaptively
	for id, adaptive := range map[string]bool{"anth/claude-sonnet-5": true, "anth/kimi-k3": false} {
		e := entries[id]
		if e["baseUrl"] != gateway.URL() {
			t.Errorf("%s: baseUrl %v, want %s", id, e["baseUrl"], gateway.URL())
		}
		c, _ := e["compat"].(map[string]any)
		if got := c["forceAdaptiveThinking"] == true; got != adaptive {
			t.Errorf("%s: compat %v", id, e["compat"])
		}
	}
	if _, ok := entries["resp/grok-5"]["baseUrl"]; ok {
		t.Errorf("a Responses model keeps the provider's baseUrl: %v", entries["resp/grok-5"])
	}
	// a GPT is told its cache's lifetime only where the gateway relays the
	// request as it is: not in a group it may translate for
	for id, want := range map[string]bool{"resp/gpt-5.5": true, "group/gpts": true, "group/gpt-mix": false, "both/gpt-5.4": false} {
		if _, got := entries[id]["promptCache"]; got != want {
			t.Errorf("%s: promptCache %v, want one: %v", id, entries[id]["promptCache"], want)
		}
	}

	// Pi alone: omp asks the same mixed groups on Chat, as before
	was := ompVersion
	ompVersion = func() string { return "16.3.5" }
	t.Cleanup(func() { ompVersion = was })
	if err := omp(home).Field("model").Set("magpie/resp/grok-5"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, ".omp", "agent", "models.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var omps map[string]any
	if err := yaml.Unmarshal(b, &omps); err != nil {
		t.Fatal(err)
	}
	op := omps["providers"].(map[string]any)["magpie"].(map[string]any)
	if op["api"] != "openai-completions" {
		t.Errorf("omp provider: %v", op["api"])
	}
	seen := 0
	for _, raw := range op["models"].([]any) {
		m := raw.(map[string]any)
		if id := m["id"]; id == "group/mixed" || id == "group/nested" || id == "group/relaymix" {
			seen++
			if m["api"] != nil {
				t.Errorf("omp %s: api %v, want none", id, m["api"])
			}
		}
	}
	if seen != 3 {
		t.Errorf("omp lists %d of the mixed groups: %s", seen, b)
	}
}
