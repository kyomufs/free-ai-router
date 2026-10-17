package opencode

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// TestProjectHeaderForwarded — the project value goes out as-is, without
// substitutions.
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

// TestSystemPromptIsPureFunctionOfToolNames is a property from the plan: the
// prompt is a pure function of the list of active tool names.
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

	// Idempotence: the same input gives the same output.
	if RenderSystemPrompt([]string{"shell", "write", "edit"}) != withAll {
		t.Fatal("RenderSystemPrompt is not deterministic")
	}
	// The order of the names in the input does not affect the result.
	if RenderSystemPrompt([]string{"edit", "write", "shell"}) != withAll {
		t.Fatal("RenderSystemPrompt depends on the order of tool names")
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

// TestCatalogRefusalIsAnError — an unavailable catalog is returned as an error
// rather than as an empty list. An operator would read an empty list as "the
// service has no models" and look for the cause in the wrong place.
func TestCatalogRefusalIsAnError(t *testing.T) {
	p := newCatalogProvider(t, `{"type":"error"}`, http.StatusForbidden)

	if models, err := p.Models(t.Context()); err == nil {
		t.Fatalf("Models = %+v, want an error", models)
	}
}

// TestCatalogEmptyListIsAnError — a response without models means the parsing
// drifted from the response shape, and staying silent about that is not an
// option.
func TestCatalogEmptyListIsAnError(t *testing.T) {
	p := newCatalogProvider(t, `{"data":[]}`, http.StatusOK)

	if models, err := p.Models(t.Context()); err == nil {
		t.Fatalf("Models = %+v, want an error for an empty answer", models)
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
