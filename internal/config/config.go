// Package config собирает конфигурацию демона из трёх уровней.
//
// ПОРЯДОК СТАРШИНСТВА — часть контракта, а не деталь реализации:
//
//	флаги CLI                  побеждают всё
//	переменные окружения       перекрывают файл
//	config.json                базовый слой
//
// Каждое значение помнит, откуда пришло. Это не украшение: без источника
// отладка слияния превращается в угадывание, и `--print-config` печатает
// именно его.
//
// РАСКЛАДКА. Пути определяются XDG-переменными, а не самой конфигурацией:
// иначе получилась бы циклическая зависимость — файл, сообщающий, где лежит
// файл. Три корня разделены по природе данных:
//
//	конфигурация   ты правишь; никогда не создаётся машиной, никогда не чистится
//	состояние      переживает рестарты; накапливается
//	временное       живёт, пока живёт процесс
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AppName — имя каталогов приложения в XDG-корнях.
const AppName = "free-ai-router"

// Paths — все места на диске, которые использует демон.
type Paths struct {
	// ConfigDir — каталог конфигурации.
	ConfigDir string
	// StateDir — каталог состояния.
	StateDir string
	// RuntimeDir — каталог временных файлов.
	RuntimeDir string
	// ConfigFile — файл конфигурации.
	ConfigFile string
	// StateFile — файл состояния.
	StateFile string
	// LogFile — журнал.
	LogFile string
}

// DefaultPaths разрешает XDG-каталоги.
//
// Переменные читаются через os.Getenv, а не через os.UserConfigDir: тот
// подставляет каталог, специфичный для платформы (на macOS — Library), тогда
// как остальная система ожидает XDG-путь.
func DefaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("config: cannot resolve the home directory: %w", err)
	}

	configHome := envOr("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	stateHome := envOr("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	runtimeHome := envOr("XDG_RUNTIME_DIR", filepath.Join(stateHome, AppName, "run"))

	configDir := filepath.Join(configHome, AppName)
	stateDir := filepath.Join(stateHome, AppName)

	return Paths{
		ConfigDir:  configDir,
		StateDir:   stateDir,
		RuntimeDir: filepath.Join(runtimeHome, AppName),
		ConfigFile: filepath.Join(configDir, "config.json"),
		StateFile:  filepath.Join(stateDir, "state.json"),
		LogFile:    filepath.Join(stateDir, "app.log"),
	}, nil
}

// envOr возвращает значение переменной или запасное.
func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

// Source указывает, откуда пришло значение настройки.
//
// Источник нужен для `--print-config`: без него оператор не может отличить
// значение из флага от значения из файла.
type Source string

const (
	// SourceDefault — встроенное значение.
	SourceDefault Source = "default"
	// SourceFile — из config.json.
	SourceFile Source = "config.json"
	// SourceEnv — из переменной окружения.
	SourceEnv Source = "env"
	// SourceFlag — из флага командной строки.
	SourceFlag Source = "flag"
)

// Value — одно значение настройки вместе с его источником.
type Value[T any] struct {
	Val  T
	From Source
}

// Set записывает значение и его источник.
func (v *Value[T]) Set(val T, from Source) {
	v.Val = val
	v.From = from
}

// OrDefault проставляет источник «по умолчанию», если он ещё не задан.
func (v *Value[T]) OrDefault() {
	if v.From == "" {
		v.From = SourceDefault
	}
}

