package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// codexShellTools are Codex's exec_command and write_stdin as it offers
// them, their integer fields declared number (Codex reads them as u64,
// usize and i32), and a third-party tool that declares its own integers.
const codexShellTools = `"tools":[
	{"type":"function","name":"exec_command","description":"Runs a command in a PTY, returning output or a session ID for ongoing interaction.","strict":false,"parameters":{"type":"object","properties":{
		"cmd":{"type":"string","description":"Shell command to execute."},
		"workdir":{"type":"string","description":"Optional working directory to run the command in; defaults to the turn cwd."},
		"shell":{"type":"string","description":"Shell binary to launch. Defaults to the user's default shell."},
		"login":{"type":"boolean","description":"Whether to run the shell with -l/-i semantics. Defaults to true."},
		"yield_time_ms":{"type":"number","description":"How long to wait (in milliseconds) for output before yielding."},
		"max_output_tokens":{"type":"number","description":"Maximum number of tokens to return. Excess output will be truncated."}},
		"required":["cmd"],"additionalProperties":false}},
	{"type":"function","name":"write_stdin","description":"Writes characters to an existing unified exec session and returns recent output.","strict":false,"parameters":{"type":"object","properties":{
		"session_id":{"type":"number","description":"Identifier of the running unified exec session."},
		"chars":{"type":"string","description":"Bytes to write to stdin (may be empty to poll)."},
		"yield_time_ms":{"type":"number","description":"How long to wait (in milliseconds) for output before yielding."},
		"max_output_tokens":{"type":"number","description":"Maximum number of tokens to return. Excess output will be truncated."}},
		"required":["session_id"],"additionalProperties":false}},
	{"type":"function","name":"count_things","description":"Counts things.","parameters":{"type":"object","properties":{
		"count":{"type":"integer"},
		"ratio":{"type":"number"},
		"opts":{"type":"object","properties":{"depth":{"type":["integer","null"]}}}}}}]`

// codexShellTurn is a Codex turn asking model, with codexShellTools.
func codexShellTurn(model string, stream bool) string {
	return `{"model":"` + model + `","instructions":"You are Codex.","stream":` + strconv.FormatBool(stream) + `,"store":false,
		"include":["reasoning.encrypted_content"],"tool_choice":"auto","parallel_tool_calls":true,` + codexShellTools + `,
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"for i in 1..5 echo i, sleep 2"}]}]}`
}

// rolloutCall is a function_call Codex recorded in its rollout and the
// output it gave the model for it.
type rolloutCall struct {
	Name, Arguments, Output string
}

// rollout reads a case from testdata/grok_integral_args: real Codex rollout
// lines, a function_call and the function_call_output of its call_id.
func rollout(t *testing.T, file string) rolloutCall {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "grok_integral_args", file))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	type payload struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		CallID    string `json:"call_id"`
		Output    string `json:"output"`
	}
	var calls, outputs []payload
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var line struct {
			Payload payload `json:"payload"`
		}
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		switch line.Payload.Type {
		case "function_call":
			calls = append(calls, line.Payload)
		case "function_call_output":
			outputs = append(outputs, line.Payload)
		}
	}
	if len(calls) != 1 || len(outputs) != 1 || calls[0].CallID != outputs[0].CallID {
		t.Fatalf("%s: want one call and its output, got %v %v", file, calls, outputs)
	}
	return rolloutCall{Name: calls[0].Name, Arguments: calls[0].Arguments, Output: outputs[0].Output}
}

// serdeFloat is the error Codex gave a call whose integer came as a float.
var serdeFloat = regexp.MustCompile("invalid type: floating point `([^`]+)`, expected (\\w+) at line 1 column (\\d+)")

// refusedField is the field of args that Codex's error names: the number
// written as the error quotes it and ending at the column it gives, with
// the Rust type Codex wanted.
func refusedField(t *testing.T, args, output string) (field, want string) {
	t.Helper()
	m := serdeFloat.FindStringSubmatch(output)
	if m == nil {
		t.Fatalf("not a float refused: %s", output)
	}
	col, _ := strconv.Atoi(m[3])
	dec := json.NewDecoder(strings.NewReader(args))
	dec.UseNumber()
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("arguments %s", args)
	}
	for dec.More() {
		k, _ := dec.Token()
		v, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		if n, ok := v.(json.Number); ok && string(n) == m[1] && int(dec.InputOffset()) == col {
			return k.(string), m[2]
		}
	}
	t.Fatalf("no field of %s is %s at column %d", args, m[1], col)
	return "", ""
}

