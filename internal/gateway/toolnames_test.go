package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRewriteToolNameInStreamEvent — the model calls the name it was given, and
// the client dispatches on the name it declared. The answer has to carry the
// client's spelling, while everything else in the event stays as it arrived.
func TestRewriteToolNameInStreamEvent(t *testing.T) {
	payload := []byte(`{"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"subagentfork","arguments":""}}]}}]}`)
	out := rewriteEventPayload(payload, map[string]string{"subagentfork": "subagent_fork"})

	var event map[string]any
	if err := json.Unmarshal(out, &event); err != nil {
		t.Fatalf("rewritten event does not parse: %v", err)
	}
	choice := event["choices"].([]any)[0].(map[string]any)
	call := choice["delta"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	fn := call["function"].(map[string]any)
	if got := fn["name"]; got != "subagent_fork" {
		t.Fatalf("name = %v, want subagent_fork", got)
	}
	if got := call["id"]; got != "call_1" {
		t.Fatalf("the call id changed: %v", got)
	}
}

// TestRewriteLeavesUnrelatedPayloadAlone — a payload that carries no renamed
// name must be returned byte for byte: re-marshalling every event of every
// answer would be a cost with no benefit, and a chance to change a field nobody
// asked about.
func TestRewriteLeavesUnrelatedPayloadAlone(t *testing.T) {
	names := map[string]string{"subagentfork": "subagent_fork"}

	for _, payload := range []string{
		`{"choices":[{"delta":{"content":"hello"}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"function":{"name":"write","arguments":"{}"}}]}}]}`,
		`not json at all`,
	} {
		if out := rewriteEventPayload([]byte(payload), names); string(out) != payload {
			t.Fatalf("payload changed: %s -> %s", payload, out)
		}
	}
}

// TestRewriteEventLineKeepsFraming — SSE framing and the line ending are part of
// the stream, not decoration.
func TestRewriteEventLineKeepsFraming(t *testing.T) {
	names := map[string]string{"subagentfork": "subagent_fork"}
	line := []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"subagentfork\"}}]}}]}\r\n")

	out := string(rewriteEventLine(line, names))
	if !strings.HasPrefix(out, "data: ") {
		t.Fatalf("framing lost: %q", out)
	}
	if !strings.HasSuffix(out, "\r\n") {
		t.Fatalf("line ending lost: %q", out)
	}
	if !strings.Contains(out, `"name":"subagent_fork"`) {
		t.Fatalf("name not rewritten: %q", out)
	}

	for _, untouched := range [][]byte{
		[]byte("data: [DONE]\n"),
		[]byte("event: message\n"),
		[]byte(": keep-alive\n"),
	} {
		if got := rewriteEventLine(untouched, names); string(got) != string(untouched) {
			t.Fatalf("line changed: %q -> %q", untouched, got)
		}
	}
}

// TestRewriteToolNamesInCollectedAnswer — the same translation has to happen in
// the single body handed to a client that asked for no stream.
func TestRewriteToolNamesInCollectedAnswer(t *testing.T) {
	answer := map[string]any{
		"choices": []any{
			map[string]any{
				"message": map[string]any{
					"tool_calls": []any{
						map[string]any{
							"id":       "call_1",
							"type":     "function",
							"function": map[string]any{"name": "subagentfork", "arguments": "{}"},
						},
					},
				},
			},
		},
	}

	if !rewriteToolNamesIn(answer, map[string]string{"subagentfork": "subagent_fork"}) {
		t.Fatal("rewrite reported no change")
	}
	choice := answer["choices"].([]any)[0].(map[string]any)
	call := choice["message"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	if got := call["function"].(map[string]any)["name"]; got != "subagent_fork" {
		t.Fatalf("name = %v, want subagent_fork", got)
	}
}
