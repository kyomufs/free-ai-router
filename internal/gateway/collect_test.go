package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"free-ai-router/internal/provider"
)

// Collecting a stream into a single response.
//
// THE REASON THIS EXISTS. A provider's check can require the stream form on the
// wire while the client is entitled to the shape it asked for. Measured on
// opencode: the same request answered 200 with "stream": true and 403
// FreeTierError with "stream": false. The gateway therefore sends a stream and,
// for a non-streaming client, merges the events back into one body.

// TestEventsBecomeOneAnswer — the pieces are merged into the shape a direct
// non-streaming call would have produced.
func TestEventsBecomeOneAnswer(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		// A comment line carries nothing the merged answer needs.
		_, _ = io.WriteString(w, ": keep-alive\n\n")
		_, _ = io.WriteString(w, `data: {"id":"cmpl-1","object":"chat.completion.chunk","created":1792410367,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"id":"cmpl-1","choices":[{"index":0,"delta":{"reasoning_content":"think "},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"id":"cmpl-1","choices":[{"index":0,"delta":{"reasoning_content":"more"},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"id":"cmpl-1","choices":[{"index":0,"delta":{"content":"hel"},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"id":"cmpl-1","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"id":"cmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"shell","arguments":"{\"comm"}}]},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"id":"cmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"and\":\"ls\"}"}}]},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"id":"cmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"id":"cmpl-1","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":7,"total_tokens":12}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	h, _, _ := newTestHandler(t, upstream)
	rec := postChat(t, h, `{"model":"test/model","messages":[{"role":"user","content":"x"}]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("content-type"); ct != "application/json" {
		t.Fatalf("content-type = %q, want the single-response one", ct)
	}

	var answer struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Role             string `json:"role"`
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			Total int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatalf("the answer is not JSON: %v: %s", err, rec.Body.String())
	}

	if answer.Object != "chat.completion" {
		t.Fatalf("object = %q", answer.Object)
	}
	if answer.ID != "cmpl-1" || answer.Model != "model" || answer.Created != 1792410367 {
		t.Fatalf("the envelope was not carried over: %+v", answer)
	}
	if len(answer.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(answer.Choices))
	}
	message := answer.Choices[0].Message
	if message.Content != "hello" {
		t.Fatalf("content = %q, want the pieces joined", message.Content)
	}
	if message.ReasoningContent != "think more" {
		t.Fatalf("reasoning_content = %q", message.ReasoningContent)
	}
	if answer.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("finish_reason = %q", answer.Choices[0].FinishReason)
	}
	if len(message.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %d, want 1", len(message.ToolCalls))
	}
	if got := message.ToolCalls[0]; got.ID != "call_1" || got.Function.Name != "shell" || got.Function.Arguments != `{"command":"ls"}` {
		t.Fatalf("the tool call was not assembled: %+v", got)
	}
	if answer.Usage.Total != 12 {
		t.Fatalf("usage = %+v, want the counters of the final event", answer.Usage)
	}
}

// TestAStreamCutShortIsNotAnAnswer — a stream that stops without its end marker
// is a transport failure, not a short answer.
//
// Handing over what arrived would be the worst of the two: the client parses it
// as a complete reply and the truncation is invisible to it.
func TestAStreamCutShortIsNotAnAnswer(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"id":"cmpl-1","model":"model","choices":[{"index":0,"delta":{"content":"half"},"finish_reason":null}]}`+"\n\n")
	}))
	defer upstream.Close()

	h, _, _ := newTestHandler(t, upstream)
	rec := postChat(t, h, `{"model":"test/model","messages":[{"role":"user","content":"x"}]}`)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusBadGateway, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "half") {
		t.Fatalf("the half answer reached the client: %s", rec.Body.String())
	}
}

// TestAProviderThatAlreadyAnswersOnceIsPassedThrough — the collection must not
// touch a body that is already a single response.
func TestAProviderThatAlreadyAnswersOnceIsPassedThrough(t *testing.T) {
	const body = `{"id":"cmpl-2","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"whole"},"finish_reason":"stop"}],"future_field":{"keep":true}}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer upstream.Close()

	h, _, _ := newTestHandler(t, upstream)
	rec := postChat(t, h, `{"model":"test/model","messages":[{"role":"user","content":"x"}]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != body {
		t.Fatalf("the body was rewritten:\n got %s\nwant %s", rec.Body.String(), body)
	}
}

// TestAStreamIsStillStreamedWhenAskedFor — the client's own shape wins.
func TestAStreamIsStillStreamedWhenAskedFor(t *testing.T) {
	const chunk = `data: {"choices":[{"delta":{"content":"piece"},"finish_reason":null}]}` + "\n\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, chunk)
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	h, _, _ := newTestHandler(t, upstream)
	rec := postChat(t, h, `{"model":"test/model","messages":[{"role":"user","content":"x"}],"stream":true}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Header().Get("content-type") != "text/event-stream" {
		t.Fatalf("content-type = %q, want the stream one", rec.Header().Get("content-type"))
	}
	if rec.Body.String() != chunk+"data: [DONE]\n\n" {
		t.Fatalf("the events were altered: %q", rec.Body.String())
	}
}

// TestAProviderThatRequiresAStreamIsFedOne — the wire shape follows the
// provider's contract, not the client's request.
func TestAProviderThatRequiresAStreamIsFedOne(t *testing.T) {
	var sent map[string]any

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		sent = nil
		_ = json.Unmarshal(raw, &sent)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"id":"cmpl-3","model":"model","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	h, p, _ := newTestHandler(t, upstream)
	p.build = func(provider.Request) (provider.Prepared, error) {
		return provider.Prepared{
			URL:             upstream.URL + "/chat/completions",
			Headers:         http.Header{"content-type": []string{"application/json"}},
			Body:            []byte(`{"model":"model","stream":true}`),
			StreamsUpstream: true,
		}, nil
	}

	rec := postChat(t, h, `{"model":"test/model","messages":[{"role":"user","content":"x"}]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if stream, ok := sent["stream"].(bool); !ok || !stream {
		t.Fatalf("the upstream did not receive a stream request: %+v", sent)
	}
	if ct := rec.Header().Get("content-type"); ct != "application/json" {
		t.Fatalf("content-type = %q, want a single response", ct)
	}
	if !strings.Contains(rec.Body.String(), `"content":"ok"`) {
		t.Fatalf("the answer was not merged: %s", rec.Body.String())
	}
}

// TestTheSystemPartIsDeliveredOnce — the system instruction must reach the
// provider exactly once.
//
// It travels in its own field, and keeping a copy in the history would hand the
// provider two identical system entries: the model is billed for both, and the
// instruction is read twice.
func TestTheSystemPartIsDeliveredOnce(t *testing.T) {
	body := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "first rule"},
			map[string]any{"role": "system", "content": "second rule"},
			map[string]any{"role": "user", "content": "hello"},
			map[string]any{"role": "assistant", "content": "hi"},
		},
	}

	if got := systemFrom(body); got != "first rule\nsecond rule" {
		t.Fatalf("system = %q", got)
	}

	messages, err := parseMessages(body["messages"])
	if err != nil {
		t.Fatalf("parseMessages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want the two non-system ones: %+v", len(messages), messages)
	}
	for _, m := range messages {
		if m.Role == "system" {
			t.Fatalf("a system message stayed in the history: %+v", m)
		}
	}
	if messages[0].Role != "user" || messages[1].Role != "assistant" {
		t.Fatalf("the order was not preserved: %+v", messages)
	}
}
