package opencode

import (
	"encoding/json"
	"strings"
	"testing"

	"free-ai-router/internal/provider"
)

// newTestProvider создаёт провайдера с предсказуемыми подстановками, чтобы
// утверждения не зависели от окружения машины.
func newTestProvider(t *testing.T) *Provider {
	t.Helper()
	p, err := New(Config{OSName: "Linux", ShellName: "bash"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

// TestGateToolsAlwaysPresent — свойство контракта: запрос обязан нести оба
// обязательных инструмента, даже если клиент не прислал ни одного.
func TestGateToolsAlwaysPresent(t *testing.T) {
	p := newTestProvider(t)

	cases := []struct {
		name  string
		tools []provider.Tool
	}{
		{"клиент не прислал инструментов", nil},
		{"клиент прислал один посторонний", []provider.Tool{{Name: "grep", Description: "search"}}},
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

// TestGateToolReplacedWhenCallerSendsOwn — клиент может прислать инструмент с
// правильным именем и чужим содержимым. Проверка сверяет записи целиком,
// поэтому такая запись обязана быть ЗАМЕНЕНА канонической, а не оставлена.
func TestGateToolReplacedWhenCallerSendsOwn(t *testing.T) {
	p := newTestProvider(t)

	prep, err := p.Build(provider.Request{
		Model: provider.Model{ID: "mimo-v2.6-flash-free"},
		Tools: []provider.Tool{{
			Name:        gateNameShell,
			Description: "зову себя bash и ничего не умею",
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

// TestGateShellDescriptionCarriesOSAndShell: описание инструмента
// параметризовано системой и оболочкой, и подстановка обязана произойти.
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

// TestToolChoiceFollowsClientSignalledChoice — форма, которую шлёт клиент.
//
// Обязательные инструменты добавляются ВСЕГДА, поэтому массив `tools` в теле
// непуст даже когда клиент не прислал ничего. Из этого следует, что
// `tool_choice` не может зависеть от «прислал ли клиент инструменты»: он
// зависит только от того, задал ли клиент требование.
//
// Проверяется обе половины: без требования поля нет вовсе; с требованием оно
// доезжает дословно.
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
	// При этом сами инструменты на месте: их наличие не зависит от клиента.
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

// TestSessionAndAffinityAreDistinct — свойство контракта: заголовки сессии и
// affinity несут РАЗНЫЕ значения, когда сессия вложенная.
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

	got := prep.Headers.Get("x-opencode-session-id")
	want := "ses_000000000000AAAAAAAAAAAAAA"
	if got != want {
		t.Fatalf("x-opencode-session-id = %q, want %q", got, want)
	}
	if got := prep.Headers.Get("x-opencode-session"); got == want {
		t.Fatalf("x-opencode-session must carry the affinity, not the session id")
	}
	if got := prep.Headers.Get("x-session-affinity"); got != "ses_000000000000BBBBBBBBBBBBBB" {
		t.Fatalf("x-session-affinity = %q, want the affinity", got)
	}
}

// TestParentHeadersOnlyForNestedSessions — заголовки родителя присутствуют
// тогда и только тогда, когда родитель есть.
func TestParentHeadersOnlyForNestedSessions(t *testing.T) {
	p := newTestProvider(t)

	flat, err := p.Build(provider.Request{
		Model:   provider.Model{ID: "x"},
		Session: provider.SessionIDs{Session: "ses_000000000000AAAAAAAAAAAAAA"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if flat.Headers.Get("x-opencode-parent-session-id") != "" {
		t.Fatalf("a flat session must not carry x-opencode-parent-session-id")
	}
	if flat.Headers.Get("x-parent-session-id") != "" {
		t.Fatalf("a flat session must not carry x-parent-session-id")
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
	if got := nested.Headers.Get("x-opencode-parent-session-id"); got != "ses_000000000000DDDDDDDDDDDDDD" {
		t.Fatalf("x-opencode-parent-session-id = %q, want the parent", got)
	}
	if got := nested.Headers.Get("x-parent-session-id"); got != "ses_000000000000DDDDDDDDDDDDDD" {
		t.Fatalf("x-parent-session-id = %q, want the parent", got)
	}
}

// TestProjectHeaderForwarded — значение проекта уходит как есть, без подстановок.
func TestProjectHeaderForwarded(t *testing.T) {
	p := newTestProvider(t)

	for _, project := range []string{"global", "prj_abcdef0123456789"} {
		prep, err := p.Build(provider.Request{
			Model:   provider.Model{ID: "x"},
			Session: provider.SessionIDs{Session: "ses_000000000000AAAAAAAAAAAAAA", Project: project},
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := prep.Headers.Get("x-opencode-project"); got != project {
			t.Fatalf("x-opencode-project = %q, want %q", got, project)
		}
	}
}

// TestReasoningFieldFromModel — размышления пишутся в то поле, которое
// объявила модель, а не в произвольное.
func TestReasoningFieldFromModel(t *testing.T) {
	p := newTestProvider(t)

	prep, err := p.Build(provider.Request{
		Model: provider.Model{ID: "x", ReasoningField: "reasoning_content"},
		Messages: []provider.Message{
			{Role: "user", Text: "привет"},
			{Role: "assistant", Text: "ответ", Reasoning: "размышление"},
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
		if m["reasoning_content"] != "размышление" {
			t.Fatalf("reasoning landed in the wrong field: %v", m)
		}
	}
	if !found {
		t.Fatal("no assistant message in the body")
	}
}

// TestEmptyAssistantDropped — пустой ход ассистента на проводе недопустим и
// обязан быть отброшен.
func TestEmptyAssistantDropped(t *testing.T) {
	p := newTestProvider(t)

	prep, err := p.Build(provider.Request{
		Model: provider.Model{ID: "x"},
		Messages: []provider.Message{
			{Role: "user", Text: "привет"},
			{Role: "assistant"},
			{Role: "user", Text: "ещё"},
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

// TestSystemPromptIsPureFunctionOfToolNames — свойство из плана: промпт есть
// чистая функция от списка имён активных инструментов.
func TestSystemPromptIsPureFunctionOfToolNames(t *testing.T) {
	base := RenderSystemPrompt(nil)
	if strings.Contains(base, "Prefer dedicated tools over shell commands") {
		t.Fatal("guidance about shell leaked into a prompt with no shell tool")
	}
	if !strings.Contains(base, "${OPENCODE_TOOL_GUIDANCE}") && !strings.Contains(base, "# Harness") {
		t.Fatal("the template did not render at all")
	}

	withShell := RenderSystemPrompt([]string{"shell"})
	if !strings.Contains(withShell, "Prefer dedicated tools over shell commands") {
		t.Fatal("shell guidance missing when the shell tool is active")
	}

	withAll := RenderSystemPrompt([]string{"shell", "write", "edit"})
	for _, want := range []string{
		"Prefer dedicated tools over shell commands",
		"Use the write tool to create files",
		"Use the edit tool for targeted changes",
	} {
		if !strings.Contains(withAll, want) {
			t.Fatalf("guidance %q missing with all tools active", want)
		}
	}

	// Идемпотентность: один и тот же вход даёт один и тот же выход.
	if RenderSystemPrompt([]string{"shell", "write", "edit"}) != withAll {
		t.Fatal("RenderSystemPrompt is not deterministic")
	}
	// Порядок имён на входе не влияет на результат.
	if RenderSystemPrompt([]string{"edit", "write", "shell"}) != withAll {
		t.Fatal("RenderSystemPrompt depends on the order of tool names")
	}
}

// TestBuiltinModelsAreUsable — каталог непуст и у каждой модели есть
// обязательные поля.
func TestBuiltinModelsAreUsable(t *testing.T) {
	p := newTestProvider(t)
	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) == 0 {
		t.Fatal("the builtin catalog is empty")
	}
	for _, m := range models {
		if m.ID == "" {
			t.Fatalf("a model has no id: %+v", m)
		}
		if m.Context <= 0 {
			t.Fatalf("model %q has a non-positive context window", m.ID)
		}
		if !m.Free {
			t.Fatalf("model %q from the builtin catalog is not marked free", m.ID)
		}
	}
}

// TestModelsAreCopied — вызывающий не должен получать доступ к внутреннему
// срезу: иначе правка каталога извне сломала бы провайдера.
func TestModelsAreCopied(t *testing.T) {
	p := newTestProvider(t)
	first, err := p.Models(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 {
		t.Fatal("empty catalog")
	}
	first[0].ID = "подменённый"

	second, err := p.Models(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if second[0].ID == "подменённый" {
		t.Fatal("the catalog slice is shared with the caller")
	}
}

// --- вспомогательные ---

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