// takes is whether Rust's serde reads lit as typ.
func takes(lit, typ string) bool {
	var err error
	switch typ {
	case "u64", "usize":
		_, err = strconv.ParseUint(lit, 10, 64)
	case "i32":
		_, err = strconv.ParseInt(lit, 10, 32)
	case "i64":
		_, err = strconv.ParseInt(lit, 10, 64)
	default:
		return false
	}
	return err == nil
}

// fieldsOf is each value of a call's arguments as written: a number as its
// literal, a string quoted, an object's fields under its name and a dot.
func fieldsOf(t *testing.T, args string) map[string]string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(args))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("%v: %s", err, args)
	}
	out := map[string]string{}
	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		for k, v := range m {
			switch v := v.(type) {
			case json.Number:
				out[prefix+k] = string(v)
			case string:
				out[prefix+k] = strconv.Quote(v)
			case map[string]any:
				walk(prefix+k+".", v)
			default:
				b, _ := json.Marshal(v)
				out[prefix+k] = string(b)
			}
		}
	}
	walk("", m)
	return out
}

// argsRead is the arguments of every function_call Codex reads in a
// Responses reply, by where it read them: the stream's
// function_call_arguments.delta and .done, output_item.done and
// response.completed, or a whole reply's output.
func argsRead(t *testing.T, body []byte, stream bool) map[string][]string {
	t.Helper()
	read := map[string][]string{}
	calls := func(where string, items []map[string]any) {
		for _, it := range items {
			if it["type"] == "function_call" {
				a, _ := it["arguments"].(string)
				read[where] = append(read[where], a)
			}
		}
	}
	if !stream {
		var res struct {
			Output []map[string]any `json:"output"`
		}
		if err := json.Unmarshal(body, &res); err != nil {
			t.Fatalf("%v: %s", err, body)
		}
		calls("output", res.Output)
		return read
	}
	for _, line := range strings.Split(string(body), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var ev struct {
			Type      string         `json:"type"`
			Delta     string         `json:"delta"`
			Arguments string         `json:"arguments"`
			Item      map[string]any `json:"item"`
			Response  struct {
				Output []map[string]any `json:"output"`
			} `json:"response"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			t.Fatalf("%v: %s", err, data)
		}
		switch ev.Type {
		case "response.function_call_arguments.delta":
			read["delta"] = append(read["delta"], ev.Delta)
		case "response.function_call_arguments.done":
			read["arguments.done"] = append(read["arguments.done"], ev.Arguments)
		case "response.output_item.done":
			calls("output_item.done", []map[string]any{ev.Item})
		case "response.completed":
			calls("completed", ev.Response.Output)
		}
	}
	return read
}

// responsesCall is a Responses upstream answering every request with one
// call to name with args, in a stream as Grok's relay sends it (the
// arguments' done event naming no tool) or whole.
func responsesCall(name, args string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var q map[string]any
		json.Unmarshal(b, &q)
		quoted, _ := json.Marshal(args)
		call := `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"` + name + `","arguments":` + string(quoted) + `,"status":"completed"}`
		done := `{"id":"resp_1","object":"response","status":"completed","model":"grok-4.6","output":[` + call + `],"usage":{"input_tokens":5,"output_tokens":3}}`
		if q["stream"] != true {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, done)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: response.created`+"\n"+`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","status":"in_progress","output":[]}}`,
			`event: response.output_item.added`+"\n"+`data: {"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"`+name+`","arguments":"","status":"in_progress"}}`,
			`event: response.function_call_arguments.delta`+"\n"+`data: {"type":"response.function_call_arguments.delta","sequence_number":2,"item_id":"fc_1","output_index":0,"delta":`+string(quoted)+`}`,
			`event: response.function_call_arguments.done`+"\n"+`data: {"type":"response.function_call_arguments.done","sequence_number":3,"item_id":"fc_1","output_index":0,"arguments":`+string(quoted)+`}`,
			`event: response.output_item.done`+"\n"+`data: {"type":"response.output_item.done","sequence_number":4,"output_index":0,"item":`+call+`}`,
			`event: response.completed`+"\n"+`data: {"type":"response.completed","sequence_number":5,"response":`+done+`}`))
	}
}

// chatCall is a Chat upstream streaming one call to name with args.
func chatCall(name, args string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		quoted, _ := json.Marshal(args)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","model":"grok-4.6","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"`+name+`","arguments":""}}]}}]}`,
			`data: {"id":"c1","model":"grok-4.6","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":`+string(quoted)+`}}]}}]}`,
			`data: {"id":"c1","model":"grok-4.6","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			`data: {"id":"c1","model":"grok-4.6","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
			`data: [DONE]`))
	}
}

