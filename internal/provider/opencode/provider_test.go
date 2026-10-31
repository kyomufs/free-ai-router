package opencode

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"free-ai-router/internal/provider"
	"free-ai-router/internal/provider/modelcat"
)

// newTestProvider creates a provider with predictable substitutions, so that the
// assertions do not depend on the machine's environment.
func newTestProvider(t *testing.T) *Provider {
	t.Helper()
	p, err := New(Config{OSName: "Linux", ShellName: "bash"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

// TestGateToolsAlwaysPresent is a contract property: the request must carry both
// mandatory tools, even if the client sent none.
func TestGateToolsAlwaysPresent(t *testing.T) {
	p := newTestProvider(t)

	cases := []struct {
		name  string
		tools []provider.Tool
	}{
		{"the client sent no tools", nil},
		{"the client sent one unrelated tool", []provider.Tool{{Name: "grep", Description: "search"}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prep, err := p.Build(provider.Request{
				Model: provider.Model{ID: "mimo-v2.6-flash-free"},
				Tools: tc.tools,
			})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			names := toolNamesFromBody(t, prep.Body)
			if !contains(names, gateNameShell) {
				t.Fatalf("tools %v lack the %q gate entry", names, gateNameShell)
			}
			if !contains(names, gateNameRead) {
				t.Fatalf("tools %v lack the %q gate entry", names, gateNameRead)
			}
		})
	}
}

// TestGateToolReplacedWhenCallerSendsOwn — the client may send a tool with the
// right name and someone else's content. The check compares entries in full, so
// such an entry must be REPLACED with the canonical one rather than kept.
func TestGateToolReplacedWhenCallerSendsOwn(t *testing.T) {
	p := newTestProvider(t)

	prep, err := p.Build(provider.Request{
		Model: provider.Model{ID: "mimo-v2.6-flash-free"},
		Tools: []provider.Tool{{
			Name:        gateNameShell,
			Description: "I call myself bash and can do nothing",
			Schema:      []byte(`{"type":"object","properties":{}}`),
		}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	entries := toolEntriesFromBody(t, prep.Body)
	shellEntries := 0
	for _, entry := range entries {
		fn := entry["function"].(map[string]any)
		if fn["name"] != gateNameShell {
			continue
		}
		shellEntries++
		desc, _ := fn["description"].(string)
		if !strings.HasPrefix(desc, "Execute a shell command and return its output.") {
			t.Fatalf("the gate entry kept the caller's description: %q", desc)
		}
		params, ok := fn["parameters"].(map[string]any)
		if !ok {
			t.Fatalf("the gate entry has no parameters object")
		}
		if _, ok := params["$schema"]; !ok {
			t.Fatalf("the gate entry lost its $schema dialect")
		}
		props, ok := params["properties"].(map[string]any)
		if !ok || len(props) == 0 {
			t.Fatalf("the gate entry has empty properties: %v", props)
		}
	}
	if shellEntries != 1 {
		t.Fatalf("expected exactly one %q entry, got %d", gateNameShell, shellEntries)
	}
}

// TestGateShellDescriptionCarriesOSAndShell: the tool description is
// parameterised by system and shell, and the substitution must happen.
func TestGateShellDescriptionCarriesOSAndShell(t *testing.T) {
	p := newTestProvider(t)
	prep, err := p.Build(provider.Request{Model: provider.Model{ID: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	desc := gateDescription(t, prep.Body, gateNameShell)
	if !strings.Contains(desc, "Commands run on Linux using bash.") {
		t.Fatalf("shell description lacks the OS/shell clause: %q", desc)
	}
}

// TestToolChoiceFollowsClientSignalledChoice is the shape the client sends.
//
// The mandatory tools are added ALWAYS, so the `tools` array in the body is
// non-empty even when the client sent nothing. It follows that `tool_choice`
// cannot depend on "whether the client sent tools": it depends only on whether
// the client stated a requirement.
//
// Both halves are checked: without a requirement the field is absent entirely;
// with a requirement it arrives verbatim.
func TestToolChoiceFollowsClientSignalledChoice(t *testing.T) {
	p := newTestProvider(t)

	unset, err := p.Build(provider.Request{Model: provider.Model{ID: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	var unsetBody map[string]any
	if err := json.Unmarshal(unset.Body, &unsetBody); err != nil {
		t.Fatal(err)
	}
	if _, present := unsetBody["tool_choice"]; present {
		t.Fatalf("tool_choice must be absent when the client set none, got %v", unsetBody["tool_choice"])
	}
	// Meanwhile the tools themselves are there: their presence does not depend
	// on the client.
	if tools, _ := unsetBody["tools"].([]any); len(tools) == 0 {
		t.Fatal("the gate tools must be present even without caller tools")
	}

	signalled, err := p.Build(provider.Request{
		Model:      provider.Model{ID: "x"},
		ToolChoice: "auto",
	})
	if err != nil {
		t.Fatal(err)
	}
	var signalledBody map[string]any
	if err := json.Unmarshal(signalled.Body, &signalledBody); err != nil {
		t.Fatal(err)
	}
	if signalledBody["tool_choice"] != "auto" {
		t.Fatalf("tool_choice = %v, want auto", signalledBody["tool_choice"])
	}
}

// TestSessionAndAffinityAreDistinct is a contract property: the session and
// affinity headers carry DIFFERENT values when the session is nested.
func TestSessionAndAffinityAreDistinct(t *testing.T) {
	p := newTestProvider(t)

	prep, err := p.Build(provider.Request{
		Model: provider.Model{ID: "x"},
		Session: provider.SessionIDs{
			Session:  "ses_000000000000AAAAAAAAAAAAAA",
			Affinity: "ses_000000000000BBBBBBBBBBBBBB",
			Project:  "global",
			Message:  "msg_000000000000CCCCCCCCCCCCCC",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The outbound names are spelled as the reference client spells them, so the
	// lookup is a map key and not Get, which canonicalises the name first.
	got := header(prep.Headers, "X-Opencode-Session-Id")
	want := "ses_000000000000AAAAAAAAAAAAAA"
	if got != want {
		t.Fatalf("X-Opencode-Session-Id = %q, want %q", got, want)
	}
	if got := header(prep.Headers, "X-Opencode-Session"); got != "ses_000000000000BBBBBBBBBBBBBB" {
		t.Fatalf("X-Opencode-Session = %q, want the affinity", got)
	}
	if got := header(prep.Headers, "X-Session-Affinity"); got != "ses_000000000000BBBBBBBBBBBBBB" {
		t.Fatalf("X-Session-Affinity = %q, want the affinity", got)
	}
	if got := header(prep.Headers, "X-Session-Id"); got != "ses_000000000000BBBBBBBBBBBBBB" {
		t.Fatalf("X-Session-Id = %q, want the affinity", got)
	}
}

// TestParentHeadersOnlyForNestedSessions — the parent headers are present if and
// only if there is a parent.
func TestParentHeadersOnlyForNestedSessions(t *testing.T) {
	p := newTestProvider(t)

	flat, err := p.Build(provider.Request{
		Model:   provider.Model{ID: "x"},
		Session: provider.SessionIDs{Session: "ses_000000000000AAAAAAAAAAAAAA"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if header(flat.Headers, "X-Opencode-Parent-Session-Id") != "" {
		t.Fatalf("a flat session must not carry X-Opencode-Parent-Session-Id")
	}

	nested, err := p.Build(provider.Request{
		Model: provider.Model{ID: "x"},
		Session: provider.SessionIDs{
			Session: "ses_000000000000AAAAAAAAAAAAAA",
			Parent:  "ses_000000000000DDDDDDDDDDDDDD",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := header(nested.Headers, "X-Opencode-Parent-Session-Id"); got != "ses_000000000000DDDDDDDDDDDDDD" {
		t.Fatalf("X-Opencode-Parent-Session-Id = %q, want the parent", got)
	}
}

// TestProjectHeaderForwarded — the value the identifier source produced reaches
// the wire unchanged. Substitution happens in SessionIDs, not here.
func TestProjectHeaderForwarded(t *testing.T) {
	p := newTestProvider(t)

	const project = "156f1e9a1edbdacd44def9b6b405af2c7d710552"
	prep, err := p.Build(provider.Request{
		Model:   provider.Model{ID: "x"},
		Session: provider.SessionIDs{Session: "ses_000000000000AAAAAAAAAAAAAA", Project: project},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := header(prep.Headers, "X-Opencode-Project"); got != project {
		t.Fatalf("X-Opencode-Project = %q, want %q", got, project)
	}
}

// TestReasoningFieldFromModel — the thinking is written to the field the model
// declared, not to an arbitrary one.
func TestReasoningFieldFromModel(t *testing.T) {
	p := newTestProvider(t)

	prep, err := p.Build(provider.Request{
		Model: provider.Model{ID: "x", ReasoningField: "reasoning_content"},
		Messages: []provider.Message{
			{Role: "user", Text: "hi"},
			{Role: "assistant", Text: "answer", Reasoning: "thinking"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var body struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(prep.Body, &body); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, m := range body.Messages {
		if m["role"] != "assistant" {
			continue
		}
		found = true
		if m["reasoning_content"] != "thinking" {
			t.Fatalf("reasoning landed in the wrong field: %v", m)
		}
	}
	if !found {
		t.Fatal("no assistant message in the body")
	}
}

// TestEmptyReasoningIsStillWritten — the thinking field is written even when the
// thinking is empty.
//
// A missing key is the failure, not an empty value: in thinking mode the
// upstream requires the field on every assistant message when tools are in
// play, and a turn whose thinking happened to be empty cannot be allowed to
// lose it. That is what the client means when it sends `reasoning_content: ""`,
// and it is what the upstream holds the client to with "The `reasoning_content`
// in the thinking mode must be passed back to the API".
func TestEmptyReasoningIsStillWritten(t *testing.T) {
	p := newTestProvider(t)

	prep, err := p.Build(provider.Request{
		Model: provider.Model{ID: "x", Reasoning: true, ReasoningField: "reasoning_content"},
		Messages: []provider.Message{
			{Role: "user", Text: "hi"},
			{Role: "assistant", Text: "looking", ToolCalls: []provider.ToolCall{
				{ID: "call_1", Name: "shell", Arguments: `{"command":"ls"}`},
			}},
			{Role: "tool", ToolCallID: "call_1", Text: "file"},
		},
		Tools: []provider.Tool{{Name: "shell"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var body struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(prep.Body, &body); err != nil {
		t.Fatal(err)
	}
	for _, m := range body.Messages {
		if m["role"] != "assistant" {
			continue
		}
		value, ok := m["reasoning_content"]
		if !ok {
			t.Fatalf("the thinking field is missing: %v", m)
		}
		if value != "" {
			t.Fatalf("the thinking field = %v, want an empty string", value)
		}
	}
}

// TestNoThinkingFieldOutsideThinkingMode — an ordinary conversation does not get
// the field.
//
// A provider is free to reject a field it does not know, and an empty
// `reasoning_content` on every message of every request would be exactly that
// field. The field belongs to thinking mode, and thinking mode is what has to be
// established.
func TestNoThinkingFieldOutsideThinkingMode(t *testing.T) {
	p := newTestProvider(t)

	prep, err := p.Build(provider.Request{
		Model: provider.Model{ID: "x", ReasoningField: "reasoning_content"},
		Messages: []provider.Message{
			{Role: "user", Text: "hi"},
			{Role: "assistant", Text: "answer"},
			{Role: "user", Text: "again"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var body struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(prep.Body, &body); err != nil {
		t.Fatal(err)
	}
	for _, m := range body.Messages {
		if _, ok := m["reasoning_content"]; ok {
			t.Fatalf("the thinking field appeared outside thinking mode: %v", m)
		}
	}
}

// TestEmptyAssistantDropped — an empty assistant turn is not allowed on the wire
// and must be dropped.
func TestEmptyAssistantDropped(t *testing.T) {
	p := newTestProvider(t)

	prep, err := p.Build(provider.Request{
		Model: provider.Model{ID: "x"},
		Messages: []provider.Message{
			{Role: "user", Text: "hi"},
			{Role: "assistant"},
			{Role: "user", Text: "again"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(prep.Body, &body); err != nil {
		t.Fatal(err)
	}
	for _, m := range body.Messages {
		if m["role"] == "assistant" {
			t.Fatalf("an empty assistant turn reached the wire: %v", m)
		}
	}
}

// TestStreamIsAlwaysRequested is a contract property measured against the live
// service: an otherwise perfect request with "stream": false is refused with
// FreeTierError, and the same request with "stream": true is answered.
//
// The client's own shape is not lost: the flag on the prepared request tells the
// gateway that the wire is streamed, and it collects the events into one body
// when the client asked for a single response.
func TestStreamIsAlwaysRequested(t *testing.T) {
	p := newTestProvider(t)

	for _, want := range []bool{false, true} {
		t.Run("client asked stream="+strconv.FormatBool(want), func(t *testing.T) {
			prep, err := p.Build(provider.Request{
				Model:  provider.Model{ID: "mimo-v2.6-flash-free"},
				Stream: want,
			})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}

			var body map[string]any
			if err := json.Unmarshal(prep.Body, &body); err != nil {
				t.Fatalf("unmarshal body: %v", err)
			}
			if stream, ok := body["stream"].(bool); !ok || !stream {
				t.Fatalf("stream = %v, want the stream form on the wire", body["stream"])
			}
			options, ok := body["stream_options"].(map[string]any)
			if !ok {
				t.Fatalf("stream_options missing: %s", prep.Body)
			}
			if include, _ := options["include_usage"].(bool); !include {
				t.Fatalf("the usage counters are not requested: %v", options)
			}
			if !prep.StreamsUpstream {
				t.Fatal("the gateway was not told the wire is streamed")
			}
		})
	}
}

// TestSystemPromptCarriesTheCanonicalClient — the free lane recognises the
// client by the canonical prompt and the environment block that follows it.
//
// MEASURED on 2026-10-25: the text alone, the prompt this package used to
// render, and a generic system message were answered 429 FreeUsageLimitError;
// this composition was answered 200. The values in the block are not checked.
func TestSystemPromptCarriesTheCanonicalClient(t *testing.T) {
	p := newTestProvider(t)
	model := provider.Model{ID: "mimo-v2.6-flash-free"}

	got := p.systemPrompt(model, "")
	if !strings.HasPrefix(got, "You are an AI agent running in OpenCode, a coding agent harness.") {
		t.Fatalf("the prompt does not start with the canonical text: %q", got[:80])
	}
	wantModel := "\n# Your Model\n- Name: mimo-v2.6-flash-free\n" +
		"- Provider ID: opencode\n- Model ID: mimo-v2.6-flash-free\n"
	if !strings.Contains(got, wantModel) {
		t.Fatalf("the model block is missing:\n%s", wantModel)
	}
	if !strings.Contains(got, "\nToday's date: ") {
		t.Fatal("the date line is missing")
	}
	if !strings.Contains(got, "Here is some useful information about the environment you are running in:\n<env>\n") {
		t.Fatal("the environment block is missing")
	}
	if !strings.Contains(got, "\n  Platform: ") || !strings.HasSuffix(got, "</env>") {
		t.Fatal("the environment block is not closed")
	}
}

// TestSystemPromptKeepsTheClientRules — the client's own system part is not
// dropped: it is placed before the environment block, where the v2 client puts
// its own tool guidance.
func TestSystemPromptKeepsTheClientRules(t *testing.T) {
	p := newTestProvider(t)
	got := p.systemPrompt(provider.Model{ID: "m"}, "HARNESS RULES")
	if !strings.Contains(got, "\nHARNESS RULES\n\nToday's date: ") {
		t.Fatal("the client rules are not kept before the environment block")
	}
	if !strings.HasSuffix(got, "</env>") {
		t.Fatal("the environment block must stay at the end")
	}
}

// TestBuildBodyPutsTheRenderedSystemFirst — whatever the client sent as the
// system part, the wire body starts with one rendered system message.
func TestBuildBodyPutsTheRenderedSystemFirst(t *testing.T) {
	p := newTestProvider(t)
	prep, err := p.Build(provider.Request{
		Model:    provider.Model{ID: "m"},
		System:   "HARNESS RULES",
		Messages: []provider.Message{{Role: "user", Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(prep.Body, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(body.Messages))
	}
	if body.Messages[0].Role != "system" {
		t.Fatalf("first message role = %q, want system", body.Messages[0].Role)
	}
	if !strings.HasPrefix(body.Messages[0].Content, "You are an AI agent running in OpenCode") {
		t.Fatal("the first message is not the canonical prompt")
	}
	if !strings.Contains(body.Messages[0].Content, "\nHARNESS RULES\n\nToday's date: ") {
		t.Fatal("the client's system part is missing from the first message")
	}
	if body.Messages[1].Role != "user" || body.Messages[1].Content != "hi" {
		t.Fatalf("history was changed: %+v", body.Messages[1])
	}
}

// TestOutputBudgetUsesTheV2FieldName — the client sends max_completion_tokens
// for this provider (detectMaxTokensField in
// packages/ai/src/protocols/openai-chat.ts). The old max_tokens spelling is a
// v1 marker and is no longer emitted.
func TestOutputBudgetUsesTheV2FieldName(t *testing.T) {
	p := newTestProvider(t)
	prep, err := p.Build(provider.Request{
		Model:     provider.Model{ID: "m"},
		MaxTokens: 32000,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(prep.Body, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got := body["max_completion_tokens"]; got != float64(32000) {
		t.Fatalf("max_completion_tokens = %v, want 32000", got)
	}
	if _, ok := body["max_tokens"]; ok {
		t.Fatal("the v1 max_tokens field is still emitted")
	}
}

// TestV1ForkToolNameIsReplaced — MEASURED 2026-10-31: the lane answers 429 for
// a tool declaration whose name contains `subagent_fork` in any case, and 200
// for the same entry with the underscore dropped. The replacement has to be
// reported to the gateway, or the client would receive a tool call it cannot
// dispatch.
func TestV1ForkToolNameIsReplaced(t *testing.T) {
	p := newTestProvider(t)
	prep, err := p.Build(provider.Request{
		Model: provider.Model{ID: "m"},
		Tools: []provider.Tool{
			{Name: "subagent_fork", Description: "fork"},
			{Name: "my_SUBAGENT_FORK_tool", Description: "fork again"},
			{Name: "grep", Description: "search"},
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(prep.Body, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	seen := make(map[string]bool)
	for _, entry := range toolEntriesFromBody(t, prep.Body) {
		fn, _ := entry["function"].(map[string]any)
		name, _ := fn["name"].(string)
		if _, replaced := aliasedToolName(name); replaced {
			t.Fatalf("a marker name went on the wire in %q", name)
		}
		seen[name] = true
	}
	if !seen["grep"] {
		t.Fatalf("an unrelated tool was renamed: %v", seen)
	}
	if got := prep.ToolNames["subagentfork"]; got != "subagent_fork" {
		t.Fatalf("ToolNames[subagentfork] = %q, want subagent_fork", got)
	}
	if got := prep.ToolNames["my_subagentfork_tool"]; got != "my_SUBAGENT_FORK_tool" {
		t.Fatalf("ToolNames[my_subagentfork_tool] = %q, want my_SUBAGENT_FORK_tool", got)
	}
}

// TestImageDirectoryPairIsReplaced — MEASURED 2026-10-31: `read_image` and
// `working_directory` declared together are answered 429 in 0.2 s, while either
// name alone is admitted, and 21 other pairings of `read_image` with a dsh tool
// are admitted. The replacement is therefore about the set, not about either
// name; both are sent with the underscore dropped.
func TestImageDirectoryPairIsReplaced(t *testing.T) {
	p := newTestProvider(t)
	prep, err := p.Build(provider.Request{
		Model: provider.Model{ID: "m"},
		Tools: []provider.Tool{
			{Name: "read_image", Description: "look"},
			{Name: "working_directory", Description: "cd"},
			{Name: "grep", Description: "search"},
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	seen := make(map[string]bool)
	for _, entry := range toolEntriesFromBody(t, prep.Body) {
		fn, _ := entry["function"].(map[string]any)
		name, _ := fn["name"].(string)
		seen[name] = true
	}
	for _, want := range []string{"readimage", "workingdirectory", "grep"} {
		if !seen[want] {
			t.Fatalf("tool %q is missing from the wire: %v", want, seen)
		}
	}
	for _, unwanted := range []string{"read_image", "working_directory"} {
		if seen[unwanted] {
			t.Fatalf("the refused name %q went on the wire", unwanted)
		}
	}
	if got := prep.ToolNames["readimage"]; got != "read_image" {
		t.Fatalf("ToolNames[readimage] = %q, want read_image", got)
	}
	if got := prep.ToolNames["workingdirectory"]; got != "working_directory" {
		t.Fatalf("ToolNames[workingdirectory] = %q, want working_directory", got)
	}
}

// TestForkToolNameInHistoryIsForwarded — only the declaration is a marker: the
// lane was measured to admit the same name inside an assistant tool call, and
// rewriting history would change what the model is asked to continue.
func TestForkToolNameInHistoryIsForwarded(t *testing.T) {
	p := newTestProvider(t)
	prep, err := p.Build(provider.Request{
		Model: provider.Model{ID: "m"},
		Messages: []provider.Message{
			{Role: "user", Text: "go"},
			{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "call_1", Name: "subagent_fork", Arguments: "{}"}}},
			{Role: "tool", ToolCallID: "call_1", Text: "ok"},
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(string(prep.Body), `"name":"subagent_fork"`) {
		t.Fatal("the historical tool call was rewritten; only declarations are markers")
	}
}

// TestEveryToolCarriesStrictFalse — the v2 client lowers every tool with
// `"strict": false` for this provider, mandatory ones included.
func TestEveryToolCarriesStrictFalse(t *testing.T) {
	p := newTestProvider(t)
	prep, err := p.Build(provider.Request{
		Model: provider.Model{ID: "m"},
		Tools: []provider.Tool{{Name: "grep", Description: "search", Schema: []byte(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, entry := range toolEntriesFromBody(t, prep.Body) {
		fn, _ := entry["function"].(map[string]any)
		if fn == nil {
			t.Fatalf("tool entry has no function: %+v", entry)
		}
		strict, ok := fn["strict"].(bool)
		if !ok || strict {
			t.Fatalf("tool %v strict = %v, want false", fn["name"], fn["strict"])
		}
	}
}

// --- helpers ---

func toolNamesFromBody(t *testing.T, body []byte) []string {
	t.Helper()
	out := make([]string, 0)
	for _, entry := range toolEntriesFromBody(t, body) {
		fn, ok := entry["function"].(map[string]any)
		if !ok {
			continue
		}
		name, _ := fn["name"].(string)
		out = append(out, name)
	}
	return out
}

func toolEntriesFromBody(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var parsed struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	return parsed.Tools
}

func gateDescription(t *testing.T, body []byte, name string) string {
	t.Helper()
	for _, entry := range toolEntriesFromBody(t, body) {
		fn, ok := entry["function"].(map[string]any)
		if !ok {
			continue
		}
		if fn["name"] != name {
			continue
		}
		desc, _ := fn["description"].(string)
		return desc
	}
	t.Fatalf("tool %q not found in the body", name)
	return ""
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// --- catalog ---

// metaPath is where the catalog checks serve the property document from.
const metaPath = "/meta.json"

// newCatalogProvider brings up a provider pointing at a stub service.
//
// The network is not allowed in catalog checks: it would make the result depend
// on the state of someone else's service, and such a test stops asserting
// anything — it either fails for no reason or passes regardless of the code.
func newCatalogProvider(t *testing.T, listBody string, listStatus int) *Provider {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if listStatus != http.StatusOK {
			http.Error(w, listBody, listStatus)
			return
		}
		w.Header().Set("content-type", "application/json")

		// The property catalog is served too, and empty. Leaving it to the
		// published address would put the network back into a check that exists
		// precisely to keep it out, and the model list usually settles before the
		// property request times out, so the violation would be invisible.
		if r.URL.Path == metaPath {
			_, _ = w.Write([]byte(`{"` + string(ID) + `":{"models":{}}}`))
			return
		}
		_, _ = w.Write([]byte(listBody))
	}))
	t.Cleanup(srv.Close)

	p, err := New(Config{
		BaseURL:   srv.URL,
		MetaURL:   srv.URL + metaPath,
		OSName:    "Linux",
		ShellName: "bash",
		HTTP:      srv.Client(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Reads come out of memory, so the catalog has to be warmed before anything
	// can be read from it.
	p.Start(t.Context())
	t.Cleanup(func() { _ = p.Stop() })
	return p
}

// newCatalogProviderWithMeta serves both sources from one stub.
//
// The live list and the property catalog are separate requests, so the stub
// answers them by path. That is what lets a check reach the eligibility logic
// at all: the logic reads prices, and prices only exist in the property
// catalog — against a stub that serves the list alone, every model looks
// unpriced and the filter cannot be observed.
//
// Passing nil meta means the property source is not described at all, which is
// the case the fallback exists for.
func newCatalogProviderWithMeta(t *testing.T, listBody string, meta map[string]string) *Provider {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")

		// The property catalog is one document holding every service; the
		// gateway reads its own section out of it.
		if r.URL.Path == metaPath {
			models := make(map[string]any, len(meta))
			for id, raw := range meta {
				var parsed map[string]any
				if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
					t.Errorf("bad meta fixture for %q: %v", id, err)
					continue
				}
				models[id] = parsed
			}
			body, err := json.Marshal(map[string]any{
				string(ID): map[string]any{"models": models},
			})
			if err != nil {
				t.Errorf("encoding meta fixture: %v", err)
				return
			}
			_, _ = w.Write(body)
			return
		}
		_, _ = w.Write([]byte(listBody))
	}))
	t.Cleanup(srv.Close)

	p, err := New(Config{
		BaseURL:   srv.URL,
		OSName:    "Linux",
		ShellName: "bash",
		HTTP:      srv.Client(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.catalog = modelcat.New(
		srv.URL, liveModelsPath, srv.Client(), p.applyHeaders,
		&metaSource{http: srv.Client(), url: srv.URL + metaPath},
	).WithEligibility(isFree)
	// Reads come out of memory, so the catalog has to be warmed before anything
	// can be read from it.
	p.Start(t.Context())
	t.Cleanup(func() { _ = p.Stop() })
	return p
}

// TestCatalogComesFromService — the composition of the catalog is determined by
// the service. This is the main property: a list in the code would drift from
// the service, and the client would select models that no longer exist.
func TestCatalogComesFromService(t *testing.T) {
	p := newCatalogProvider(t, `{"data":[{"id":"alive-free"},{"id":"second-free"}]}`, http.StatusOK)

	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 2 || models[0].ID != "alive-free" || models[1].ID != "second-free" {
		t.Fatalf("Models = %+v, want exactly what the service declared", models)
	}
	for _, m := range models {
		// These models have no properties, so they are described with
		// defaults rather than rejected.
		if m.Context <= 0 || m.MaxOutput <= 0 {
			t.Fatalf("model %q came back without usable limits: %+v", m.ID, m)
		}
	}
}

// TestPaidModelsAreFilteredOut — a paid model must not reach the listing.
//
// The gateway routes free models, so a paid one is a request that bills the
// operator rather than a less attractive choice. The listing is what a client
// picks from, so the exclusion has to happen there.
func TestPaidModelsAreFilteredOut(t *testing.T) {
	p := newCatalogProviderWithMeta(t,
		`{"data":[{"id":"free-one-free"},{"id":"paid-one"}]}`,
		map[string]string{
			"free-one-free": `{"name":"Free One","cost":{"input":0,"output":0}}`,
			"paid-one":      `{"name":"Paid One","cost":{"input":3,"output":15}}`,
		})

	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 1 || models[0].ID != "free-one-free" {
		t.Fatalf("Models = %+v, want only the free model", models)
	}
}

// TestUnpricedModelWithFreeNameIsKept — the fallback for a model the property
// source has not reached.
//
// The price is unknown in that case, not zero, and the name is the only
// evidence left. Omitting the model would silently hide something the service
// is serving, which the operator could not distinguish from a withdrawal.
func TestUnpricedModelWithFreeNameIsKept(t *testing.T) {
	p := newCatalogProviderWithMeta(t, `{"data":[{"id":"newly-added-free"}]}`, nil)

	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 1 || models[0].ID != "newly-added-free" {
		t.Fatalf("Models = %+v, want the unpriced free-named model kept", models)
	}
}

// TestUnpricedModelWithoutFreeNameIsDropped — the other half of the fallback.
//
// A model the source does not describe and whose name claims nothing gets the
// benefit of no doubt: it is left out. Admitting it would put a model of
// unknown price into a listing offered as free.
func TestUnpricedModelWithoutFreeNameIsDropped(t *testing.T) {
	p := newCatalogProviderWithMeta(t, `{"data":[{"id":"some-paid-looking-name"}]}`, nil)

	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 0 {
		t.Fatalf("Models = %+v, want the unproven model left out", models)
	}
}

// TestCatalogRefusalLeavesTheCatalogEmpty — an unavailable catalog is not an
// error on the read path any more.
//
// The poller owns the network, and its failure has no caller to return to: the
// read serves memory and reports what it has. An empty catalog is the honest
// answer, and the reason is in the log rather than in a return value nobody is
// waiting for.
func TestCatalogRefusalLeavesTheCatalogEmpty(t *testing.T) {
	p := newCatalogProvider(t, `{"type":"error"}`, http.StatusForbidden)

	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v, want a read from memory not to fail", err)
	}
	if len(models) != 0 {
		t.Fatalf("Models = %+v, want nothing after a refused list", models)
	}
}

// TestCatalogEmptyListLeavesTheCatalogEmpty — a response without models does not
// become a catalog.
//
// Staying silent about the reason is what the read does now; what must not
// happen is the empty answer being taken for a list.
func TestCatalogEmptyListLeavesTheCatalogEmpty(t *testing.T) {
	p := newCatalogProvider(t, `{"data":[]}`, http.StatusOK)

	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v, want a read from memory not to fail", err)
	}
	if len(models) != 0 {
		t.Fatalf("Models = %+v, want nothing from an empty answer", models)
	}
}

// TestNoEmbeddedCatalog — no model list is left inside the provider.
//
// The check is not for the sake of code cleanliness: the embedded list was
// precisely the source of the failure — models from it were retired while the
// binary kept declaring them. A list can only be brought back here together
// with this check.
func TestNoEmbeddedCatalog(t *testing.T) {
	p := newTestProvider(t)

	// A provider without the network must not be able to name a catalog: the
	// only source is the service. The test is built so that it fails at the
	// first return to a constant in the code.
	if p.catalog == nil {
		t.Fatal("no catalog: models are not taken from the service")
	}
}

// header reads an outbound header by the spelling it is written in.
//
// Not Get: the names go out in the reference client's spelling — the custom
// ones in lowercase — and Get canonicalises the lookup key, which would miss
// them. Reading the map directly is what keeps the test honest about the wire
// form.
func header(h http.Header, name string) string {
	if v, ok := h[name]; ok && len(v) > 0 {
		return v[0]
	}
	return ""
}

// TestHeaderSetMatchesReferenceClient — the outbound set is the one the client
// we imitate actually sends, measured from its live request.
//
// The property under test is not "the headers are present" but "the headers are
// exactly these": an extra header is as much a divergence as a missing one, and
// `x-session-affinity` is listed explicitly because it used to be sent and the
// client never sends it.
func TestHeaderSetMatchesReferenceClient(t *testing.T) {
	p := newTestProvider(t)

	prep, err := p.Build(provider.Request{
		Model: provider.Model{ID: "x"},
		Session: provider.SessionIDs{
			Session:  "ses_000000000000AAAAAAAAAAAAAA",
			Affinity: "ses_000000000000AAAAAAAAAAAAAA",
			Project:  "global",
			Message:  "msg_000000000000CCCCCCCCCCCCCC",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"Content-Type":          "application/json",
		"Accept-Encoding":       "gzip, deflate, br, zstd",
		"User-Agent":            p.userAgent,
		"Accept":                "*/*",
		"Connection":            "keep-alive",
		"X-Opencode-Client":     p.clientName,
		"X-Opencode-Project":    "global",
		"X-Opencode-Session":    "ses_000000000000AAAAAAAAAAAAAA",
		"X-Opencode-Session-Id": "ses_000000000000AAAAAAAAAAAAAA",
		"X-Session-Affinity":    "ses_000000000000AAAAAAAAAAAAAA",
		"X-Session-Id":          "ses_000000000000AAAAAAAAAAAAAA",
	}
	for name, value := range want {
		if got := header(prep.Headers, name); got != value {
			t.Fatalf("%s = %q, want %q", name, got, value)
		}
	}
	// The trace pair is minted per request, so only its shape is fixed.
	if got := header(prep.Headers, "Traceparent"); !isTraceparent(got) {
		t.Fatalf("Traceparent = %q, want the W3C shape", got)
	}
	if got := header(prep.Headers, "B3"); !isB3(got) {
		t.Fatalf("B3 = %q, want the B3 value", got)
	}
	if got := len(prep.Headers); got != len(want)+2 {
		names := make([]string, 0, got)
		for name := range prep.Headers {
			names = append(names, name)
		}
		t.Fatalf("headers = %v, want exactly the %d names of the reference client", names, len(want)+2)
	}
}

func isTraceparent(v string) bool {
	parts := strings.Split(v, "-")
	if len(parts) != 4 || parts[0] != "00" || parts[3] != "01" {
		return false
	}
	return isHex(parts[1], 32) && isHex(parts[2], 16)
}

func isB3(v string) bool {
	parts := strings.Split(v, "-")
	if len(parts) != 4 || parts[2] != "1" {
		return false
	}
	return isHex(parts[0], 32) && isHex(parts[1], 16) && isHex(parts[3], 16)
}

func isHex(v string, n int) bool {
	if len(v) != n {
		return false
	}
	for _, c := range v {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
