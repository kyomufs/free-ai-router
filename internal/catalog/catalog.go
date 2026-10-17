// Package catalog собирает провайдеров из конфигурации в рабочий реестр.
//
// ЗАЧЕМ ОТДЕЛЬНЫЙ ПАКЕТ. provider описывает, ЧТО такое провайдер, и не знает
// о конфигурации. config описывает настройки, но не знает о провайдерах.
// Связывает их этот пакет — и только он. Благодаря этому ни один из двух не
// зависит от другого, а добавление нового типа провайдера не требует правки
// ни того, ни другого: достаточно зарегистрировать конструктор здесь.
//
// РЕГИСТРАЦИЯ ТИПОВ. Тип провайдера — это имя кода, который его обслуживает
// ("opencode", "openai"). Соответствие «тип → конструктор» задаётся таблицей
// ниже. Новый тип регистрируется одной строкой.
package catalog

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"free-ai-router/internal/config"
	"free-ai-router/internal/provider"
	"free-ai-router/internal/provider/openai"
	"free-ai-router/internal/provider/opencode"
)

// Builder создаёт провайдера из настроек конфигурации.
//
// На вход подаётся идентификатор провайдера (имя секции в конфигурации) и его
// настройки; на выходе готовый провайдер. Ошибка означает, что секция
// заполнена неверно, и это должно быть видно сразу, а не при первом запросе.
type Builder func(id string, cfg config.ProviderConfig, defaults Defaults) (provider.Provider, error)

// Defaults — значения, общие для всех провайдеров.
//
// Передаются отдельно, чтобы провайдеру не приходилось перечитывать
// конфигурацию целиком ради одной строки.
type Defaults struct {
	// Upstream — адрес по умолчанию для провайдеров, не указавших свой.
	Upstream string
	// HTTP — клиент для запросов каталога.
	//
	// Один на всех провайдеров: каталоги запрашиваются редко, и заводить
	// каждому свой пул соединений значило бы держать несколько наборов
	// простаивающих сокетов ради одного и того же хоста.
	HTTP *http.Client
}

// builders — соответствие «тип провайдера → конструктор».
//
// Таблица, а не switch: добавление типа не трогает логику сборки, и её нельзя
// случайно сломать, дописывая ветку.
var builders = map[string]Builder{
	// Полноценный провайдер со своим контрактом.
	"opencode": buildOpenCode,

	// Шаблон для любого OpenAI-совместимого сервиса. Назван нейтрально: под
	// ним работает и ZenMux, и Freebuff, и всё, что говорит на том же
	// протоколе.
	"openai": buildOpenAI,
}

// Build собирает реестр провайдеров из конфигурации.
//
// Провайдеры с enabled: false пропускаются — это способ выключить секцию, не
// удаляя её. Провайдер без моделей не является ошибкой: у некоторых сервисов
// каталог узнаётся позже, а сам шлюз работает и без него.
func Build(cfg *config.Config) (*provider.Registry, error) {
	defaults := Defaults{Upstream: cfg.Upstream.Val}

	ids := make([]string, 0, len(cfg.Providers))
	for id := range cfg.Providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	reg := provider.NewRegistry()
	for _, id := range ids {
		pc := cfg.Providers[id]
		if !pc.IsEnabled() {
			continue
		}

		if strings.TrimSpace(pc.Type) == "" {
			return nil, fmt.Errorf("provider %q: type is required", id)
		}
		build, ok := builders[pc.Type]
		if !ok {
			return nil, fmt.Errorf("provider %q: unknown type %q (known: %s)",
				id, pc.Type, strings.Join(KnownTypes(), ", "))
		}

		p, err := build(id, pc, defaults)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", id, err)
		}
		if p.ID() != provider.ID(id) {
			// Конструктор обязан вернуть провайдера под тем именем, которое
			// назвал оператор: это имя уходит в маршруты клиента, и
			// расхождение сломало бы выбор провайдера.
			return nil, fmt.Errorf("provider %q: constructor returned %q", id, p.ID())
		}
		reg.Register(p)
	}

	if reg.Len() == 0 {
		return nil, fmt.Errorf("no providers configured: add a \"providers\" section to the configuration")
	}
	return reg, nil
}

// KnownTypes возвращает список зарегистрированных типов.
//
// Нужен для сообщения об ошибке: оператор, написавший опечатку, должен сразу
// увидеть, какие варианты существуют.
func KnownTypes() []string {
	out := make([]string, 0, len(builders))
	for name := range builders {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// buildOpenCode собирает провайдера OpenCode Zen.
func buildOpenCode(id string, pc config.ProviderConfig, defaults Defaults) (provider.Provider, error) {
	baseURL := pc.BaseURL
	if baseURL == "" {
		baseURL = opencode.DefaultBaseURL
	}

	settings := copySettings(pc.Settings)
	return opencode.New(opencode.Config{
		BaseURL:      baseURL,
		APIKey:       pc.ProviderKey(),
		AnonymousKey: stringSetting(settings, "anonymous_key"),
		UserAgent:    stringSetting(settings, "user_agent"),
		ShellName:    stringSetting(settings, "shell_name"),
		OSName:       stringSetting(settings, "os_name"),
		ClientName:   stringSetting(settings, "client_name"),
		HTTP:         defaults.HTTP,
	})
}

// buildOpenAI собирает провайдера на OpenAI-совместимом протоколе.
//
// Здесь и находится точка расширения: чтобы подключить новый сервис
// (ZenMux, Freebuff и подобные), достаточно секции в конфигурации с типом
// "openai". Писать код не нужно.
func buildOpenAI(id string, pc config.ProviderConfig, defaults Defaults) (provider.Provider, error) {
	baseURL := pc.BaseURL
	if baseURL == "" {
		// Общий адрес по умолчанию годится только как подсказка: у каждого
		// сервиса свой. Но если оператор задал upstream глобально, взять его
		// разумнее, чем отказ.
		baseURL = defaults.Upstream
	}
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("base_url is required for an openai-compatible provider")
	}

	settings := copySettings(pc.Settings)
	return openai.New(openai.Config{
		ID:             provider.ID(id),
		BaseURL:        baseURL,
		APIKey:         pc.ProviderKey(),
		AnonymousKey:   stringSetting(settings, "anonymous_key"),
		AuthHeader:     stringSetting(settings, "auth_header"),
		AuthPrefix:     stringSetting(settings, "auth_prefix"),
		UserAgent:      stringSetting(settings, "user_agent"),
		ReasoningField: stringSetting(settings, "reasoning_field"),
		ChatPath:       stringSetting(settings, "chat_path"),
		ExtraHeaders:   stringMapSetting(settings, "headers"),
		HTTP:           defaults.HTTP,
	})
}

// copySettings копирует настройки, чтобы конструктор не мог изменить
// конфигурацию через полученную карту.
func copySettings(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// stringSetting читает строковую настройку.
//
// Неверный тип игнорируется, а не считается ошибкой: настройка с чужим типом
// почти всегда означает опечатку в имени, и падать из-за неё хуже, чем
// применить умолчание.
func stringSetting(settings map[string]any, key string) string {
	v, ok := settings[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// stringMapSetting читает настройку-словарь строк.
func stringMapSetting(settings map[string]any, key string) map[string]string {
	raw, ok := settings[key]
	if !ok {
		return nil
	}
	source, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(source))
	for k, v := range source {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