// integralCase is a call Grok makes and what Codex is to read in it.
type integralCase struct {
	name, tool, args string
	want             map[string]string // fields of the arguments Codex reads
	refused          string            // Codex's error over args, for a recorded case
}

// integralCases are the recorded calls, from testdata/grok_integral_args,
// and the rule's edges.
func integralCases(t *testing.T) []integralCase {
	var cs []integralCase
	for _, rc := range []struct {
		file string
		want map[string]string
	}{
		{"grok_4_6_exec_command.jsonl", map[string]string{"cmd": `"for i in 1 2 3 4 5; do echo $i; sleep 2; done"`, "yield_time_ms": "2500", "max_output_tokens": "4000"}},
		{"grok_4_7_exec_command.jsonl", map[string]string{"cmd": `"for i in 1 2 3 4 5; do echo $i; sleep 2; done"`, "yield_time_ms": "2500", "max_output_tokens": "4000"}},
		{"grok_4_6_write_stdin.jsonl", map[string]string{"session_id": "17277", "chars": `""`, "yield_time_ms": "120000"}},
	} {
		c := rollout(t, rc.file)
		cs = append(cs, integralCase{name: rc.file, tool: c.Name, args: c.Arguments, want: rc.want, refused: c.Output})
	}
	return append(cs,
		// the tool's own schema: integer, also nested and in a type list;
		// number stays as written
		integralCase{name: "schema integer", tool: "count_things", args: `{"count":3.0,"ratio":3.0,"opts":{"depth":2.0}}`,
			want: map[string]string{"count": "3", "ratio": "3.0", "opts.depth": "2"}},
		integralCase{name: "exponent", tool: "exec_command", args: `{"cmd":"ls","yield_time_ms":1.2e3,"max_output_tokens":4000}`,
			want: map[string]string{"cmd": `"ls"`, "yield_time_ms": "1200", "max_output_tokens": "4000"}},
		integralCase{name: "a fraction and a string", tool: "exec_command", args: `{"cmd":"ls","yield_time_ms":2500.5,"max_output_tokens":"2500.0"}`,
			want: map[string]string{"cmd": `"ls"`, "yield_time_ms": "2500.5", "max_output_tokens": `"2500.0"`}},
		integralCase{name: "out of range", tool: "exec_command", args: `{"cmd":"ls","yield_time_ms":1e300,"max_output_tokens":9007199254740993.0}`,
			want: map[string]string{"cmd": `"ls"`, "yield_time_ms": "1e300", "max_output_tokens": "9007199254740993.0"}},
		integralCase{name: "a field Codex doesn't read as an integer", tool: "exec_command", args: `{"cmd":"ls","yield_time_ms":2500.0,"timeout":5.0}`,
			want: map[string]string{"cmd": `"ls"`, "yield_time_ms": "2500", "timeout": "5.0"}},
	)
}

// checkIntegral checks every function_call Codex reads in a reply to c.
func checkIntegral(t *testing.T, c integralCase, body []byte, stream bool) {
	t.Helper()
	read := argsRead(t, body, stream)
	places := []string{"output"}
	if stream {
		places = []string{"arguments.done", "output_item.done", "completed"}
		// a delta is part of the arguments, left as Grok wrote it
		if d := read["delta"]; len(d) != 1 || d[0] != c.args {
			t.Fatalf("deltas %q, want %q", d, c.args)
		}
	}
	for _, where := range places {
		got := read[where]
		if len(got) != 1 {
			t.Fatalf("%s: %d calls read: %s", where, len(got), body)
		}
		fields := fieldsOf(t, got[0])
		if len(fields) != len(c.want) {
			t.Fatalf("%s: arguments %s, want %v", where, got[0], c.want)
		}
		for k, v := range c.want {
			if fields[k] != v {
				t.Fatalf("%s: %s is %s in %s, want %s", where, k, fields[k], got[0], v)
			}
		}
		// what Codex refused it over it now takes
		if c.refused != "" {
			field, typ := refusedField(t, c.args, c.refused)
			if !takes(fields[field], typ) {
				t.Fatalf("%s: Codex reads %s as %s and is given %s", where, field, typ, fields[field])
			}
		}
	}
}