// Config — полностью разрешённая конфигурация.
type Config struct {
	// --- режим работы ------------------------------------------------------

	// Mode — форма развёртывания.
	Mode Value[string]
	// Listen — адрес прослушивания.
	Listen Value[string]
	// Token — общий секрет для режима server. Отдельно от учётных данных
	// провайдера: клиент аутентифицируется ИМ, а шлюз — ключом провайдера.
	Token Value[string]

	// --- провайдеры и механизмы --------------------------------------------

	// Providers — описания провайдеров по их идентификаторам.
	Providers map[string]ProviderConfig

	// --- транспорт ---------------------------------------------------------

	// Transport — идентификатор транспорта.
	Transport Value[string]
	// TransportConfig — настройки транспорта.
	TransportConfig map[string]any

	// --- тайминги ----------------------------------------------------------

	// FirstEventTimeout — бюджет до первых заголовков ответа.
	FirstEventTimeout Value[time.Duration]
	// IdleTimeout — бюджет молчания обычного потока.
	IdleTimeout Value[time.Duration]
	// ReasoningIdleTimeout — бюджет молчания для рассуждающих моделей.
	ReasoningIdleTimeout Value[time.Duration]

	// --- прочее ------------------------------------------------------------

	// Upstream — базовый адрес по умолчанию для провайдеров, не указавших
	// свой. Нужен редко, но позволяет не повторять адрес в каждой секции.
	Upstream Value[string]

	// Paths — места на диске. Не настройка: определяется XDG.
	Paths Paths
}

// ProviderConfig — настройки одного провайдера.
//
// Настройки хранятся как map, а не как структура: у разных типов провайдеров
// разные поля, и общая структура описывала бы только пересечение. Разбор
// значений — дело самого провайдера, он же и сообщает об ошибке.
type ProviderConfig struct {
	// Type — какой код обслуживает эту запись ("opencode", "openai").
	Type string `json:"type"`
	// Enabled позволяет выключить запись, не удаляя её.
	Enabled *bool `json:"enabled,omitempty"`

	// Поля ниже — общие для всех провайдеров, потому что нужны почти всем.
	// Всё, чего здесь нет, задаётся в Settings.

	// BaseURL — адрес сервиса.
	BaseURL string `json:"base_url,omitempty"`
	// APIKey — учётные данные прямо в файле.
	APIKey string `json:"api_key,omitempty"`
	// APIKeyEnv — имя переменной с ключом. Предпочтительнее ключа в файле:
	// конфигурация часто попадает в резервные копии.
	APIKeyEnv string `json:"api_key_env,omitempty"`
	// Models — каталог моделей, если провайдер его не знает сам.
	Models []ModelConfig `json:"models,omitempty"`

	// Settings — всё остальное, что понимает конкретный тип провайдера.
	Settings map[string]any `json:"settings,omitempty"`
}

// IsEnabled сообщает, включена ли запись.
//
// Отсутствие поля означает «включена»: оператор, добавивший секцию, ждёт, что
// она работает.
func (p ProviderConfig) IsEnabled() bool {
	if p.Enabled == nil {
		return true
	}
	return *p.Enabled
}

// ModelConfig — описание модели в конфигурации.
type ModelConfig struct {
	ID              string   `json:"id"`
	Name            string   `json:"name,omitempty"`
	Description     string   `json:"description,omitempty"`
	Context         int      `json:"context,omitempty"`
	MaxOutput       int      `json:"max_output,omitempty"`
	Vision          bool     `json:"vision,omitempty"`
	Reasoning       bool     `json:"reasoning,omitempty"`
	ReasoningField  string   `json:"reasoning_field,omitempty"`
	ReasoningKind   string   `json:"reasoning_kind,omitempty"`
	ReasoningValues []string `json:"reasoning_values,omitempty"`
}

// Режимы работы.
const (
	// ModeLocal — шлюз и клиент на одной машине. Токен не проверяется вовсе:
	// до loopback-слушателя кроме этой машины никто не дотянется.
	ModeLocal = "local"
	// ModeServer — шлюз на сервере, клиент на другой машине. Токен обязателен,
	// иначе открытый релей расходует квоту оператора для любого, кто найдёт
	// порт.
	ModeServer = "server"
)

// Режимы по умолчанию.
const (
	DefaultMode      = ModeLocal
	DefaultListen    = "127.0.0.1:8787"
	DefaultTransport = "direct"
	DefaultUpstream  = "https://opencode.ai"

	DefaultFirstEventTimeout    = 30 * time.Second
	DefaultIdleTimeout          = 120 * time.Second
	DefaultReasoningIdleTimeout = 300 * time.Second
)

