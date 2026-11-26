package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// #5840: the run's model is shown the client's tools as mcp__magpie__<name>
// (bridgeName), and a tool name it writes inside another tool's arguments
// has only that name to go by. toolAliases is the map back: each alias to
// the name the client declared, for the tools whose alias differs from
// their name, and never for an alias that is itself a tool the client
// declared.
func TestToolAliasesNameTheClientsTools(t *testing.T) {
	spawn := bridgeName("spawn.agent")
	if spawn == "mcp__magpie__spawn.agent" || !strings.HasPrefix(spawn, "mcp__magpie__spawn_agent_") {
		t.Fatalf("spawn.agent is shown as %q, a hashed name was expected", spawn)
	}
	magpieTool := "mcp__magpie__magpie-browser__browser_click"
	got := toolAliases(map[string]bool{"read": true, "grep": true, "spawn.agent": true, magpieTool: true})
	want := map[string]string{
		"mcp__magpie__read": "read",
		"mcp__magpie__grep": "grep",
		spawn:               "spawn.agent",
	}
	if !maps.Equal(got, want) {
		t.Errorf("aliases %v, want %v", got, want)
	}
	if name, ok := got[bridgeName(magpieTool)]; ok {
		// its alias is its own name: nothing to turn back
		t.Errorf("the magpie server's tool has the alias %q, which names %q", bridgeName(magpieTool), name)
	}

	// with the alias a tool of the client's own, it stays that tool
	got = toolAliases(map[string]bool{"read": true, "mcp__magpie__read": true})
	if name, ok := got["mcp__magpie__read"]; ok {
		t.Errorf("mcp__magpie__read is declared by the client, yet is turned into %q", name)
	}
	if len(got) != 0 {
		t.Errorf("aliases %v, want none", got)
	}
}

// aliasCase is one arguments text and what a run's client is to be given
// of it.
type aliasCase struct{ name, in, want string }

// clipArgs keeps a failure's text readable when its input is long.
func clipArgs(s string) string {
	if len(s) > 300 {
		return s[:150] + "…" + s[len(s)-150:]
	}
	return s
}

