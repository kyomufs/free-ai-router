package gateway

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// answerOnce hands the client a single response.
//
// The wire may carry either shape: a provider that does not care about the
// stream sends one JSON body, and a provider whose check requires the stream
// form sends events even though the client asked for a single answer. Both end
// here, and the client gets what it asked for: the collected shape is
// indistinguishable from the one a direct non-streaming call would produce.
//
// The watchdog is re-armed before every read for the same reason as in
// passThroughStream: the silence that matters is the gap between two pieces of
// the answer, and a stream that stops arriving without closing would otherwise
// read as a healthy one.
func (h *Handler) answerOnce(w http.ResponseWriter, resp *http.Response, toolNames map[string]string, wd *watchdog, idle time.Duration) {
	contentType := resp.Header.Get("content-type")

	// Not an event stream: the answer is already one body, and rewriting it
	// would only risk changing fields the client parses.
	if !isEventStream(contentType) {
		if contentType != "" {
			w.Header().Set("content-type", contentType)
		}
		w.WriteHeader(resp.StatusCode)
		for {
			wd.arm(idle, errSilence)
			buf := make([]byte, 32<<10)
			n, err := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}

	answer, err := collectAnswer(resp.Body, wd, idle)
	if err != nil {
		// The stream broke before the answer was complete. The client must not
		// receive a half-built body: a truncated JSON would be parsed as a
		// valid answer with missing content, which is worse than a transport
		// failure it can retry.
		writeError(w, http.StatusBadGateway, "transport", "the answer broke off: "+err.Error())
		return
	}

	rewriteToolNamesIn(answer, toolNames)

	raw, err := json.Marshal(answer)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "gateway error", err.Error())
		return
	}

	w.Header().Set("content-type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// isEventStream reports whether the content type is an event stream.
func isEventStream(contentType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "text/event-stream")
}

// collected is the merged answer of a stream.
//
// The fields are the ones a single chat completion carries. Merging is
// necessary rather than convenient: the model sends its content in pieces, its
// thinking in other pieces, its tool calls as fragments that only make sense
// concatenated, and its counters in a final event that carries no choices.
type collected struct {
	id      string
	model   string
	created int64
	// role is taken from the first delta that declares it, because the answer
	// has to declare it too.
	role string
	// content is the concatenation of the content pieces.
	content strings.Builder
	// reasoning is the concatenation of the thinking pieces.
	reasoning strings.Builder
	// calls are the tool calls being assembled, in the order the stream
	// declared them.
	calls   []*collectedCall
	byIndex map[int]*collectedCall
	// finishReason is the last one declared.
	finishReason string
	// usage is the counters of the whole answer.
	usage json.RawMessage
}

// collectedCall is one tool call being assembled from fragments.
type collectedCall struct {
	index     int
	id        string
	name      string
	arguments strings.Builder
}

// collectAnswer reads an event stream and merges it into a single answer.
//
// It returns an error only when the stream ends before it says it is done: an
// answer cut short in the middle is not an answer, and returning what arrived
// would pass a truncated reply off as a complete one.
func collectAnswer(body io.Reader, wd *watchdog, idle time.Duration) (map[string]any, error) {
	c := &collected{byIndex: make(map[int]*collectedCall)}
	scanner := bufio.NewScanner(body)
	// The events of a long answer are large: the default limit would cut them
	// mid-line and turn a healthy stream into a parse error.
	scanner.Buffer(make([]byte, 64<<10), 4<<20)

	done := false
	for !done {
		wd.arm(idle, errSilence)
		if !scanner.Scan() {
			break
		}
		line := scanner.Text()
		payload, ok := eventData(line)
		if !ok {
			continue
		}
		if payload == "[DONE]" {
			done = true
			break
		}
		c.merge(payload)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !done {
		return nil, io.ErrUnexpectedEOF
	}
	return c.answer(), nil
}

// eventData extracts the payload of an event line.
//
// Only data lines carry the answer; comments, event names and blank separators
// carry nothing the merged answer needs.
func eventData(line string) (string, bool) {
	line = strings.TrimRight(line, "\r")
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(line, "data:")), true
}

// merge folds one event into the answer.
//
// A malformed event is skipped rather than fatal: the pieces that did parse are
// still the model's answer, and the counters and the done marker are what
// decide whether the answer is complete.
func (c *collected) merge(payload string) {
	var event struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Created int64  `json:"created"`
		Choices []struct {
			Delta struct {
				Role             string          `json:"role"`
				Content          string          `json:"content"`
				ReasoningContent string          `json:"reasoning_content"`
				Reasoning        string          `json:"reasoning"`
				ToolCalls        []collectedTool `json:"tool_calls"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return
	}

	if c.id == "" {
		c.id = event.ID
	}
	if c.model == "" {
		c.model = event.Model
	}
	if c.created == 0 {
		c.created = event.Created
	}
	if len(event.Usage) > 0 && string(event.Usage) != "null" {
		c.usage = event.Usage
	}

	for _, choice := range event.Choices {
		if choice.Delta.Role != "" {
			c.role = choice.Delta.Role
		}
		c.content.WriteString(choice.Delta.Content)
		c.reasoning.WriteString(choice.Delta.ReasoningContent)
		c.reasoning.WriteString(choice.Delta.Reasoning)
		for _, call := range choice.Delta.ToolCalls {
			c.addCall(call)
		}
		if choice.FinishReason != "" {
			c.finishReason = choice.FinishReason
		}
	}
}

// collectedTool is one tool call fragment as it arrives.
type collectedTool struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// addCall folds a tool call fragment into the answer.
//
// The fragments of one call are tied together by index, not by id: only the
// first fragment carries the id, and the arguments arrive split across the
// rest. Order is preserved, because a client executes the calls in the order it
// receives them.
func (c *collected) addCall(fragment collectedTool) {
	call, ok := c.byIndex[fragment.Index]
	if !ok {
		call = &collectedCall{index: fragment.Index}
		c.byIndex[fragment.Index] = call
		c.calls = append(c.calls, call)
	}
	if fragment.ID != "" {
		call.id = fragment.ID
	}
	if fragment.Function.Name != "" {
		call.name = fragment.Function.Name
	}
	call.arguments.WriteString(fragment.Function.Arguments)
}

// answer assembles the single response.
func (c *collected) answer() map[string]any {
	message := map[string]any{
		"role":    c.role,
		"content": c.content.String(),
	}
	if message["role"] == "" {
		message["role"] = "assistant"
	}
	if reasoning := c.reasoning.String(); reasoning != "" {
		message["reasoning_content"] = reasoning
	}

	if len(c.calls) > 0 {
		calls := make([]any, 0, len(c.calls))
		for _, call := range c.calls {
			calls = append(calls, map[string]any{
				"id":   call.id,
				"type": "function",
				"function": map[string]any{
					"name":      call.name,
					"arguments": call.arguments.String(),
				},
			})
		}
		message["tool_calls"] = calls
	}

	created := c.created
	if created == 0 {
		created = time.Now().Unix()
	}

	out := map[string]any{
		"id":      c.id,
		"object":  "chat.completion",
		"created": created,
		"model":   c.model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": c.finishReason,
		}},
	}
	if len(c.usage) > 0 {
		out["usage"] = c.usage
	}
	return out
}
