package main

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A local server's preset is added, or set, at another port or computer
// with address=, each API keeping its path; a vendor's preset or a custom
// provider is told to give url= instead.
func TestProviderAddressPair(t *testing.T) {
	p, _ := provider.FromPreset("ollama")
	if err := applyPairs(&p, []string{"address=192.168.1.5:11434"}); err != nil {
		t.Fatal(err)
	}
	if p.Chat != "http://192.168.1.5:11434/v1" || p.Anthropic != "http://192.168.1.5:11434" || p.Responses != "" {
		t.Fatalf("ollama: %q %q %q", p.Chat, p.Responses, p.Anthropic)
	}
	p, _ = provider.FromPreset("omlx")
	if err := applyPairs(&p, []string{"address=https://studio.local:8001/v1"}); err != nil {
		t.Fatal(err)
	}
	if p.Chat != "https://studio.local:8001/v1" || p.Responses != "https://studio.local:8001/v1" || p.Anthropic != "https://studio.local:8001" {
		t.Fatalf("omlx: %q %q %q", p.Chat, p.Responses, p.Anthropic)
	}
	p, _ = provider.FromPreset("lmstudio")
	if err := applyPairs(&p, []string{"address=ftp://box"}); err == nil || !strings.Contains(err.Error(), "is not an address like") || p.Chat != "http://localhost:1234/v1" {
		t.Fatalf("bad address: %v, chat %q", err, p.Chat)
	}
	d, _ := provider.FromPreset("deepseek")
	if err := applyPairs(&d, []string{"address=127.0.0.1:1"}); err == nil || !strings.Contains(err.Error(), "url=") || strings.Contains(d.Chat, "127.0.0.1") {
		t.Fatalf("deepseek: %v, chat %q", err, d.Chat)
	}
	c := provider.Provider{Name: "My Relay", Chat: "https://relay.example.com/v1"}
	if err := applyPairs(&c, []string{"address=127.0.0.1:1"}); err == nil || !strings.Contains(err.Error(), "url=") {
		t.Fatalf("custom: %v", err)
	}
}