// #5840: every JSON string in a call's arguments that is exactly a tool's
// alias comes back as the client's name, whether the arguments are
// handed over whole or stream in pieces cut anywhere, and nothing else in
// them changes by a byte. A string is matched whole wherever it stands,
// so an object key that is an alias is turned too. A string that only
// contains an alias, spells it with an escape, or is longer than any alias
// is left alone: a grep pattern or a pasted log line is the model's own
// text.
func TestAliasRewriterTurnsNestedToolNamesBack(t *testing.T) {
	aliases := toolAliases(map[string]bool{"read": true, "grep": true, "spawn.agent": true, "read_file": true})
	spawn := bridgeName("spawn.agent")
	long := strings.Repeat("x", 10_000)
	keep := func(name, in string) aliasCase { return aliasCase{name, in, in} }
	cases := []aliasCase{
		{"batch of calls",
			`{"tool_calls":[{"tool":"mcp__magpie__read","arguments":{"path":"a"}},{"tool":"mcp__magpie__grep","arguments":{}}]}`,
			`{"tool_calls":[{"tool":"read","arguments":{"path":"a"}},{"tool":"grep","arguments":{}}]}`},
		{"hashed alias",
			`{"tool":"` + spawn + `","arguments":{"n":1}}`,
			`{"tool":"spawn.agent","arguments":{"n":1}}`},
		{"object key",
			`{"mcp__magpie__read":{"path":"a"},"other":1}`,
			`{"read":{"path":"a"},"other":1}`},
		{"allowlist",
			`{"allowed":["mcp__magpie__read","mcp__magpie__grep","Bash"]}`,
			`{"allowed":["read","grep","Bash"]}`},
		{"alias that begins another alias",
			`{"a":"mcp__magpie__read","b":"mcp__magpie__read_file"}`,
			`{"a":"read","b":"read_file"}`},
		{"whole text a string",
			`"mcp__magpie__read"`,
			`"read"`},
		{"spacing kept",
			"{ \"tool\" :\n\t\"mcp__magpie__grep\" , \"n\" : 1 }",
			"{ \"tool\" :\n\t\"grep\" , \"n\" : 1 }"},
		{"alias after a string with an escape",
			`{"a":"mcp__magpie__read\"","b":"mcp__magpie__read"}`,
			`{"a":"mcp__magpie__read\"","b":"read"}`},
		{"alias after an escaped backslash",
			`{"p":"C:\\","q":"mcp__magpie__read"}`,
			`{"p":"C:\\","q":"read"}`},
		{"alias beside an alias-like pattern",
			`{"tool":"mcp__magpie__read","arguments":{"pattern":"mcp__magpie__read x"}}`,
			`{"tool":"read","arguments":{"pattern":"mcp__magpie__read x"}}`},

		keep("string that contains an alias", `{"pattern":"grep mcp__magpie__read in logs"}`),
		keep("alias with a suffix", `{"tool":"mcp__magpie__readx"}`),
		keep("alias with a space after", `{"tool":"mcp__magpie__read "}`),
		keep("alias in other case", `{"tool":"MCP__MAGPIE__READ"}`),
		keep("prefix only", `{"tool":"mcp__magpie__"}`),
		keep("part of an alias", `{"tool":"mcp__magpie__rea"}`),
		keep("alias spelled with an escape", `{"tool":"mcp\u005f_magpie__read"}`),
		keep("alias written twice", `{"tool":"mcp__magpie__readmcp__magpie__read"}`),
		keep("escaped quote before it", `{"a":"a\"mcp__magpie__read"}`),
		keep("escaped quote inside it", `{"a":"mcp__magpie__re\"ad"}`),
		keep("non-ASCII text", `{"text":"héllo wörld 日本語 😀 mcp__magpie__réad","e":"\u00e9"}`),
		keep("long string", `{"data":"`+long+`"}`),
		keep("long string that begins as an alias", `{"data":"mcp__magpie__read`+long+`"}`),
		keep("empty strings", `{"":"","a":[""]}`),
		keep("numbers, booleans and null", `{"n":12,"f":-1.5e3,"b":true,"c":false,"z":null,"arr":[1,2,3],"o":{}}`),
		keep("no strings", `[1,2,{}]`),
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := unaliasArgs(aliases, json.RawMessage(c.in)); got != c.want {
				t.Fatalf("whole: got %s\nwant %s", clipArgs(got), clipArgs(c.want))
			}
			if !json.Valid([]byte(c.want)) {
				t.Fatalf("the case's own want isn't JSON: %s", clipArgs(c.want))
			}
			// the same arguments cut in two anywhere, and in single bytes
			for i := 0; i <= len(c.in); i++ {
				w := newAliasRewriter(aliases)
				got := w.write(c.in[:i]) + w.write(c.in[i:]) + w.flush()
				if got != c.want {
					t.Fatalf("cut at %d of %d: got %s\nwant %s", i, len(c.in), clipArgs(got), clipArgs(c.want))
				}
			}
			w := newAliasRewriter(aliases)
			var got strings.Builder
			for i := range len(c.in) {
				got.WriteString(w.write(c.in[i : i+1]))
			}
			got.WriteString(w.flush())
			if got.String() != c.want {
				t.Fatalf("byte by byte: got %s\nwant %s", clipArgs(got.String()), clipArgs(c.want))
			}
			// a run whose tools all have their own names has nothing to turn
			for _, none := range []map[string]string{nil, {}} {
				if got := unaliasArgs(none, json.RawMessage(c.in)); got != c.in {
					t.Fatalf("no aliases: got %s\nwant %s", clipArgs(got), clipArgs(c.in))
				}
				w := newAliasRewriter(none)
				if got := w.write(c.in) + w.flush(); got != c.in {
					t.Fatalf("no aliases, streamed: got %s\nwant %s", clipArgs(got), clipArgs(c.in))
				}
			}
		})
	}

	// arguments that end inside a string lose nothing, and the arguments
	// stream on as they come: only what may still become an alias waits
	t.Run("unterminated string", func(t *testing.T) {
		const in = `{"tool":"mcp__magpie__re`
		w := newAliasRewriter(aliases)
		out := w.write(in)
		if held := len(in) - len(out); held > len("mcp__magpie__read_file") {
			t.Errorf("%d bytes of %q are held back, more than any alias is long", held, in)
		}
		if got := out + w.flush(); got != in {
			t.Errorf("got %q, want %q", got, in)
		}
		if again := w.flush(); again != "" {
			t.Errorf("a second flush gives %q", again)
		}
		if got := unaliasArgs(aliases, json.RawMessage(in)); got != in {
			t.Errorf("whole: got %q, want %q", got, in)
		}
	})
	t.Run("long string streams on", func(t *testing.T) {
		in := `{"pattern":"` + long
		w := newAliasRewriter(aliases)
		if out := w.write(in); out != in {
			t.Errorf("a string that can't be an alias was held: %d of %d bytes came", len(out), len(in))
		}
	})

	// a name the client declared that JSON writes with an escape comes
	// back as that name once the arguments are read
	t.Run("declared name needing escapes", func(t *testing.T) {
		names := []string{`say "hi"`, `back\slash`, "tab\there", "a<b>&c", "日本語"}
		tools := map[string]bool{}
		for _, n := range names {
			tools[n] = true
		}
		odd := toolAliases(tools)
		for _, n := range names {
			in := `{"tool":"` + bridgeName(n) + `"}`
			var args struct {
				Tool string `json:"tool"`
			}
			got := unaliasArgs(odd, json.RawMessage(in))
			if err := json.Unmarshal([]byte(got), &args); err != nil {
				t.Errorf("%q: the arguments are no longer JSON (%v): %s", n, err, got)
			} else if args.Tool != n {
				t.Errorf("%q came back as %q (%s)", n, args.Tool, got)
			}
		}
	})
}

