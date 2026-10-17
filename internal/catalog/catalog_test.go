package catalog

import (
	"strings"
	"testing"

	"free-ai-router/internal/config"
	"free-ai-router/internal/provider"
)

// cfgWith собирает конфигурацию с одной секцией провайдера.
func cfgWith(t *testing.T, id string, pc config.ProviderConfig) *config.Config {
	t.Helper()
	c := config.Default()
	c.Providers = map[string]config.ProviderConfig{id: pc}
	return c
}

// TestBuildsEnabledProviders — включённые секции попадают в реестр.
func TestBuildsEnabledProviders(t *testing.T) {
	c := cfgWith(t, "opencode", config.ProviderConfig{Type: "opencode"})

	reg, err := Build(c)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if reg.Len() != 1 {
		t.Fatalf("Len = %d, want 1", reg.Len())
	}
	p, err := reg.Get("opencode")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.ID() != "opencode" {
		t.Fatalf("ID = %q, want opencode", p.ID())
	}

	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) == 0 {
		t.Fatal("the opencode provider came up with an empty catalog")
	}
}

// TestDisabledProvidersSkipped — выключенная секция не мешает работать
// остальным.
func TestDisabledProvidersSkipped(t *testing.T) {
	off := false
	c := config.Default()
	c.Providers = map[string]config.ProviderConfig{
		"opencode": {Type: "opencode"},
		"выключен": {Type: "opencode", Enabled: &off},
	}

	reg, err := Build(c)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if reg.Len() != 1 {
		t.Fatalf("Len = %d, want 1 (the disabled entry must be skipped)", reg.Len())
	}
	if _, err := reg.Get("выключен"); err == nil {
		t.Fatal("the disabled provider reached the registry")
	}
}

// TestEmptyConfigurationIsAnError — без провайдеров шлюзу нечего делать, и это
// должно быть сказано прямо, а не выясняться при первом запросе.
func TestEmptyConfigurationIsAnError(t *testing.T) {
	c := config.Default()
	if _, err := Build(c); err == nil {
		t.Fatal("an empty configuration must be an error")
	}

	off := false
	c.Providers = map[string]config.ProviderConfig{"выключен": {Type: "opencode", Enabled: &off}}
	if _, err := Build(c); err == nil {
		t.Fatal("a configuration with only disabled providers must be an error")
	}
}

// TestUnknownTypeNamesTheKnownOnes — опечатка в типе должна сразу показать,
// какие типы существуют.
func TestUnknownTypeNamesTheKnownOnes(t *testing.T) {
	c := cfgWith(t, "мой", config.ProviderConfig{Type: "zenmux"})

	_, err := Build(c)
	if err == nil {
		t.Fatal("an unknown type must fail")
	}
	msg := err.Error()
	for _, known := range KnownTypes() {
		if !strings.Contains(msg, known) {
			t.Fatalf("the error %q does not mention the known type %q", msg, known)
		}
	}
}

// TestMissingTypeIsAnError — секция без типа не собирается: неизвестно, каким
// кодом её обслуживать.
func TestMissingTypeIsAnError(t *testing.T) {
	c := cfgWith(t, "мой", config.ProviderConfig{})
	if _, err := Build(c); err == nil {
		t.Fatal("a section without a type must fail")
	}
}

// TestOpenAICompatibleProviderBuilds — точка расширения: новый сервис
// подключается секцией, без кода.
func TestOpenAICompatibleProviderBuilds(t *testing.T) {
	c := cfgWith(t, "zenmux", config.ProviderConfig{
		Type:    "openai",
		BaseURL: "https://zenmux.ai/api/v1",
		Models: []config.ModelConfig{
			{ID: "free-model", Context: 128000, MaxOutput: 32000, Reasoning: true},
		},
	})

	reg, err := Build(c)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	p, err := reg.Get("zenmux")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.ID() != "zenmux" {
		t.Fatalf("ID = %q, want the section name", p.ID())
	}

	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "free-model" {
		t.Fatalf("Models = %+v", models)
	}
	if !models[0].Free {
		t.Fatal("a model declared in the configuration must be marked free")
	}
}

