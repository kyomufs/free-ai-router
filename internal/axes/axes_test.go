package axes

import (
	"encoding/json"
	"strings"
	"testing"

	"free-ai-router/internal/config"
)

// The registries are passed in, so the test supplies its own: the catalog must
// describe the axes without depending on which providers happen to be built in.
func testRegistries() Registries {
	return Registries{Providers: []string{"opencode", "stub"}, Transports: []string{"direct"}}
}

// TestEveryAxisIsComplete fails on an axis that cannot be used.
//
// An axis without a name cannot be found, without a kind cannot be read, and
// without a description cannot be understood; whichever of the three is missing,
// the entry is a decoration rather than a setting.
func TestEveryAxisIsComplete(t *testing.T) {
	for _, a := range Catalog(testRegistries()) {
		if a.Name == "" {
			t.Fatalf("an axis has no name: %+v", a)
		}
		if a.Kind == "" {
			t.Errorf("%s: no kind", a.Name)
		}
		if a.Description == "" {
			t.Errorf("%s: no description", a.Name)
		}
		if a.Scope != ScopeGlobal && a.Scope != ScopeProvider {
			t.Errorf("%s: unexpected scope %q", a.Name, a.Scope)
		}
	}
}

// TestNamesAreUnique keeps the dictionary a dictionary.
//
// Two entries under one name would make Find answer with whichever came first,
// and the NixOS check would compare the module's options against an entry nobody
// reads.
func TestNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range Catalog(testRegistries()) {
		if seen[a.Name] {
			t.Errorf("duplicate axis %q", a.Name)
		}
		seen[a.Name] = true
	}
}

// TestConfigKeyAndFileBackingAgree.
//
// An axis that claims a key in the file must have one, and an axis outside the
// file must not — otherwise a consumer rendering the file writes a value that
// changes nothing, which is the failure this whole dictionary exists to prevent.
func TestConfigKeyAndFileBackingAgree(t *testing.T) {
	for _, a := range Catalog(testRegistries()) {
		switch {
		case a.InConfigFile && a.ConfigKey == "":
			t.Errorf("%s: backed by the file but names no key", a.Name)
		case !a.InConfigFile && a.ConfigKey != "":
			t.Errorf("%s: outside the file but names the key %q", a.Name, a.ConfigKey)
		case !a.InConfigFile && a.Scope != ScopeGlobal:
			t.Errorf("%s: only a global axis can live outside the file", a.Name)
		}
	}
}

// TestPathAxesAreTheOnlyOnesOutsideTheFile states which axes those are.
//
// The two overrides choose WHICH file is read: put inside the file, they would
// have to be read from the file they select.
func TestPathAxesAreTheOnlyOnesOutsideTheFile(t *testing.T) {
	const want = 2
	var outside []string
	for _, a := range Catalog(testRegistries()) {
		if a.InConfigFile {
			continue
		}
		outside = append(outside, a.Name)
		if a.Kind != KindPath {
			t.Errorf("%s: outside the file but not a path", a.Name)
		}
	}
	if len(outside) != want {
		t.Errorf("outside the file: %v, want %d axes", outside, want)
	}
	if strings.Join(outside, ",") != "config,state" {
		t.Errorf("outside the file: %v, want config and state", outside)
	}
}

// TestEnumDefaultsAreAllowed catches a default that is not a value.
//
// The pairing is easy to break while editing one list and forgetting the other,
// and the failure would only show up as a daemon that refuses to start with its
// own default configuration.
func TestEnumDefaultsAreAllowed(t *testing.T) {
	for _, a := range Catalog(testRegistries()) {
		if a.Kind != KindEnum {
			continue
		}
		if len(a.Values) == 0 {
			t.Errorf("%s: an enum with no values", a.Name)
			continue
		}
		// A provider-scoped axis has no default of its own: an empty value means
		// "inherit the global axis".
		if a.Scope == ScopeProvider {
			if a.Default != "" {
				t.Errorf("%s: a provider-scoped axis must default to empty, got %q", a.Name, a.Default)
			}
			continue
		}
		if !contains(a.Values, a.Default) {
			t.Errorf("%s: default %q is not one of %v", a.Name, a.Default, a.Values)
		}
	}
}

// TestGlobalAxesHaveBothWaysToSetThem is requirement 2 stated as a test.
//
// Every global axis must be settable without a file and without the module: a
// flag for the current run and a variable for the environment it is started in.
// An axis with only one of the two works in one deployment and not the other.
func TestGlobalAxesHaveBothWaysToSetThem(t *testing.T) {
	for _, a := range Catalog(testRegistries()) {
		if a.Scope != ScopeGlobal {
			continue
		}
		if a.Flag == "" {
			t.Errorf("%s: no flag", a.Name)
		}
		if a.Env == "" {
			t.Errorf("%s: no environment variable", a.Name)
		}
		if !strings.HasPrefix(a.Env, "FREE_AI_ROUTER_") {
			t.Errorf("%s: variable %q is outside the application's namespace", a.Name, a.Env)
		}
	}
}

