package gateway

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Grok's models write an integer in a tool call's arguments as an integral
// float (yield_time_ms 2500.0, session_id 17277.0), whoever serves them: a
// Grok subscription, its plugin, xAI's API or a relay in front of any of
// them. Codex reads such fields as u64, usize or i32 and turns the whole
// call away ("failed to parse function arguments: invalid type: floating
// point `2500.0`, expected u64"), so a Codex turn on grok-4.6 or grok-4.7
// stopped at its first command (reproduced 11 of 11 through a custom
// OpenAI-compatible relay provider, 2026-10-09; the rollouts are in
// testdata/grok_integral_args). In a Responses reply from a Grok model,
// such a float is written as the integer it is where the tool takes one.

// grokModel is whether id, the model as its upstream is asked for it, is a
// Grok model, under whatever vendor prefix a relay or router gives it
// (xai/grok-4.5, x-ai/grok-4).
func grokModel(id string) bool {
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		id = id[i+1:]
	}
	return strings.HasPrefix(strings.ToLower(id), "grok")
}

// intFields are the properties of a tool's arguments, or of an object in
// them, that hold an integer or an object with such properties.
type intFields map[string]*intField

type intField struct {
	whole bool      // the value is an integer
	props intFields // an object's own
}

// codexInts are the fields Codex's own tools declare as number in their
// schema but parse as integers (u64, usize, i32), by the tool's name.
// wait's max_tokens is code mode's (testdata/context/codex.json).
var codexInts = map[string]intFields{
	"exec_command": wholes("yield_time_ms", "max_output_tokens"),
	"write_stdin":  wholes("session_id", "yield_time_ms", "max_output_tokens"),
	"wait":         wholes("yield_time_ms", "max_output_tokens", "max_tokens"),
}

func wholes(names ...string) intFields {
	f := intFields{}
	for _, n := range names {
		f[n] = &intField{whole: true}
	}
	return f
}

// toolInts is, for a reply from a Grok model, the integer fields of each
// tool the request offered, by the name the model calls it under.
type toolInts struct {
	schema map[string]intFields
}

// intsOf is the integer fields of a translated request's tools.
func intsOf(tools []Tool) *toolInts {
	t := &toolInts{schema: map[string]intFields{}}
	for _, tl := range tools {
		if f := schemaInts(tl.Schema); f != nil {
			t.schema[tl.Name] = f
		}
	}
	return t
}

// intsIn is the integer fields of the tools a Responses request offers, a
// namespace's by the flat name it goes to Grok under (namespacedIn).
func intsIn(body []byte) *toolInts {
	t := &toolInts{schema: map[string]intFields{}}
	var q struct {
		Tools []rTool `json:"tools"`
	}
	json.Unmarshal(body, &q)
	for _, tl := range q.Tools {
		switch tl.Type {
		case "function":
			if f := schemaInts(tl.Parameters); f != nil {
				t.schema[tl.Name] = f
			}
		case "namespace":
			for _, nt := range tl.Tools {
				if f := schemaInts(nt.Parameters); nt.Type == "function" && f != nil {
					t.schema[callKey(nt.Name, tl.Name)] = f
				}
			}
		}
	}
	return t
}

// callKey is the name a call to name in namespace is looked up under.
func callKey(name, namespace string) string {
	if namespace == "" || namespace == liteNamespace {
		return name
	}
	return flatName(namespace, name)
}

// intSchema is as much of a JSON schema as says where its integers are.
type intSchema struct {
	Type       json.RawMessage      `json:"type"`
	Properties map[string]intSchema `json:"properties"`
}

func schemaInts(schema json.RawMessage) intFields {
	var s intSchema
	if len(schema) == 0 || json.Unmarshal(schema, &s) != nil {
		return nil
	}
	return s.fields()
}

func (s intSchema) fields() intFields {
	var out intFields
	for k, p := range s.Properties {
		f := &intField{whole: p.integer(), props: p.fields()}
		if f.whole || f.props != nil {
			if out == nil {
				out = intFields{}
			}
			out[k] = f
		}
	}
	return out
}