// #5840: Claude Code is asked for one call of mcp__magpie__batch whose
// input names two other tools, as the run's model had to write them, and
// the client is handed a call of batch with read and grep inside: the
// declared names, however the input reaches the bridge (in pieces of
// Claude Code's stream cut inside an alias, or in a message written whole)
// and however the client asks for the reply (streamed or not). A string
// of the input that only contains an alias is the model's own text.
func TestClaudeBridgeNestedToolNamesComeBackAsDeclared(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for Claude Code")
	}
	const input = `{"tool_calls":[{"tool":"mcp__magpie__read","arguments":{"pattern":"mcp__magpie__read x"}},{"tool":"mcp__magpie__grep","arguments":{}}]}`
	// the input as Claude Code streams it: cut inside both aliases and
	// inside the pattern that merely contains one
	pieces := []string{
		`{"tool_calls":[{"tool":"mcp__magp`,
		`ie__read","arguments":{"pattern":"mcp__magpie__rea`,
		`d x"}},{"tool":"mcp__magpie_`,
		`_grep","arguments":{}}]`,
		`}`,
	}
	if strings.Join(pieces, "") != input {
		t.Fatalf("the pieces make %q, not the input", strings.Join(pieces, ""))
	}

	dir := t.TempDir()
	event := func(e any) string {
		b, err := json.Marshal(map[string]any{"type": "stream_event", "event": e})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	lines := []string{
		event(map[string]any{"type": "message_start", "message": map[string]any{"id": "m", "model": "claude-sonnet-5", "usage": map[string]any{"input_tokens": 1}}}),
		event(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "toolu_1", "name": "mcp__magpie__batch"}}),
	}
	for _, p := range pieces {
		lines = append(lines, event(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": p}}))
	}
	lines = append(lines,
		event(map[string]any{"type": "content_block_stop", "index": 0}),
		event(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]any{"output_tokens": 3}}),
		event(map[string]any{"type": "message_stop"}),
	)
	streamed := filepath.Join(dir, "streamed.jsonl")
	if err := os.WriteFile(streamed, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	whole, err := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{
		"id": "m", "model": "claude-sonnet-5", "stop_reason": "tool_use", "usage": map[string]any{"output_tokens": 3},
		"content": []any{map[string]any{"type": "tool_use", "id": "toolu_1", "name": "mcp__magpie__batch", "input": json.RawMessage(input)}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	wholeFile := filepath.Join(dir, "whole.jsonl")
	if err := os.WriteFile(wholeFile, append(whole, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	// Claude Code, which writes the one call each time it is spoken to:
	// in its stream's events, or in a message whole when asked to
	script := `#!/bin/sh
while read -r line; do
  case "$line" in
  *wholeplease*) cat '` + wholeFile + `' ;;
  *) cat '` + streamed + `' ;;
  esac
done
`
	testenv.Program(t, filepath.Join(dir, "claude"), script)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	runsEndWithTest(t, dir)

	tools := `[{"name":"batch","input_schema":{"type":"object"}},{"name":"read","input_schema":{"type":"object"}},{"name":"grep","input_schema":{"type":"object"}}]`
	p := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}
	for _, stream := range []bool{false, true} {
		for _, say := range []string{"call it", "call it wholeplease"} {
			what := fmt.Sprintf("stream=%t %q", stream, say)
			s := New()
			body := fmt.Sprintf(`{"model":"claude-sonnet-5","max_tokens":100,"stream":%t,"messages":[{"role":"user","content":%q}],"tools":%s}`, stream, say, tools)
			rec := httptest.NewRecorder()
			var u Usage
			if code, msg := s.serveClaudeSubscription(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, p, "claude-sonnet-5", []byte(body), &u); code != 200 {
				s.subscription.abortAll()
				t.Fatalf("%s: %d %s", what, code, msg)
			}
			s.subscription.abortAll()

			var names []string
			var args string
			if stream {
				var pieces []string
				for _, ev := range events(rec.Body.String()) {
					switch ev["type"] {
					case "content_block_start":
						if b, _ := ev["content_block"].(map[string]any); b["type"] == "tool_use" {
							names = append(names, fmt.Sprint(b["name"]))
						}
					case "content_block_delta":
						if d, _ := ev["delta"].(map[string]any); d["type"] == "input_json_delta" {
							pieces = append(pieces, fmt.Sprint(d["partial_json"]))
						}
					}
				}
				args = strings.Join(pieces, "")
			} else {
				var reply struct {
					Content []struct {
						Type  string          `json:"type"`
						Name  string          `json:"name"`
						Input json.RawMessage `json:"input"`
					} `json:"content"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
					t.Fatalf("%s: the reply isn't a message: %v: %s", what, err, rec.Body)
				}
				for _, c := range reply.Content {
					if c.Type == "tool_use" {
						names = append(names, c.Name)
						args = string(c.Input)
					}
				}
			}
			if !slices.Equal(names, []string{"batch"}) {
				t.Fatalf("%s: the client is told of the calls %q, want one of batch: %s", what, names, rec.Body)
			}
			var got struct {
				ToolCalls []struct {
					Tool      string         `json:"tool"`
					Arguments map[string]any `json:"arguments"`
				} `json:"tool_calls"`
			}
			if err := json.Unmarshal([]byte(args), &got); err != nil {
				t.Fatalf("%s: the call's arguments aren't JSON (%v): %s", what, err, args)
			}
			var inner []string
			for _, c := range got.ToolCalls {
				inner = append(inner, c.Tool)
			}
			if !slices.Equal(inner, []string{"read", "grep"}) {
				t.Errorf("%s: the batch calls %q, want the declared read and grep: %s", what, inner, args)
			}
			if len(got.ToolCalls) > 0 && got.ToolCalls[0].Arguments["pattern"] != "mcp__magpie__read x" {
				t.Errorf("%s: the pattern is %v, want it as the model wrote it: %s", what, got.ToolCalls[0].Arguments["pattern"], args)
			}
			if n := strings.Count(args, "mcp__magpie__"); n != 1 {
				t.Errorf("%s: %d mcp__magpie__ in the arguments, want only the one in the pattern: %s", what, n, args)
			}
		}
	}
}

// #5840 on a run in Claude Code's interactive mode: a call the MCP helper
// hands over before its message is written tells the client itself
// (onCall), and a tool it names in its arguments reaches the client under
// the declared name there too.
func TestClaudeTUICallNamesNestedToolsAsDeclared(t *testing.T) {
	c := newTUICalls(t)
	c.run.mu.Lock()
	c.run.tools = map[string]bool{"Read": true, "Grep": true}
	c.run.mu.Unlock()
	first := c.run.segment
	c.call("toolu_a", `{"tool_calls":[{"tool":"mcp__magpie__Grep","arguments":{"pattern":"mcp__magpie__Grep x"}}]}`)
	_, calls, _, _, raw := c.reply(first)
	if want := `toolu_a{"tool_calls":[{"tool":"Grep","arguments":{"pattern":"mcp__magpie__Grep x"}}]}`; len(calls) != 1 || calls[0] != want {
		t.Fatalf("the call told the client %q, want %q:\n%s", calls, want, raw)
	}
}

// streamedRun reads lines as Claude Code's stream-json output on a run
// outside interactive mode, and returns the events its reply was told
// once the output ends. The fixtures fit the segment's buffer, so the
// output is read whole before the events are.
func streamedRun(t *testing.T, tools map[string]bool, lines ...string) []Event {
	t.Helper()
	run := &subscriptionRun{model: "claude-haiku-5-5", pending: map[string]chan mcpToolResult{}, tools: tools}
	seg := run.attach()
	if len(lines) >= cap(seg) {
		t.Fatalf("%d lines may not fit the segment's %d events", len(lines), cap(seg))
	}
	run.readOutput(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	var evs []Event
	for {
		select {
		case ev, ok := <-seg:
			if !ok {
				return evs
			}
			evs = append(evs, ev)
		default:
			return evs
		}
	}
}

func cliStreamEvent(e string) string { return `{"type":"stream_event","event":` + e + `}` }

// A call's arguments held back while a string may still become an alias
// are handed on before the next block starts, so the client never reads
// one call's bytes as the next one's, and before a cut attempt's error, so
// what was streamed is not lost (#5840 review).
func TestAliasRewriterFlushesBeforeTheNextBlockAndACut(t *testing.T) {
	tools := map[string]bool{"batch": true, "read": true}
	start := cliStreamEvent(`{"type":"message_start","message":{"id":"m","model":"claude-haiku-5-5","usage":{"input_tokens":1}}}`)
	block := func(i int, id string) string {
		return cliStreamEvent(`{"type":"content_block_start","index":` + itoa(i) + `,"content_block":{"type":"tool_use","id":"` + id + `","name":"mcp__magpie__batch"}}`)
	}
	delta := func(i int, s string) string {
		q, _ := json.Marshal(s)
		return cliStreamEvent(`{"type":"content_block_delta","index":` + itoa(i) + `,"delta":{"type":"input_json_delta","partial_json":` + string(q) + `}}`)
	}
	// args is each call's arguments as told, by the call before them
	args := func(evs []Event) (map[string]string, []EventKind) {
		got, cur := map[string]string{}, ""
		var kinds []EventKind
		for _, ev := range evs {
			kinds = append(kinds, ev.Kind)
			switch ev.Kind {
			case KToolStart:
				cur = ev.ID
			case KToolArgs:
				got[cur] += ev.Text
			}
		}
		return got, kinds
	}

	t.Run("next block", func(t *testing.T) {
		evs := streamedRun(t, tools,
			start,
			block(0, "toolu_a"), delta(0, `{"tool":"mcp__mag`),
			block(1, "toolu_b"), delta(1, `{"tool":"mcp__magpie__read"}`), cliStreamEvent(`{"type":"content_block_stop","index":1}`),
			cliStreamEvent(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}`),
			cliStreamEvent(`{"type":"message_stop"}`),
		)
		got, _ := args(evs)
		if got["toolu_a"] != `{"tool":"mcp__mag` || got["toolu_b"] != `{"tool":"read"}` {
			t.Fatalf("the calls were told %q, want toolu_a's held bytes before toolu_b began", got)
		}
	})
	for _, end := range []struct{ name, line string }{
		{"message_stop", cliStreamEvent(`{"type":"message_stop"}`)},
		{"output end", ""},
		{"error result", `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"boom"}`},
	} {
		t.Run("cut by "+end.name, func(t *testing.T) {
			lines := []string{start, block(0, "toolu_a"), delta(0, `{"tool":"mcp__mag`)}
			if end.line != "" {
				lines = append(lines, end.line)
			}
			got, kinds := args(streamedRun(t, tools, lines...))
			if got["toolu_a"] != `{"tool":"mcp__mag` {
				t.Fatalf("a cut call was told %q, want all it streamed: %v", got["toolu_a"], kinds)
			}
			at := slices.Index(kinds, KError)
			if at < 0 {
				t.Fatalf("a cut call ended with no error: %v", kinds)
			}
			if slices.Contains(kinds[at:], KToolArgs) {
				t.Fatalf("arguments told after the error: %v", kinds)
			}
		})
	}
}

// An argument naming one of magpie's own tools (its web search, answer or
// advisor tool) is not a client tool's alias: the client declared none
// of them (#5840 review).
func TestAliasesLeaveMagpiesOwnTools(t *testing.T) {
	run := &subscriptionRun{tools: map[string]bool{"read": true, "web_search": true, "answer": true, advisorTool: true},
		searchName: "web_search", answerName: "answer",
		search: func(context.Context, string) (string, []Hit, error) { return "", nil, nil },
		advice: func(context.Context, *Request, []Part, Part, int) (Part, Event) { return Part{}, Event{} }}
	got := run.aliases()
	if got["mcp__magpie__read"] != "read" {
		t.Errorf("aliases %v lack the client's read", got)
	}
	for _, own := range []string{"web_search", "answer", advisorTool} {
		if n, ok := got["mcp__magpie__"+own]; ok {
			t.Errorf("magpie's %s is taken for the client's %q", own, n)
		}
	}
}
