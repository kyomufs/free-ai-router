package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeConfig кладёт файл во временный каталог и возвращает пути.
func writeConfig(t *testing.T, body string) Paths {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Paths{
		ConfigDir:  dir,
		StateDir:   dir,
		RuntimeDir: dir,
		ConfigFile: path,
		StateFile:  filepath.Join(dir, "state.json"),
		LogFile:    filepath.Join(dir, "app.log"),
	}
}

// TestDefaultsWhenFileAbsent — отсутствующий файл не ошибка: умолчания
// работают сами.
func TestDefaultsWhenFileAbsent(t *testing.T) {
	dir := t.TempDir()
	paths := Paths{ConfigFile: filepath.Join(dir, "нет-файла.json"), StateFile: filepath.Join(dir, "s.json")}

	c, err := LoadFrom(paths)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if c.Mode.Val != DefaultMode {
		t.Fatalf("Mode = %q, want %q", c.Mode.Val, DefaultMode)
	}
	if c.Mode.From != SourceDefault {
		t.Fatalf("Mode source = %q, want %q", c.Mode.From, SourceDefault)
	}
	if c.Listen.Val != DefaultListen {
		t.Fatalf("Listen = %q, want %q", c.Listen.Val, DefaultListen)
	}
}

// TestFileOverridesDefaults — файл накладывается на умолчания, а источник
// значения это отражает.
func TestFileOverridesDefaults(t *testing.T) {
	paths := writeConfig(t, `{
		"mode": "server",
		"listen": "0.0.0.0:9000",
		"token": "секрет",
		"transport": "direct",
		"first_event_timeout": "45s"
	}`)

	c, err := LoadFrom(paths)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if c.Mode.Val != ModeServer || c.Mode.From != SourceFile {
		t.Fatalf("Mode = %q from %q, want %q from %q", c.Mode.Val, c.Mode.From, ModeServer, SourceFile)
	}
	if c.Listen.Val != "0.0.0.0:9000" {
		t.Fatalf("Listen = %q", c.Listen.Val)
	}
	if c.FirstEventTimeout.Val != 45*time.Second {
		t.Fatalf("FirstEventTimeout = %v, want 45s", c.FirstEventTimeout.Val)
	}
	// Поля, которых нет в файле, остаются умолчаниями.
	if c.IdleTimeout.Val != DefaultIdleTimeout || c.IdleTimeout.From != SourceDefault {
		t.Fatalf("IdleTimeout = %v from %q, want the default", c.IdleTimeout.Val, c.IdleTimeout.From)
	}
}

// TestEnvOverridesFile — окружение перекрывает файл.
func TestEnvOverridesFile(t *testing.T) {
	paths := writeConfig(t, `{"mode": "local", "listen": "127.0.0.1:8000"}`)

	t.Setenv(EnvListen, "127.0.0.1:9999")

	c, err := LoadFrom(paths)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if c.Listen.Val != "127.0.0.1:9999" {
		t.Fatalf("Listen = %q, want the env value", c.Listen.Val)
	}
	if c.Listen.From != SourceEnv {
		t.Fatalf("Listen source = %q, want %q", c.Listen.From, SourceEnv)
	}
	// Значение из файла, не тронутое окружением, сохраняет свой источник.
	if c.Mode.From != SourceFile {
		t.Fatalf("Mode source = %q, want %q", c.Mode.From, SourceFile)
	}
}

// TestFlagsOverrideEverything — флаги старше всего остального.
func TestFlagsOverrideEverything(t *testing.T) {
	paths := writeConfig(t, `{"listen": "127.0.0.1:8000"}`)
	t.Setenv(EnvListen, "127.0.0.1:9999")

	c, err := LoadFrom(paths)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	c.ApplyFlags("", "127.0.0.1:7777", "", "", "")

	if c.Listen.Val != "127.0.0.1:7777" {
		t.Fatalf("Listen = %q, want the flag value", c.Listen.Val)
	}
	if c.Listen.From != SourceFlag {
		t.Fatalf("Listen source = %q, want %q", c.Listen.From, SourceFlag)
	}
}

// TestEmptyFlagsDoNotOverride — пустой флаг означает «не задан», а не
// «установить пусто». Иначе запуск без флагов обнулял бы конфигурацию.
func TestEmptyFlagsDoNotOverride(t *testing.T) {
	paths := writeConfig(t, `{"listen": "127.0.0.1:8000"}`)
	c, err := LoadFrom(paths)
	if err != nil {
		t.Fatal(err)
	}
	c.ApplyFlags("", "", "", "", "")

	if c.Listen.Val != "127.0.0.1:8000" || c.Listen.From != SourceFile {
		t.Fatalf("an empty flag changed the value: %q from %q", c.Listen.Val, c.Listen.From)
	}
}

// TestServerModeRequiresToken — инвариант между осями. Без него шлюз стал бы
// открытым релеем.
func TestServerModeRequiresToken(t *testing.T) {
	paths := writeConfig(t, `{"mode": "server", "listen": "0.0.0.0:8787"}`)
	if _, err := LoadFrom(paths); err == nil {
		t.Fatal("server mode without a token must fail")
	}

	paths = writeConfig(t, `{"mode": "server", "listen": "0.0.0.0:8787", "token": "x"}`)
	if _, err := LoadFrom(paths); err != nil {
		t.Fatalf("server mode with a token must load: %v", err)
	}
}