// integer is whether the schema's type is "integer", or a list with it.
func (s intSchema) integer() bool {
	var one string
	if json.Unmarshal(s.Type, &one) == nil {
		return one == "integer"
	}
	var many []string
	return json.Unmarshal(s.Type, &many) == nil && slices.Contains(many, "integer")
}

// of is the integer fields of a call to name, in namespace when the call
// carries one: the tool's schema's, and Codex's own for its tools.
func (t *toolInts) of(name, namespace string) intFields {
	f := t.schema[callKey(name, namespace)]
	base := name
	if namespace == "" {
		// a namespace's function, called under its flat name
		if i := strings.LastIndex(name, "__"); i >= 0 {
			base = name[i+2:]
		}
	}
	known := codexInts[base]
	if known == nil {
		return f
	}
	if f == nil {
		return known
	}
	out := maps.Clone(f)
	for k, v := range known {
		if have := out[k]; have == nil {
			out[k] = v
		} else if !have.whole {
			out[k] = &intField{whole: true, props: have.props}
		}
	}
	return out
}

// args is a call's arguments with each integral float in an integer field
// written as the integer, and whether one was. t nil is a model that isn't
// Grok's, whose arguments are left as they are.
func (t *toolInts) args(args, name, namespace string) (string, bool) {
	if t == nil {
		return args, false
	}
	return wholeArgs(args, t.of(name, namespace))
}

// wholeArgs is args, a JSON object, with each number in a field of f that
// integral takes rewritten in place; everything else is left byte for
// byte. Arguments that aren't whole JSON are left as they are.
func wholeArgs(args string, f intFields) (string, bool) {
	if len(f) == 0 || !strings.ContainsAny(args, ".eE") || !json.Valid([]byte(args)) {
		return args, false
	}
	type cut struct {
		at, end int
		lit     string
	}
	var cuts []cut
	dec := json.NewDecoder(strings.NewReader(args))
	dec.UseNumber()
	var value func(at *intField) error
	value = func(at *intField) error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch v := tok.(type) {
		case json.Delim:
			if v != '{' && v != '[' {
				return nil
			}
			var props intFields
			if at != nil && v == '{' {
				props = at.props
			}
			for dec.More() {
				var sub *intField
				if v == '{' {
					k, err := dec.Token()
					if err != nil {
						return err
					}
					key, _ := k.(string)
					sub = props[key]
				}
				if err := value(sub); err != nil {
					return err
				}
			}
			_, err := dec.Token() // its end
			return err
		case json.Number:
			if at != nil && at.whole {
				if lit, ok := integral(string(v)); ok {
					end := int(dec.InputOffset())
					cuts = append(cuts, cut{end - len(v), end, lit})
				}
			}
		}
		return nil
	}
	if value(&intField{props: f}) != nil || len(cuts) == 0 {
		return args, false
	}
	var b strings.Builder
	last := 0
	for _, c := range cuts {
		b.WriteString(args[last:c.at])
		b.WriteString(c.lit)
		last = c.end
	}
	b.WriteString(args[last:])
	return b.String(), true
}

// maxExact is the largest integer a float64 holds exactly (2^53-1); one
// past it was rounded before the model's float was written, if it is one.
const maxExact = 1<<53 - 1

// integral is a JSON number literal written with a fraction or an exponent
// (2500.0, 1.2e3) as the integer literal it equals, and whether it is
// one: an exact integer no larger than maxExact.
func integral(lit string) (string, bool) {
	s, neg := strings.CutPrefix(lit, "-")
	mant, exp, hasExp := s, "", false
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		mant, exp, hasExp = s[:i], s[i+1:], true
	}
	whole, frac, dot := strings.Cut(mant, ".")
	if !dot && !hasExp {
		return lit, false // an integer already
	}
	e := 0
	if hasExp {
		// bounded, so the sums below can't overflow
		n, err := strconv.Atoi(exp)
		if err != nil || n > 1000 || n < -1000 {
			return lit, false
		}
		e = n
	}
	// the value is digits × 10^e
	digits := strings.TrimLeft(whole+frac, "0")
	e -= len(frac)
	t := strings.TrimRight(digits, "0")
	e += len(digits) - len(t)
	digits = t
	if digits == "" {
		return "0", true
	}
	if e < 0 || len(digits)+e > len(strconv.Itoa(maxExact)) {
		return lit, false
	}
	n, err := strconv.ParseInt(digits+strings.Repeat("0", e), 10, 64)
	if err != nil || n > maxExact {
		return lit, false
	}
	if neg {
		n = -n
	}
	return strconv.FormatInt(n, 10), true
}

