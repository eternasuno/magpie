package gateway

import (
	"encoding/json"
	"maps"
	"strings"
)

// toolAliases is the client's name of each of its tools by the alias the
// run's model was shown it under (bridgeName), for those that differ. A
// tool name carried inside another tool's arguments, a batch's items, a
// router's call_tool or a subagent's allowlist, was written by the model
// with the only names it had, and the client knows none of them (#5840).
// An alias that is itself a tool the client declared stays that tool.
func toolAliases(tools map[string]bool) map[string]string {
	aliases := map[string]string{}
	for name := range tools {
		if a := bridgeName(name); a != name && !tools[a] {
			aliases[a] = name
		}
	}
	return aliases
}

// aliasRewriter hands a call's arguments on as they stream, with every
// JSON string that is exactly a tool's alias (toolAliases) turned into the
// client's name. Only a string that may still become an alias is held, at
// most the longest alias: the arguments stream on as before, and a string
// that merely contains an alias, a grep pattern or a pasted log line, is
// left alone. An alias is [A-Za-z0-9_-] only, so a string with an escape
// in it is none.
type aliasRewriter struct {
	aliases  map[string]string
	longest  int
	inString bool
	escaped  bool
	holding  bool
	held     []byte // the string's text so far, while holding
}

func newAliasRewriter(aliases map[string]string) *aliasRewriter {
	w := &aliasRewriter{aliases: aliases}
	for a := range aliases {
		w.longest = max(w.longest, len(a))
	}
	return w
}

// write takes the next piece of the arguments and returns what can be
// handed on of them now.
func (w *aliasRewriter) write(chunk string) string {
	if len(w.aliases) == 0 {
		return chunk
	}
	var out strings.Builder
	for i := 0; i < len(chunk); i++ {
		c := chunk[i]
		switch {
		case !w.inString:
			out.WriteByte(c)
			if c == '"' {
				w.inString, w.holding, w.held = true, true, w.held[:0]
			}
		case w.holding && c == '"':
			if name, ok := w.aliases[string(w.held)]; ok {
				q, _ := json.Marshal(name)
				out.Write(q[1 : len(q)-1])
			} else {
				out.Write(w.held)
			}
			out.WriteByte(c)
			w.inString, w.holding = false, false
		case w.holding:
			w.held = append(w.held, c)
			if !aliasByte(c) || len(w.held) > w.longest || !w.mayBecomeAlias() {
				out.Write(w.held)
				w.holding = false
				w.escaped = c == '\\'
			}
		default:
			out.WriteByte(c)
			switch {
			case w.escaped:
				w.escaped = false
			case c == '\\':
				w.escaped = true
			case c == '"':
				w.inString = false
			}
		}
	}
	return out.String()
}

// flush returns what is held at the arguments' end: a string never closed,
// as it came.
func (w *aliasRewriter) flush() string {
	if !w.holding {
		return ""
	}
	w.holding = false
	return string(w.held)
}

func (w *aliasRewriter) mayBecomeAlias() bool {
	for a := range w.aliases {
		if strings.HasPrefix(a, string(w.held)) {
			return true
		}
	}
	return false
}

func aliasByte(c byte) bool {
	return c == '_' || c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// unaliasArgs is a call's whole arguments with its tools' aliases turned
// into the client's names (aliasRewriter).
func unaliasArgs(aliases map[string]string, args json.RawMessage) string {
	w := newAliasRewriter(aliases)
	return w.write(string(args)) + w.flush()
}

// aliases is the run's toolAliases, of the tools its agent was told of,
// less magpie's own (its web search, answer and advisor tools): the client
// declared none of them, so an argument naming one is not its alias.
func (r *subscriptionRun) aliases() map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	aliases := toolAliases(r.tools)
	maps.DeleteFunc(aliases, func(_, name string) bool {
		return r.search != nil && name == r.searchName || r.answerName != "" && name == r.answerName ||
			r.advice != nil && name == advisorTool
	})
	return aliases
}
