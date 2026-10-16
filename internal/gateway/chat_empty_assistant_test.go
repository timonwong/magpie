package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// strictRefuseEmptyAssistant answers as DeepSeek's Chat API does (and Kimi's,
// and the relays in front of them): an assistant message with neither
// content nor tool_calls turns the whole request away, reasoning_content or
// not. An agent interrupted while the model was still thinking keeps such a
// turn in its history and sends it again on every later request, so the
// session is stuck (hope-agent#779, charmbracelet/crush#3794, ccx#35).
func strictRefuseEmptyAssistant(body []byte) (int, string) {
	var q struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	json.Unmarshal(body, &q)
	for _, m := range q.Messages {
		if string(m["role"]) != `"assistant"` {
			continue
		}
		var calls []json.RawMessage
		json.Unmarshal(m["tool_calls"], &calls)
		if len(calls) > 0 {
			continue
		}
		var s string
		var parts []json.RawMessage
		if json.Unmarshal(m["content"], &s) == nil && s != "" ||
			json.Unmarshal(m["content"], &parts) == nil && len(parts) > 0 {
			continue
		}
		return 400, `{"error":{"message":"Invalid assistant message: content or tool_calls must be set","type":"invalid_request_error","param":null,"code":"invalid_request_error"}}`
	}
	return 0, ""
}

const chatStreamOkReply = `data: {"id":"c1","object":"chat.completion.chunk","model":"m1","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}` + "\n\ndata: [DONE]\n\n"

// noBareAssistant fails the test when a sent message is an assistant one
// with nothing a strict upstream counts.
func noBareAssistant(t *testing.T, ms []json.RawMessage) {
	t.Helper()
	for i, raw := range ms {
		var m map[string]any
		json.Unmarshal(raw, &m)
		if m["role"] == "assistant" && bareAssistantAny(m) {
			t.Errorf("message %d is an assistant message with no content or tool calls: %s", i, raw)
		}
	}
}

func bareAssistantAny(m map[string]any) bool {
	if calls, _ := m["tool_calls"].([]any); len(calls) > 0 {
		return false
	}
	switch c := m["content"].(type) {
	case string:
		return strings.TrimSpace(c) == ""
	case []any:
		return len(c) == 0
	}
	return true
}

// Claude Code stopped while the model was thinking keeps the turn with its
// thinking alone (or with an empty text block, or nothing); sent on to a
// Chat upstream that turn has nothing in it a strict one takes, and every
// later request of the session was refused 400. It is left out, and the
// user messages either side of it go as one, so no two stand in a row.
// The turns that have text or calls go as before, the call's thinking
// with it for DeepSeek.
func TestChatEmptyAssistantLeftOutFromAnthropic(t *testing.T) {
	for _, c := range []struct{ name, turn string }{
		{"thinking", `[{"type":"thinking","thinking":"Let me plan the essay.","signature":"sig"}]`},
		{"redacted", `[{"type":"redacted_thinking","data":"c2VhbGVk"}]`},
		{"empty-text", `[{"type":"text","text":""}]`},
		{"thinking-and-empty-text", `[{"type":"thinking","thinking":"Let me plan.","signature":"sig"},{"type":"text","text":""}]`},
		{"no-blocks", `[]`},
		{"empty-string", `""`},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fake{t: t, refuse: strictRefuseEmptyAssistant, reply: chatStreamOkReply}
			up := setup(t, provider.Chat, f)
			chatProvider(t, up.URL, provider.Provider{ID: "ds", Models: []string{"deepseek-v4.1-flash"}})
			code, body := postTo(t, New(), "/v1/messages", `{"model":"ds/deepseek-v4.1-flash","max_tokens":1024,"stream":false,"tools":[{"name":"get_time","input_schema":{"type":"object","properties":{}}}],"messages":[
				{"role":"user","content":[{"type":"text","text":"what time is it?"}]},
				{"role":"assistant","content":[{"type":"thinking","thinking":"I should call get_time.","signature":"s1"},{"type":"tool_use","id":"call_1","name":"get_time","input":{}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"12:00"}]},
				{"role":"assistant","content":[{"type":"text","text":"It is 12:00."}]},
				{"role":"user","content":[{"type":"text","text":"write a long essay"}]},
				{"role":"assistant","content":`+c.turn+`},
				{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"},{"type":"text","text":"keep it short"}]}]}`)
			if code != 200 || f.calls != 1 {
				t.Fatalf("%d after %d calls: %s", code, f.calls, body)
			}
			ms := sentMessages(t, f.got)
			noBareAssistant(t, ms)
			var roles []string
			for _, raw := range ms {
				var m map[string]any
				json.Unmarshal(raw, &m)
				roles = append(roles, m["role"].(string))
			}
			if got := strings.Join(roles, " "); got != "user assistant tool assistant user" {
				t.Fatalf("roles = %q: %s", got, f.got)
			}
			var last map[string]any
			json.Unmarshal(ms[4], &last)
			text, _ := last["content"].(string)
			if !strings.Contains(text, "write a long essay") || !strings.Contains(text, "keep it short") {
				t.Errorf("the user messages either side were not joined: %s", ms[4])
			}
			var call map[string]any
			json.Unmarshal(ms[1], &call)
			if call["reasoning_content"] != "I should call get_time." {
				t.Errorf("the tool call lost its thinking: %s", ms[1])
			}
		})
	}
}