// Default возвращает конфигурацию со встроенными значениями.
func Default() *Config {
	c := &Config{
		Providers:       make(map[string]ProviderConfig),
		TransportConfig: make(map[string]any),
	}
	c.Mode.Set(DefaultMode, SourceDefault)
	c.Listen.Set(DefaultListen, SourceDefault)
	c.Token.Set("", SourceDefault)
	c.Transport.Set(DefaultTransport, SourceDefault)
	c.Upstream.Set(DefaultUpstream, SourceDefault)
	c.FirstEventTimeout.Set(DefaultFirstEventTimeout, SourceDefault)
	c.IdleTimeout.Set(DefaultIdleTimeout, SourceDefault)
	c.ReasoningIdleTimeout.Set(DefaultReasoningIdleTimeout, SourceDefault)
	return c
}

// IsServer сообщает, действует ли проверка токена.
func (c *Config) IsServer() bool { return c.Mode.Val == ModeServer }

// Load читает конфигурацию: файл, затем окружение.
//
// Отсутствующий файл не ошибка — встроенные значения работают сами. Ошибка
// разбора это ошибка: молчаливый откат к умолчаниям скрыл бы опечатку.
func Load() (*Config, error) {
	paths, err := DefaultPaths()
	if err != nil {
		return nil, err
	}
	return LoadFrom(paths)
}

// LoadFrom читает конфигурацию по заданным путям.
func LoadFrom(paths Paths) (*Config, error) {
	c := Default()
	c.Paths = paths

	if err := c.applyFile(paths.ConfigFile); err != nil {
		return nil, err
	}
	if err := c.applyEnv(); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// fileShape — форма файла конфигурации.
//
// Отдельный тип, а не сам Config: в файле значение это просто значение, а
// источник нужно знать только после слияния.
type fileShape struct {
	Mode                 *string                   `json:"mode"`
	Listen               *string                   `json:"listen"`
	Token                *string                   `json:"token"`
	Transport            *string                   `json:"transport"`
	TransportSettings    map[string]any            `json:"transport_settings"`
	Upstream             *string                   `json:"upstream"`
	FirstEventTimeout    *string                   `json:"first_event_timeout"`
	IdleTimeout          *string                   `json:"idle_timeout"`
	ReasoningIdleTimeout *string                   `json:"reasoning_idle_timeout"`
	Providers            map[string]ProviderConfig `json:"providers"`
}

// applyFile накладывает файл на встроенные значения.
func (c *Config) applyFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // нет файла — работают умолчания
		}
		return fmt.Errorf("config: reading %s: %w", path, err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}

	var shape fileShape
	if err := json.Unmarshal(raw, &shape); err != nil {
		return fmt.Errorf("config: parsing %s: %w", path, err)
	}

	if shape.Mode != nil {
		c.Mode.Set(*shape.Mode, SourceFile)
	}
	if shape.Listen != nil {
		c.Listen.Set(*shape.Listen, SourceFile)
	}
	if shape.Token != nil {
		c.Token.Set(*shape.Token, SourceFile)
	}
	if shape.Transport != nil {
		c.Transport.Set(*shape.Transport, SourceFile)
	}
	if shape.Upstream != nil {
		c.Upstream.Set(*shape.Upstream, SourceFile)
	}
	if shape.TransportSettings != nil {
		c.TransportConfig = shape.TransportSettings
	}
	if shape.Providers != nil {
		c.Providers = shape.Providers
	}

	for _, d := range []struct {
		name string
		src  *string
		dst  *Value[time.Duration]
	}{
		{"first_event_timeout", shape.FirstEventTimeout, &c.FirstEventTimeout},
		{"idle_timeout", shape.IdleTimeout, &c.IdleTimeout},
		{"reasoning_idle_timeout", shape.ReasoningIdleTimeout, &c.ReasoningIdleTimeout},
	} {
		if d.src == nil {
			continue
		}
		v, err := time.ParseDuration(*d.src)
		if err != nil {
			return fmt.Errorf("config: invalid %s %q: %w", d.name, *d.src, err)
		}
		d.dst.Set(v, SourceFile)
	}

	return nil
}