// TestProviderIDMatchesSectionName — имя маршрута берётся из секции. Если
// конструктор вернёт другое имя, клиент не найдёт провайдера.
func TestProviderIDMatchesSectionName(t *testing.T) {
	c := config.Default()
	c.Providers = map[string]config.ProviderConfig{
		"первый": {Type: "openai", BaseURL: "https://a.example/v1"},
		"второй": {Type: "openai", BaseURL: "https://b.example/v1"},
	}

	reg, err := Build(c)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, id := range []string{"первый", "второй"} {
		p, err := reg.Get(provider.ID(id))
		if err != nil {
			t.Fatalf("Get(%q): %v", id, err)
		}
		if string(p.ID()) != id {
			t.Fatalf("provider %q reports ID %q", id, p.ID())
		}
	}
}

// TestOpenAIRequiresBaseURL — у сервиса на общем протоколе нет адреса по
// умолчанию: он у каждого свой.
func TestOpenAIRequiresBaseURL(t *testing.T) {
	c := config.Default()
	c.Upstream.Val = ""
	c.Providers = map[string]config.ProviderConfig{"мой": {Type: "openai"}}

	if _, err := Build(c); err == nil {
		t.Fatal("an openai-compatible provider without a base_url must fail")
	}
}

// TestOpenCodeDefaultsToItsKnownURL — у opencode адрес известен, и его не
// нужно повторять в конфигурации.
func TestOpenCodeDefaultsToItsKnownURL(t *testing.T) {
	c := cfgWith(t, "opencode", config.ProviderConfig{Type: "opencode"})

	reg, err := Build(c)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	p, err := reg.Get("opencode")
	if err != nil {
		t.Fatal(err)
	}
	prep, err := p.Build(provider.Request{Model: provider.Model{ID: "x"}})
	if err != nil {
		t.Fatalf("Build request: %v", err)
	}
	if !strings.HasPrefix(prep.URL, "https://opencode.ai/") {
		t.Fatalf("URL = %q, want the known opencode address", prep.URL)
	}
}

// TestSettingsForwardedToProvider — настройки секции доезжают до провайдера.
func TestSettingsForwardedToProvider(t *testing.T) {
	c := cfgWith(t, "мой", config.ProviderConfig{
		Type:    "openai",
		BaseURL: "https://example.test/v1",
		Settings: map[string]any{
			"chat_path":  "/custom/chat",
			"user_agent": "мой-агент/1.0",
		},
	})

	reg, err := Build(c)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	p, _ := reg.Get("мой")
	prep, err := p.Build(provider.Request{Model: provider.Model{ID: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(prep.URL, "/custom/chat") {
		t.Fatalf("URL = %q, want the configured chat path", prep.URL)
	}
	if got := prep.Headers.Get("user-agent"); got != "мой-агент/1.0" {
		t.Fatalf("user-agent = %q", got)
	}
}

// TestBuildDoesNotMutateConfiguration — сборка не должна менять конфигурацию:
// её читают и другие потребители.
func TestBuildDoesNotMutateConfiguration(t *testing.T) {
	settings := map[string]any{"chat_path": "/original"}
	c := cfgWith(t, "мой", config.ProviderConfig{
		Type:     "openai",
		BaseURL:  "https://example.test/v1",
		Settings: settings,
	})

	if _, err := Build(c); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if settings["chat_path"] != "/original" {
		t.Fatal("Build mutated the caller's settings map")
	}
}

// TestModelsWithoutIDDropped — модель без идентификатора бессмысленна и
// отбрасывается, а не превращается в запись с пустым именем.
func TestModelsWithoutIDDropped(t *testing.T) {
	c := cfgWith(t, "мой", config.ProviderConfig{
		Type:    "openai",
		BaseURL: "https://example.test/v1",
		Models: []config.ModelConfig{
			{ID: "хорошая"},
			{ID: "   "},
			{},
		},
	})

	reg, err := Build(c)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	p, _ := reg.Get("мой")
	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "хорошая" {
		t.Fatalf("Models = %+v, want only the complete entry", models)
	}
}

// TestRegistryOrderIsStable — порядок в реестре не должен зависеть от обхода
// map: списки в CLI и TUI иначе прыгали бы между запусками.
func TestRegistryOrderIsStable(t *testing.T) {
	c := config.Default()
	c.Providers = map[string]config.ProviderConfig{
		"в": {Type: "openai", BaseURL: "https://v.test/v1"},
		"а": {Type: "openai", BaseURL: "https://a.test/v1"},
		"б": {Type: "openai", BaseURL: "https://b.test/v1"},
	}

	reg, err := Build(c)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	ids := reg.IDs()
	want := []provider.ID{"а", "б", "в"}
	if len(ids) != len(want) {
		t.Fatalf("IDs = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("IDs = %v, want %v", ids, want)
		}
	}
}
