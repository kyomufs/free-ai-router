package gateway

import (
	"bytes"
	"encoding/json"
)

// Tool names in the answer.
//
// A provider may have to send a tool declaration under another name (see
// internal/provider/opencode: the free lane reads one v1 tool name as a marker).
// The model then calls the replacement, and the client would not recognize it:
// the name is what the client dispatches on. The provider reports the
// replacement in provider.Prepared.ToolNames, and this file applies it to the
// answer, in both shapes the gateway can hand out — an event stream and a
// collected body.
//
// Only `function.name` is touched. The field appears in a tool call and nowhere
// else in a chat completion, so the walk can be generic: it does not depend on
// the shape of a particular event.

// rewriteToolNamesIn rewrites every `function.name` that names a replacement,
// and reports whether anything changed.
func rewriteToolNamesIn(v any, names map[string]string) bool {
	if len(names) == 0 {
		return false
	}
	changed := false
	switch node := v.(type) {
	case map[string]any:
		if fn, ok := node["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok {
				if original, replaced := names[name]; replaced {
					fn["name"] = original
					changed = true
				}
			}
		}
		for _, child := range node {
			if rewriteToolNamesIn(child, names) {
				changed = true
			}
		}
	case []any:
		for _, child := range node {
			if rewriteToolNamesIn(child, names) {
				changed = true
			}
		}
	}
	return changed
}

// rewriteEventPayload rewrites one JSON event. The payload is returned untouched
// when it carries no `function` field or does not parse: a stream is not the
// place to fail over a rewrite that is not needed.
func rewriteEventPayload(payload []byte, names map[string]string) []byte {
	if len(names) == 0 || !bytes.Contains(payload, []byte(`"function"`)) {
		return payload
	}
	var v any
	if err := json.Unmarshal(payload, &v); err != nil {
		return payload
	}
	if !rewriteToolNamesIn(v, names) {
		return payload
	}
	out, err := json.Marshal(v)
	if err != nil {
		return payload
	}
	return out
}

// rewriteEventLine rewrites the payload of a `data:` line, keeping the framing
// and the line ending as they arrived.
func rewriteEventLine(line []byte, names map[string]string) []byte {
	content := bytes.TrimRight(line, "\r\n")
	if !bytes.HasPrefix(content, []byte("data:")) {
		return line
	}
	payload := bytes.TrimSpace(content[len("data:"):])
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return line
	}
	rewritten := rewriteEventPayload(payload, names)
	if bytes.Equal(rewritten, payload) {
		return line
	}
	out := make([]byte, 0, len(rewritten)+8)
	out = append(out, "data: "...)
	out = append(out, rewritten...)
	out = append(out, line[len(content):]...)
	return out
}