// TestLocalModeNeedsNoToken — в local проверка не действует вовсе.
func TestLocalModeNeedsNoToken(t *testing.T) {
	paths := writeConfig(t, `{"mode": "local"}`)
	c, err := LoadFrom(paths)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if c.IsServer() {
		t.Fatal("local mode must not be treated as server")
	}
}

// TestInvalidValuesAreRejected — опечатка обязана всплыть, а не привести к
// молчаливому откату к умолчаниям.
func TestInvalidValuesAreRejected(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"битый JSON", `{"mode":`},
		{"неизвестный режим", `{"mode": "облако"}`},
		{"пустой listen", `{"listen": "   "}`},
		{"пустой transport", `{"transport": ""}`},
		{"неверная длительность", `{"first_event_timeout": "быстро"}`},
		{"нулевая длительность", `{"idle_timeout": "0s"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			paths := writeConfig(t, tc.body)
			if _, err := LoadFrom(paths); err == nil {
				t.Fatalf("config %s was accepted, want an error", tc.body)
			}
		})
	}
}

// TestProviderKeyEnvPreferredOverNothing — при отсутствии ключа в файле
// берётся переменная, названная рядом.
func TestProviderKeyEnvPreferredOverNothing(t *testing.T) {
	t.Setenv("TEST_PROVIDER_KEY", "из-переменной")

	p := ProviderConfig{APIKeyEnv: "TEST_PROVIDER_KEY"}
	if got := p.ProviderKey(); got != "из-переменной" {
		t.Fatalf("ProviderKey() = %q, want the environment value", got)
	}

	// Ключ в файле сильнее переменной: оператор, написавший его явно, ждёт
	// именно его.
	p.APIKey = "из-файла"
	if got := p.ProviderKey(); got != "из-файла" {
		t.Fatalf("ProviderKey() = %q, want the file value", got)
	}
}

// TestProviderEnabledByDefault — добавленная секция работает, пока её явно не
// выключили.
func TestProviderEnabledByDefault(t *testing.T) {
	var p ProviderConfig
	if !p.IsEnabled() {
		t.Fatal("a provider without the enabled field must be enabled")
	}
	off := false
	p.Enabled = &off
	if p.IsEnabled() {
		t.Fatal("an explicitly disabled provider must be disabled")
	}
}

// TestProvidersParsedFromFile — секция провайдеров читается вместе с моделями
// и настройками.
func TestProvidersParsedFromFile(t *testing.T) {
	paths := writeConfig(t, `{
		"providers": {
			"zenmux": {
				"type": "openai",
				"base_url": "https://zenmux.ai/api/v1",
				"api_key_env": "ZENMUX_API_KEY",
				"models": [
					{"id": "free-model", "context": 128000, "max_output": 32000}
				],
				"settings": {"chat_path": "/chat/completions"}
			},
			"выключенный": {
				"type": "openai",
				"enabled": false
			}
		}
	}`)

	c, err := LoadFrom(paths)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if len(c.Providers) != 2 {
		t.Fatalf("Providers = %d entries, want 2", len(c.Providers))
	}

	zm, ok := c.Providers["zenmux"]
	if !ok {
		t.Fatal("the zenmux provider is missing")
	}
	if zm.Type != "openai" {
		t.Fatalf("Type = %q", zm.Type)
	}
	if zm.BaseURL != "https://zenmux.ai/api/v1" {
		t.Fatalf("BaseURL = %q", zm.BaseURL)
	}
	if !zm.IsEnabled() {
		t.Fatal("zenmux must be enabled by default")
	}
	if len(zm.Models) != 1 || zm.Models[0].ID != "free-model" {
		t.Fatalf("Models = %+v", zm.Models)
	}
	if zm.Models[0].Context != 128000 {
		t.Fatalf("model context = %d", zm.Models[0].Context)
	}
	if zm.Settings["chat_path"] != "/chat/completions" {
		t.Fatalf("Settings = %+v", zm.Settings)
	}

	if c.Providers["выключенный"].IsEnabled() {
		t.Fatal("the disabled provider must report disabled")
	}
}

// TestStateOverrideFromEnv — одноразовый прогон не должен трогать рабочее
// состояние.
func TestStateOverrideFromEnv(t *testing.T) {
	paths := writeConfig(t, `{}`)
	override := filepath.Join(t.TempDir(), "одноразовый.json")
	t.Setenv(EnvState, override)

	c, err := LoadFrom(paths)
	if err != nil {
		t.Fatal(err)
	}
	if c.Paths.StateFile != override {
		t.Fatalf("StateFile = %q, want %q", c.Paths.StateFile, override)
	}
}

// TestDefaultPathsAreXDG — пути следуют XDG, а не платформенным каталогам.
// Иначе состояние оказалось бы не там, где его ищет остальная система.
func TestDefaultPathsAreXDG(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "st"))

	paths, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "cfg", AppName, "config.json"); paths.ConfigFile != want {
		t.Fatalf("ConfigFile = %q, want %q", paths.ConfigFile, want)
	}
	if want := filepath.Join(dir, "st", AppName, "state.json"); paths.StateFile != want {
		t.Fatalf("StateFile = %q, want %q", paths.StateFile, want)
	}
}

// TestEmptyFileIsValid — пустой файл эквивалентен отсутствующему.
func TestEmptyFileIsValid(t *testing.T) {
	paths := writeConfig(t, "   \n")
	c, err := LoadFrom(paths)
	if err != nil {
		t.Fatalf("an empty file must be valid: %v", err)
	}
	if c.Mode.Val != DefaultMode {
		t.Fatalf("Mode = %q, want the default", c.Mode.Val)
	}
}