// intTidy writes, in a relayed Responses reply from a Grok model, the
// integral floats in its function calls' arguments as integers (see
// above). A stream's lines pass as they came unless one carries a call
// whose arguments change; a reply that isn't one is held whole and gone
// through at the end. Argument deltas are left as they came: Codex reads
// a call's arguments from its done events.
type intTidy struct {
	ints  *toolInts
	sse   bool
	buf   []byte
	calls map[string][2]string // a streamed call's name and namespace, by its item id
}

func (t *intTidy) write(b []byte) []byte {
	t.buf = append(t.buf, b...)
	if !t.sse {
		return nil
	}
	i := bytes.LastIndexByte(t.buf, '\n')
	if i < 0 {
		return nil
	}
	out := t.lines(t.buf[:i+1])
	t.buf = append(t.buf[:0], t.buf[i+1:]...)
	return out
}

func (t *intTidy) flush() []byte {
	b := t.buf
	t.buf = nil
	if !t.sse {
		if out, ok := t.restore(b); ok {
			return out
		}
		return b
	}
	return t.lines(b)
}

func (t *intTidy) lines(b []byte) []byte {
	if !bytes.Contains(b, []byte(`function_call`)) {
		return append([]byte(nil), b...)
	}
	var out []byte
	for len(b) > 0 {
		line := b
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			line, b = b[:i+1], b[i+1:]
		} else {
			b = nil
		}
		body := bytes.TrimRight(line, "\r\n")
		data, ok := bytes.CutPrefix(body, []byte("data:"))
		if !ok || !bytes.Contains(data, []byte(`function_call`)) {
			out = append(out, line...)
			continue
		}
		nb, ok := t.restore(data)
		if !ok {
			out = append(out, line...)
			continue
		}
		out = append(append(append(out, "data: "...), nb...), line[len(body):]...)
	}
	return out
}

// restore is an event, or a whole reply, with its calls' integers written
// as integers, and whether one was.
func (t *intTidy) restore(data []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var ev map[string]any
	if dec.Decode(&ev) != nil || ev == nil {
		return nil, false
	}
	changed := false
	if it, _ := ev["item"].(map[string]any); t.call(it) {
		changed = true
	}
	// the arguments' done event names its call by item id alone
	if ev["type"] == "response.function_call_arguments.done" {
		id, _ := ev["item_id"].(string)
		named := t.calls[id]
		if name, _ := ev["name"].(string); name != "" {
			ns, _ := ev["namespace"].(string)
			named = [2]string{name, ns}
		}
		if args, ok := ev["arguments"].(string); ok && named[0] != "" {
			if nb, ok := t.ints.args(args, named[0], named[1]); ok {
				ev["arguments"], changed = nb, true
			}
		}
	}
	res, _ := ev["response"].(map[string]any)
	if res == nil && ev["object"] == "response" {
		res = ev
	}
	if res != nil {
		out, _ := res["output"].([]any)
		for _, it := range out {
			if im, _ := it.(map[string]any); t.call(im) {
				changed = true
			}
		}
	}
	if !changed {
		return nil, false
	}
	nb, err := marshalPlain(ev)
	if err != nil {
		return nil, false
	}
	return nb, true
}

// call writes a function_call's integers as integers, remembering whom a
// streamed one is to, and reports whether it changed.
func (t *intTidy) call(it map[string]any) bool {
	if it == nil || it["type"] != "function_call" {
		return false
	}
	name, _ := it["name"].(string)
	ns, _ := it["namespace"].(string)
	if id, _ := it["id"].(string); id != "" && name != "" {
		if t.calls == nil {
			t.calls = map[string][2]string{}
		}
		t.calls[id] = [2]string{name, ns}
	}
	args, _ := it["arguments"].(string)
	nb, ok := t.ints.args(args, name, ns)
	if ok {
		it["arguments"] = nb
	}
	return ok
}
