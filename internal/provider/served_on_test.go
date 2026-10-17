package provider

import (
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// ServedOn is whether a provider serves a model on an API itself, wherever
// that API comes in its order: a relay's or a preset's Responses URL
// counts as OpenAI's own does, unless the model's list names others. A
// sign-in served through its agent's own client serves none.
func TestServedOn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	if err := catalog.SaveLive("listed", "https://listed.test/v1", []catalog.Model{{ID: "glm-5", APIs: []string{"chat"}}}); err != nil {
		t.Fatal(err)
	}
	groq := Preset("groq")
	if groq == nil || groq.Chat == "" || groq.Responses == "" {
		t.Fatalf("groq preset: %+v", groq)
	}
	both := Provider{ID: "both", Chat: "https://both.test/v1", Responses: "https://both.test/v1"}
	every := Provider{ID: "every", Chat: "https://every.test/v1", Responses: "https://every.test/v1", Anthropic: "https://every.test"}
	for _, c := range []struct {
		name  string
		p     Provider
		model string
		want  bool
	}{
		{"relay GPT", both, "gpt-5.5", true},
		{"relay other", both, "grok-4.6", true},
		{"preset", Provider{ID: "groq", Preset: "groq", Chat: groq.Chat, Responses: groq.Responses}, "llama-4", true},
		{"chat only", Provider{ID: "relay", Chat: "https://relay.test/v1"}, "glm-4.6", false},
		{"listed on chat only", Provider{ID: "listed", Chat: "https://listed.test/v1", Responses: "https://listed.test/v1"}, "glm-5", false},
		{"Claude where all three are", every, "claude-sonnet-5", true},
		{"ChatGPT sign-in", Provider{ID: "codex", Responses: CodexBase, Account: &Account{Agent: "codex"}}, "gpt-5.5", true},
		{"Claude Code", Provider{ID: "claude", Anthropic: "https://api.anthropic.com", Responses: "https://api.anthropic.com", Account: &Account{Agent: "claude"}}, "claude-sonnet-5", false},
		{"nothing", Provider{ID: "none"}, "m", false},
	} {
		if got := c.p.ServedOn(c.model, Responses); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	// a Claude there is still best asked on Messages
	if n := every.Native("claude-sonnet-5"); n != Anthropic {
		t.Errorf("Claude where all three are: Native %q", n)
	}
}