// Codex stopped while the model was thinking sends the reasoning item on
// its own, the user's next message after it (ccx#35's case). Translated to
// Chat it was an assistant message with no content; it is left out.
func TestChatEmptyAssistantLeftOutFromResponses(t *testing.T) {
	f := &fake{t: t, refuse: strictRefuseEmptyAssistant, reply: chatStreamOkReply}
	up := setup(t, provider.Chat, f)
	chatProvider(t, up.URL, provider.Provider{ID: "ds", Models: []string{"deepseek-v4.1-flash"}})
	code, body := postTo(t, New(), "/v1/responses", `{"model":"ds/deepseek-v4.1-flash","stream":false,"input":[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"write a long essay"}]},
		{"type":"reasoning","summary":[{"type":"summary_text","text":"Planning the essay."}]},
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":""}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"keep it short"}]}]}`)
	if code != 200 || f.calls != 1 {
		t.Fatalf("%d after %d calls: %s", code, f.calls, body)
	}
	ms := sentMessages(t, f.got)
	noBareAssistant(t, ms)
	if len(ms) != 1 {
		t.Fatalf("sent %d messages, want the one user message: %s", len(ms), f.got)
	}
}

// Inside a tool exchange the turn was left out already when it had no
// reasoning; with DeepSeek's reasoning_content on it, it went between the
// calls and their results, which a strict upstream refuses as well.
func TestChatThinkingOnlyAssistantInsideExchangeForDeepSeek(t *testing.T) {
	r := &Request{Messages: []Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "run"}}},
		{Role: "assistant", Parts: []Part{call("A")}},
		{Role: "assistant", Parts: []Part{{Kind: Thinking, Text: "hmm", Signature: "sig"}}},
		{Role: "user", Parts: []Part{result("A", "done")}},
	}}
	var q struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(buildChat(r, "deepseek-v4.1-flash", "api.deepseek.com", false), &q); err != nil {
		t.Fatal(err)
	}
	var roles []string
	for _, m := range q.Messages {
		roles = append(roles, m["role"].(string))
		if m["role"] == "assistant" && bareAssistantAny(m) {
			t.Errorf("bare assistant message sent: %v", m)
		}
	}
	if got := strings.Join(roles, " "); got != "user assistant tool" {
		t.Errorf("roles = %q", got)
	}
}

// A reply magpie cut short and sends back to go on from (continuation.go)
// is the prefill itself, its text empty when it was cut while thinking:
// it stays, the upstream's prefill mode on it.
func TestChatResumePrefillKeptWhenEmpty(t *testing.T) {
	r := &Request{Resume: true, Messages: []Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "write"}}},
		{Role: "assistant", Parts: []Part{{Kind: Thinking, Text: "Planning"}}},
	}}
	var q struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(buildChat(r, "deepseek-v4.1-flash", "api.deepseek.com", false), &q); err != nil {
		t.Fatal(err)
	}
	if len(q.Messages) != 2 || q.Messages[1]["prefix"] != true {
		t.Errorf("the prefill went: %v", q.Messages)
	}
}

// A Chat client's own history (OpenCode, Kimi Code, any agent on Chat
// Completions) with an interrupted turn in it was relayed as it came, so
// the strict upstream refused it. Each assistant message with no content
// and no tool calls is left out, for every Chat upstream, and the users
// either side go as one; every other message keeps its bytes.
func TestChatRelayLeavesOutEmptyAssistant(t *testing.T) {
	f := &fake{t: t, reply: chatWholeOK, ctype: "application/json", refuse: strictRefuseEmptyAssistant}
	up := setup(t, provider.Chat, f)
	chatProvider(t, up.URL, provider.Provider{ID: "ds", Models: []string{"deepseek-v4.1-flash", "glm-5.2"}})
	srv := New()
	for _, model := range []string{"ds/deepseek-v4.1-flash", "ds/glm-5.2"} {
		f.calls = 0
		code, body := postTo(t, srv, "/v1/chat/completions", `{"model":"`+model+`","stream":false,"tools":[{"type":"function","function":{"name":"get_time","parameters":{"type":"object","properties":{}}}}],"messages":[
			{"role":"user","content":"what time is it?"},
			{"role":"assistant","content":null,"tool_calls":[`+timeCall+`],"reasoning_content":"I should call get_time."},
			{"role":"tool","tool_call_id":"call_1","content":"12:00"},
			{"role":"assistant","content":"It is 12:00."},
			{"role":"user","content":"write a long essay"},
			{"role":"assistant","content":"","reasoning_content":"Planning the essay."},
			{"role":"user","content":"keep it short"},
			{"role":"assistant","content":null},
			{"role":"assistant","content":[]},
			{"role":"user","content":[{"type":"text","text":"still there?"}]}]}`)
		if code != 200 || f.calls != 1 {
			t.Fatalf("%s: %d after %d calls: %s", model, code, f.calls, body)
		}
		ms := sentMessages(t, f.got)
		noBareAssistant(t, ms)
		if len(ms) != 5 {
			t.Fatalf("%s: sent %d messages, want 5: %s", model, len(ms), f.got)
		}
		sameJSON(t, ms[0], `{"role":"user","content":"what time is it?"}`)
		sameJSON(t, ms[2], `{"role":"tool","tool_call_id":"call_1","content":"12:00"}`)
		var reply map[string]any
		json.Unmarshal(ms[3], &reply)
		if reply["content"] != "It is 12:00." {
			t.Errorf("%s: the reply changed: %s", model, ms[3])
		}
		sameJSON(t, ms[4], `{"role":"user","content":[{"type":"text","text":"write a long essay\n\nkeep it short"},{"type":"text","text":"still there?"}]}`)
	}
}

// A body with no empty assistant message is relayed byte for byte.
func TestChatRelayWithoutEmptyAssistantUnchanged(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"assistant",  "content":"Hello!"},{"role":"user","content":"again"}]}`)
	if got := withoutEmptyAssistants(body); string(got) != string(body) {
		t.Errorf("body changed:\n%s\nwant\n%s", got, body)
	}
}