// Переменные окружения. Плоские оси имеют каждая свою переменную; вложенные
// настройки провайдеров задаются только файлом, потому что их форма зависит
// от типа провайдера.
const (
	EnvMode      = "FREE_AI_ROUTER_MODE"
	EnvListen    = "FREE_AI_ROUTER_LISTEN"
	EnvToken     = "FREE_AI_ROUTER_TOKEN"
	EnvTransport = "FREE_AI_ROUTER_TRANSPORT"
	EnvUpstream  = "FREE_AI_ROUTER_UPSTREAM"
	// EnvState переопределяет файл состояния — чтобы одноразовый прогон не
	// тронул рабочее состояние.
	EnvState = "FREE_AI_ROUTER_STATE"
)

// applyEnv накладывает окружение поверх файла.
func (c *Config) applyEnv() error {
	for _, e := range []struct {
		name string
		dst  *Value[string]
	}{
		{EnvMode, &c.Mode},
		{EnvListen, &c.Listen},
		{EnvToken, &c.Token},
		{EnvTransport, &c.Transport},
		{EnvUpstream, &c.Upstream},
	} {
		if v := os.Getenv(e.name); v != "" {
			e.dst.Set(v, SourceEnv)
		}
	}

	// Переопределение состояния влияет только на путь, а не на значение оси.
	if v := strings.TrimSpace(os.Getenv(EnvState)); v != "" {
		c.Paths.StateFile = v
		c.Paths.StateDir = filepath.Dir(v)
	}
	return nil
}

// ApplyFlags накладывает значения из командной строки.
//
// Принимаются только непустые значения: пустая строка означает «флаг не
// задан», а не «установить пусто».
func (c *Config) ApplyFlags(mode, listen, token, transport, upstream string) {
	if mode != "" {
		c.Mode.Set(mode, SourceFlag)
	}
	if listen != "" {
		c.Listen.Set(listen, SourceFlag)
	}
	if token != "" {
		c.Token.Set(token, SourceFlag)
	}
	if transport != "" {
		c.Transport.Set(transport, SourceFlag)
	}
	if upstream != "" {
		c.Upstream.Set(upstream, SourceFlag)
	}
}

// Validate проверяет инварианты, которые держатся МЕЖДУ осями.
//
// Единственный такой инвариант сегодня: режим server требует токена. Шлюз в
// этом режиме слушает доступный адрес, поэтому без токена он становится
// открытым релеем, расходующим квоту оператора для любого, кто найдёт порт.
// Отказ стартовать — правильная реакция; предупреждение, проскочившее в
// журнале, защитой не является.
func (c *Config) Validate() error {
	switch c.Mode.Val {
	case ModeLocal, ModeServer:
	default:
		return fmt.Errorf("config: invalid mode %q (want %s|%s)", c.Mode.Val, ModeLocal, ModeServer)
	}
	if c.Mode.Val == ModeServer && strings.TrimSpace(c.Token.Val) == "" {
		return fmt.Errorf("config: mode %q requires a token: an untokened listener on a reachable address is an open relay", ModeServer)
	}
	if strings.TrimSpace(c.Listen.Val) == "" {
		return fmt.Errorf("config: listen address must not be empty")
	}
	if strings.TrimSpace(c.Transport.Val) == "" {
		return fmt.Errorf("config: transport must not be empty")
	}
	if c.FirstEventTimeout.Val <= 0 {
		return fmt.Errorf("config: first_event_timeout must be positive")
	}
	if c.IdleTimeout.Val <= 0 {
		return fmt.Errorf("config: idle_timeout must be positive")
	}
	if c.ReasoningIdleTimeout.Val <= 0 {
		return fmt.Errorf("config: reasoning_idle_timeout must be positive")
	}
	return nil
}

// ProviderKey возвращает учётные данные провайдера.
//
// Порядок: значение в файле, затем переменная, имя которой указано рядом.
// Обратный порядок был бы опасен — ключ в файле оказался бы сильнее явно
// заданной переменной.
func (p ProviderConfig) ProviderKey() string {
	if p.APIKey != "" {
		return p.APIKey
	}
	if p.APIKeyEnv != "" {
		return os.Getenv(p.APIKeyEnv)
	}
	return ""
}