// TestValueListsComeFromTheConfiguration pins the single-source rule.
//
// The catalog must read the same slices validation ranges over. Written out by
// hand here, the lists would agree until one of them was edited — and then the
// module would offer a value the daemon refuses, or hide one it accepts.
func TestValueListsComeFromTheConfiguration(t *testing.T) {
	r := testRegistries()
	cases := []struct {
		name  string
		want  []string
		scope Scope
	}{
		{"mode", config.ModeValues(), ScopeGlobal},
		{"credential", config.CredentialValues(), ScopeGlobal},
		{"key_rotation", config.RotationValues(), ScopeGlobal},
		{"family", config.FamilyValues(), ScopeGlobal},
		{"provider.credential", config.CredentialValues(), ScopeProvider},
		{"provider.key_rotation", config.RotationValues(), ScopeProvider},
	}
	for _, c := range cases {
		a, ok := Find(r, c.name)
		if !ok {
			t.Errorf("%s: not in the catalog", c.name)
			continue
		}
		if a.Scope != c.scope {
			t.Errorf("%s: scope is %q, want %q", c.name, a.Scope, c.scope)
		}
		if strings.Join(a.Values, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: values are %v, want %v", c.name, a.Values, c.want)
		}
	}
}

// TestRegistriesFillTheEnums keeps the two registries connected to the catalog.
//
// The transport and provider types are the one part of the dictionary that cannot
// come from a constant: they are whatever was registered. An empty enum would
// read as "this setting accepts no value", which is the opposite of the truth.
func TestRegistriesFillTheEnums(t *testing.T) {
	a, ok := Find(testRegistries(), "transport")
	if !ok {
		t.Fatal("transport: not in the catalog")
	}
	if len(a.Values) != 1 || a.Values[0] != "direct" {
		t.Errorf("transport values are %v, want [direct]", a.Values)
	}

	// With nothing registered, the axis still has to describe something.
	bare, ok := Find(Registries{}, "transport")
	if !ok {
		t.Fatal("transport: not in the catalog")
	}
	if len(bare.Values) == 0 {
		t.Error("transport: an empty registry left the enum empty")
	}

	p, ok := Find(testRegistries(), "provider.type")
	if !ok {
		t.Fatal("provider.type: not in the catalog")
	}
	if strings.Join(p.Values, ",") != "opencode,stub" {
		t.Errorf("provider.type values are %v, want the registered types", p.Values)
	}
}

// TestCatalogIsIndependent per call.
//
// A shared slice would let one caller's append widen the axis for every other
// caller in the process — including the code that validates it.
func TestCatalogIsIndependent(t *testing.T) {
	first := Catalog(testRegistries())
	first[0].Values = append(first[0].Values, "injected")

	second := Catalog(testRegistries())
	for _, v := range second[0].Values {
		if v == "injected" {
			t.Fatal("a value appended by one caller reached the next call")
		}
	}
}

// TestJSONIsTheDocumentTheModuleReads.
//
// The NixOS check parses this output by field name, so the shape is a contract.
func TestJSONIsTheDocumentTheModuleReads(t *testing.T) {
	raw, err := JSON(testRegistries())
	if err != nil {
		t.Fatal(err)
	}

	var doc struct {
		Version    int    `json:"version"`
		Axes       []Axis `json:"axes"`
		Registries struct {
			Providers  []string `json:"providers"`
			Transports []string `json:"transports"`
		} `json:"registries"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the output is not valid JSON: %v", err)
	}
	if doc.Version != Version {
		t.Errorf("version is %d, want %d", doc.Version, Version)
	}
	if len(doc.Axes) == 0 {
		t.Fatal("no axes in the document")
	}
	if strings.Join(doc.Registries.Transports, ",") != "direct" {
		t.Errorf("transports are %v", doc.Registries.Transports)
	}

	// The credential axis has to survive the round trip with its values: that is
	// what the module compares its own option against.
	var found bool
	for _, a := range doc.Axes {
		if a.Name != "credential" {
			continue
		}
		found = true
		if strings.Join(a.Values, ",") != strings.Join(config.CredentialValues(), ",") {
			t.Errorf("credential values are %v", a.Values)
		}
		if a.Kind != KindEnum {
			t.Errorf("credential kind is %q", a.Kind)
		}
	}
	if !found {
		t.Error("the credential axis did not survive the encoding")
	}
}

// TestTextNamesEveryAxis keeps the human output from quietly dropping entries.
func TestTextNamesEveryAxis(t *testing.T) {
	out := Text(testRegistries())
	for _, a := range Catalog(testRegistries()) {
		if !strings.Contains(out, a.Name) {
			t.Errorf("%s: missing from the text output", a.Name)
		}
	}
}

// TestFindReportsAbsence — an unknown name is found nowhere.
func TestFindReportsAbsence(t *testing.T) {
	if _, ok := Find(testRegistries(), "nonsense"); ok {
		t.Error("an unknown axis was reported as present")
	}
}

// contains reports whether the slice holds the value.
func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
