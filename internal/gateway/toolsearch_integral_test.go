package gateway

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// searchLimitArgs is the reporter's search, as grok-4.7 wrote it through a
// relay to Codex 0.166: tool_search's schema says limit
// is a "number", Codex reads a usize, and 8.0 failed the call with
// "failed to parse tool_search arguments: invalid number" under an empty
// call_id, so the model never saw the MCP server's tools nor the error.
const searchLimitArgs = `{"query":"lody archive session help","limit":8.0}`

// codexSearchTurn is a Codex turn that offers its client-run tool search,
// the shape that makes magpie send the search as a function.
func codexSearchTurn(model string, stream bool) string {
	s := "false"
	if stream {
		s = "true"
	}
	return `{"model":"` + model + `","stream":` + s + `,"store":false,` + searchTools + `,
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"archive this session"}]}]}`
}

// searchCallLimits is each tool_search_call's limit, as written, in every
// place a Responses stream or reply hands Codex the call.
func searchCallLimits(t *testing.T, body []byte) []string {
	t.Helper()
	var limits []string
	read := func(it map[string]json.RawMessage) {
		var typ string
		json.Unmarshal(it["type"], &typ)
		if typ != "tool_search_call" {
			return
		}
		var args map[string]json.RawMessage
		if err := json.Unmarshal(it["arguments"], &args); err != nil {
			t.Fatalf("tool_search_call arguments %s: %v", it["arguments"], err)
		}
		limits = append(limits, string(args["limit"]))
	}
	items := func(raw json.RawMessage) {
		var out []map[string]json.RawMessage
		json.Unmarshal(raw, &out)
		for _, it := range out {
			read(it)
		}
	}
	if !bytes.Contains(body, []byte("data:")) {
		var res map[string]json.RawMessage
		if err := json.Unmarshal(body, &res); err != nil {
			t.Fatalf("reply %s: %v", body, err)
		}
		items(res["output"])
		return limits
	}
	for _, line := range strings.Split(string(body), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev map[string]json.RawMessage
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		var it map[string]json.RawMessage
		if json.Unmarshal(ev["item"], &it) == nil && it != nil {
			read(it)
		}
		var res map[string]json.RawMessage
		if json.Unmarshal(ev["response"], &res) == nil && res != nil {
			items(res["output"])
		}
	}
	return limits
}

// Relayed to a Responses API that is offered the search as a function, the
// model's 8.0 reaches Codex's tool_search_call as 8, wherever the stream
// carries the call and however the deltas are cut.
func TestRelayedToolSearchLimitIsCodexInteger(t *testing.T) {
	for cut, pieces := range someCuts(searchLimitArgs) {
		t.Run(cut, func(t *testing.T) {
			fresh(t)
			up := httptest.NewServer(responsesCall("tool_search", searchLimitArgs, pieces))
			defer up.Close()
			if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"grok-4.7"}, Responses: up.URL + "/v1"}); err != nil {
				t.Fatal(err)
			}
			body := askGrok(t, "/v1/responses", codexSearchTurn("relay/grok-4.7", true))
			limits := searchCallLimits(t, body)
			// output_item.added carries no arguments yet; done and completed do
			if len(limits) < 2 {
				t.Fatalf("Codex was handed %d tool_search_calls with a limit: %s", len(limits), body)
			}
			for _, l := range limits {
				if l != "" && l != "8" {
					t.Fatalf("tool_search_call limit %s, want 8: %s", l, body)
				}
			}
		})
	}
}

// Translated from a Chat API, the same search reaches Codex as 8 too.
func TestTranslatedToolSearchLimitIsCodexInteger(t *testing.T) {
	for _, stream := range []bool{true, false} {
		fresh(t)
		up := httptest.NewServer(chatCall("tool_search", searchLimitArgs, someCuts(searchLimitArgs)["by byte"]))
		if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"grok-4.7"}, Chat: up.URL + "/v1"}); err != nil {
			t.Fatal(err)
		}
		body := askGrok(t, "/v1/responses", codexSearchTurn("relay/grok-4.7", stream))
		limits := searchCallLimits(t, body)
		if len(limits) == 0 {
			t.Fatalf("stream %v: no tool_search_call: %s", stream, body)
		}
		for _, l := range limits {
			if l != "" && l != "8" {
				t.Fatalf("stream %v: tool_search_call limit %s, want 8: %s", stream, l, body)
			}
		}
		up.Close()
	}
}

// searchCallArgs keeps every other number as the model wrote it.
func TestSearchCallArgsOnlyWholeZeros(t *testing.T) {
	for in, want := range map[string]string{
		`{"query":"a 8.0 b","limit":8.0}`: `{"query":"a 8.0 b","limit":8}`,
		`{"query":"x","limit":2.5}`:       `{"query":"x","limit":2.5}`,
		`{"query":"x","limit":8}`:         `{"query":"x","limit":8}`,
		`{"query":"x"}`:                   `{"query":"x"}`,
		``:                                `{}`,
	} {
		if got := string(searchCallArgs(in)); got != want {
			t.Errorf("searchCallArgs(%s) = %s, want %s", in, got, want)
		}
	}
}