// A Grok model writes integers in a tool call's arguments as integral
// floats (2500.0), which Codex refuses ("invalid type: floating point
// `2500.0`, expected u64"), reproduced through a custom provider in front
// of grok-4.6 and grok-4.7. Relayed as is from a relay's Responses API,
// streamed or not, Codex reads them as integers where the tool's schema or
// Codex itself takes an integer, and nowhere else.
func TestGrokCallsGiveCodexIntegers(t *testing.T) {
	for _, c := range integralCases(t) {
		for _, stream := range []bool{true, false} {
			t.Run(c.name+map[bool]string{true: " stream", false: ""}[stream], func(t *testing.T) {
				fresh(t)
				up := httptest.NewServer(responsesCall(c.tool, c.args))
				defer up.Close()
				if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"grok-4.6"}, Responses: up.URL + "/v1"}); err != nil {
					t.Fatal(err)
				}
				rec := httptest.NewRecorder()
				New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(codexShellTurn("relay/grok-4.6", stream))))
				if rec.Code != 200 {
					t.Fatalf("%d %s", rec.Code, rec.Body)
				}
				checkIntegral(t, c, rec.Body.Bytes(), stream)
			})
		}
	}
}

// A relay that has only Chat for grok-4.6 is translated for: Codex reads
// the same integers in the Responses reply magpie writes.
func TestGrokCallsGiveCodexIntegersTranslated(t *testing.T) {
	for _, c := range integralCases(t) {
		for _, stream := range []bool{true, false} {
			t.Run(c.name+map[bool]string{true: " stream", false: ""}[stream], func(t *testing.T) {
				fresh(t)
				up := httptest.NewServer(chatCall(c.tool, c.args))
				defer up.Close()
				if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"grok-4.6"}, Chat: up.URL + "/v1"}); err != nil {
					t.Fatal(err)
				}
				rec := httptest.NewRecorder()
				New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(codexShellTurn("relay/grok-4.6", stream))))
				if rec.Code != 200 {
					t.Fatalf("%d %s", rec.Code, rec.Body)
				}
				checkIntegral(t, c, rec.Body.Bytes(), stream)
			})
		}
	}
}

// Another vendor's model is not Grok's: the same call reaches Codex as it
// came, relayed byte for byte, and translated with its floats.
func TestOnlyGrokCallsGetIntegers(t *testing.T) {
	c := rollout(t, "grok_4_6_exec_command.jsonl")
	for _, stream := range []bool{true, false} {
		fresh(t)
		up := httptest.NewServer(responsesCall(c.Name, c.Arguments))
		defer up.Close()
		if err := provider.Save(provider.Provider{ID: "relay", Name: "relay", Key: "k", Models: []string{"gpt-5.5"}, Responses: up.URL + "/v1"}); err != nil {
			t.Fatal(err)
		}
		// what the relay itself answers
		direct := httptest.NewRecorder()
		responsesCall(c.Name, c.Arguments)(direct, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(codexShellTurn("gpt-5.5", stream))))
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(codexShellTurn("relay/gpt-5.5", stream))))
		if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), direct.Body.Bytes()) {
			t.Fatalf("stream %v: %d\n%s\nwant\n%s", stream, rec.Code, rec.Body, direct.Body)
		}

		fresh(t)
		chat := httptest.NewServer(chatCall(c.Name, c.Arguments))
		defer chat.Close()
		if err := provider.Save(provider.Provider{ID: "relay", Name: "relay", Key: "k", Models: []string{"gpt-5.5"}, Chat: chat.URL + "/v1"}); err != nil {
			t.Fatal(err)
		}
		rec = httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(codexShellTurn("relay/gpt-5.5", stream))))
		if rec.Code != 200 {
			t.Fatalf("stream %v: %d %s", stream, rec.Code, rec.Body)
		}
		for where, got := range argsRead(t, rec.Body.Bytes(), stream) {
			if len(got) != 1 || got[0] != c.Arguments {
				t.Fatalf("stream %v: %s translated %q, want %q", stream, where, got, c.Arguments)
			}
		}
	}
}

// grokModel tells Grok's models by the id sent upstream, however a relay
// or a router names their vendor.
func TestGrokModel(t *testing.T) {
	for id, want := range map[string]bool{
		"grok-4.6": true, "grok-4.7": true, "xai/grok-4.5": true, "x-ai/grok-4": true, "relay/grok-4.6": true, "Grok-4": true,
		"gpt-5.5": false, "deepseek-v4-pro": false, "openrouter/x-ai-grok-4": false, "": false,
	} {
		if grokModel(id) != want {
			t.Errorf("grokModel(%q) = %v", id, !want)
		}
	}
}
