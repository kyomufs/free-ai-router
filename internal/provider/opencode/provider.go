// Package opencode реализует провайдера OpenCode Zen (opencode.ai).
//
// ЧТО СПЕЦИФИЧНО ДЛЯ НЕГО и поэтому живёт здесь:
//
//   - набор обязательных инструментов: запрос обязан нести определения `shell`
//     и `read` целиком — имя, описание и схему (проверка сверяет записи, а не
//     имена);
//   - вид системного промпта: сервис отвергает конкретный текст генератора
//     заголовков, поэтому промпт рендерится, а не пробрасывается как есть;
//   - форма заголовков: имена, разделение сессии и affinity, формат
//     User-Agent, запрет на префиксы `prj_` и `req_` в отдельных полях;
//   - поле учётных данных: пустая строка означает анонимный доступ, и это
//     нормальный режим, а не отсутствие настройки.
//
// ЧЕГО ЗДЕСЬ НЕТ и не должно быть: политики повторов, ротации ключей,
// транспорта. Это общая механика — см. internal/capability и
// internal/transport.
//
// Источник контракта — исходники opencode 2.0.30:
//
//	headers         packages/core/src/session/model-request.ts
//	affinity        packages/core/src/session/affinity.ts
//	user agent      packages/core/src/app.ts
//	tools           packages/core/src/tool/plugin/shell.ts, read.ts
//	system prompt   packages/core/src/session/runner/prompt/system.txt
//	                packages/core/src/session/system-prompt.ts
//	модели          packages/core/src/models-dev/snapshot.txt
package opencode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"free-ai-router/internal/provider"
)

// ID — идентификатор провайдера. Он виден в конфигурации, в CLI и как имя
// маршрута в клиенте, поэтому менять его нельзя без переименования маршрута.
const ID provider.ID = "opencode"

// DefaultBaseURL — адрес inference-эндпоинта провайдера.
//
// Взят из каталога моделей (snapshot.txt, ключ `api`), а не из кода клиента:
// каталог — это то, на что клиент ориентируется при выборе модели.
const DefaultBaseURL = "https://opencode.ai/inference/v1"

// Config — настройки провайдера, разобранные из конфигурации шлюза.
type Config struct {
	// BaseURL переопределяет адрес провайдера.
	BaseURL string
	// APIKey — учётные данные. Пустая строка означает анонимный доступ:
	// провайдер отправляет маркер анонимности, а не Bearer с ключом.
	APIKey string
	// AnonymousKey — маркер анонимности. По умолчанию "public".
	AnonymousKey string
	// UserAgent переопределяет строку User-Agent.
	UserAgent string
	// ShellName и OSName попадают в описание инструмента shell. Они часть
	// проверки, поэтому вынесены в настройки: на другой платформе описание
	// обязано совпадать с тем, что отправил бы клиент на ней.
	ShellName string
	OSName    string
	// ClientName — значение заголовка x-opencode-client. По умолчанию "cli".
	ClientName string
	// Models — переопределение каталога. Пусто означает встроенный список.
	Models []provider.Model
}

// Provider — реализация provider.Provider для OpenCode Zen.
type Provider struct {
	cfg Config

	baseURL      string
	anonymousKey string
	userAgent    string
	shellName    string
	osName       string
	clientName   string
	models       []provider.Model
}

// New создаёт провайдера из конфигурации.
func New(cfg Config) (*Provider, error) {
	p := &Provider{cfg: cfg}

	p.baseURL = strings.TrimRight(cfg.BaseURL, "/")
	if p.baseURL == "" {
		p.baseURL = DefaultBaseURL
	}
	p.anonymousKey = cfg.AnonymousKey
	if p.anonymousKey == "" {
		p.anonymousKey = "public"
	}
	p.userAgent = cfg.UserAgent
	if p.userAgent == "" {
		p.userAgent = DefaultUserAgent
	}
	p.shellName = cfg.ShellName
	if p.shellName == "" {
		p.shellName = "sh"
	}
	p.osName = cfg.OSName
	if p.osName == "" {
		p.osName = "Linux"
	}
	p.clientName = cfg.ClientName
	if p.clientName == "" {
		p.clientName = "cli"
	}
	p.models = cfg.Models
	if len(p.models) == 0 {
		p.models = builtinModels()
	}
	return p, nil
}

// ID реализует provider.Provider.
func (p *Provider) ID() provider.ID { return ID }

// Models реализует provider.Provider.
func (p *Provider) Models(_ context.Context) ([]provider.Model, error) {
	out := make([]provider.Model, len(p.models))
	copy(out, p.models)
	return out, nil
}

// Credential возвращает значение для Authorization.
//
// Отдельный метод, а не поле структуры: значение зависит от того, какой ключ
// выдала общая механика ротации, и берётся на каждый запрос.
func (p *Provider) Credential(key string) string {
	if key == "" {
		return p.anonymousKey
	}
	return key
}

// Build реализует provider.Provider.
//
// Тело запроса собирается в форме OpenAI Chat Completions: это то, что
// понимает провайдер, и то, что уже приходит от клиента. Шлюз передаёт
// сообщения в нейтральной форме, поэтому здесь происходит единственное
// преобразование.
func (p *Provider) Build(req provider.Request) (provider.Prepared, error) {
	body, err := p.buildBody(req)
	if err != nil {
		return provider.Prepared{}, err
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return provider.Prepared{}, fmt.Errorf("opencode: encoding body: %w", err)
	}

	headers := p.buildHeaders(req.Session)

	return provider.Prepared{
		URL:     p.baseURL + "/chat/completions",
		Headers: headers,
		Body:    raw,
		// Полоса reasoning держит более длинные паузы, чем обычный поток,
		// потому что модель может молчать несколько минут, продолжая
		// работать. Разделение бюджетов — часть контракта, а не настройка.
		IdleTimeout:       idleTimeoutFor(req.Model),
		FirstEventTimeout: firstEventTimeout,
	}, nil
}

// buildBody собирает тело запроса.
func (p *Provider) buildBody(req provider.Request) (map[string]any, error) {
	messages, err := p.buildMessages(req)
	if err != nil {
		return nil, err
	}

	body := map[string]any{
		"model":          req.Model.ID,
		"messages":       messages,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}

	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.ReasoningEffort != "" {
		body["reasoning_effort"] = req.ReasoningEffort
	}

	tools := p.buildTools(req.Tools)
	if len(tools) > 0 {
		body["tools"] = tools
		// tool_choice отправляется только когда инструменты есть: клиент
		// делает так же. Когда инструментов нет, поле отсутствует, а не
		// содержит "none" — это разные формы, и провайдер их различает.
		if req.ToolChoice != "" {
			body["tool_choice"] = req.ToolChoice
		}
	}

	return body, nil
}

// buildMessages переводит нейтральные сообщения в форму провайдера.
func (p *Provider) buildMessages(req provider.Request) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(req.Messages)+1)

	// Системная часть — одно сообщение, отрендеренное провайдером.
	if req.System != "" {
		out = append(out, map[string]any{"role": "system", "content": req.System})
	}

	for _, m := range req.Messages {
		switch m.Role {
		case "assistant":
			msg := map[string]any{"role": "assistant", "content": m.Text}
			if m.Reasoning != "" {
				// Имя поля берётся из метаданных модели: у разных моделей
				// размышления ждут в разных местах.
				field := req.Model.ReasoningField
				if field == "" {
					field = "reasoning_content"
				}
				msg[field] = m.Reasoning
			}
			if len(m.ToolCalls) > 0 {
				calls := make([]map[string]any, 0, len(m.ToolCalls))
				for _, tc := range m.ToolCalls {
					calls = append(calls, map[string]any{
						"id":   tc.ID,
						"type": "function",
						"function": map[string]any{
							"name":      tc.Name,
							"arguments": tc.Arguments,
						},
					})
				}
				msg["tool_calls"] = calls
			}
			// Пустой ход ассистента на проводе недопустим.
			if m.Text == "" && m.Reasoning == "" && len(m.ToolCalls) == 0 {
				continue
			}
			out = append(out, msg)

		case "tool":
			out = append(out, map[string]any{
				"role":         "tool",
				"tool_call_id": m.ToolCallID,
				"content":      nonEmpty(m.Text),
			})

		default: // user, system из клиента
			content, err := p.buildUserContent(m)
			if err != nil {
				return nil, err
			}
			if content == nil {
				continue
			}
			out = append(out, map[string]any{"role": m.Role, "content": content})
		}
	}
	return out, nil
}

// buildUserContent собирает содержимое пользовательского сообщения.
//
// Текст без вложений остаётся строкой: так делает клиент, и так короче тело.
// Вложения переводят содержимое в список частей.
func (p *Provider) buildUserContent(m provider.Message) (any, error) {
	if len(m.Images) == 0 {
		if m.Text == "" {
			return nil, nil
		}
		return m.Text, nil
	}

	parts := make([]map[string]any, 0, len(m.Images)+1)
	if m.Text != "" {
		parts = append(parts, map[string]any{"type": "text", "text": m.Text})
	}
	for _, img := range m.Images {
		mediaType := img.MediaType
		if mediaType == "" {
			mediaType = "image/png"
		}
		parts = append(parts, map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(img.Data),
			},
		})
	}
	return parts, nil
}

// buildTools собирает определения инструментов и ДОПОЛНЯЕТ их обязательными.
//
// Здесь и находится главная специфика провайдера. Проверка сверяет записи
// целиком: имя, описание и схему. Поэтому:
//
//   - обязательные инструменты добавляются, если клиент их не прислал;
//   - если клиент прислал инструмент с тем же именем, но с чужим описанием или
//     схемой, запись ЗАМЕНЯЕТСЯ канонической. Иначе запрос ушёл бы с правильным
//     именем и неправильным содержимым — а проверка смотрит на содержимое.
//
// Порядок не важен: проверка его не смотрит. Поэтому инструменты клиента идут
// первыми, обязательные дописываются в конец.
func (p *Provider) buildTools(tools []provider.Tool) []map[string]any {
	gate := p.gateTools()
	gateByName := make(map[string]map[string]any, len(gate))
	for _, g := range gate {
		gateByName[g.name] = g.entry
	}

	out := make([]map[string]any, 0, len(tools)+len(gate))
	seen := make(map[string]bool, len(gate))

	for _, t := range tools {
		if canonical, isGate := gateByName[t.Name]; isGate {
			out = append(out, canonical)
			seen[t.Name] = true
			continue
		}
		out = append(out, toolEntry(t))
	}

	for _, g := range gate {
		if !seen[g.name] {
			out = append(out, g.entry)
		}
	}
	return out
}

// toolEntry переводит объявление инструмента в форму провода.
func toolEntry(t provider.Tool) map[string]any {
	var schema any
	if len(t.Schema) > 0 {
		if err := json.Unmarshal(t.Schema, &schema); err != nil {
			// Схема клиента не разобралась: отправляем пустой объект, а не
			// роняем запрос. Проверка смотрит только на обязательные
			// инструменты, у остальных форма не критична.
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
	} else {
		schema = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"parameters":  schema,
		},
	}
}

// gateTool — обязательный инструмент с уже собранной записью.
type gateTool struct {
	name  string
	entry map[string]any
}

// gateNameShell и gateNameRead — имена обязательных инструментов.
//
// В v1 клиента инструмент назывался `bash`; в v2 он переименован в `shell`
// (packages/core/src/database/v1-migration.bun.ts, RENAMED_TOOLS). Целевая
// версия — 2.0.30, поэтому используется `shell`.
const (
	gateNameShell = "shell"
	gateNameRead  = "read"
)

// gateTools возвращает записи обязательных инструментов.
//
// Описание shell параметризовано именем оболочки и названием системы: клиент
// подставляет их из окружения, и проверка сверяет получившийся текст.
func (p *Provider) gateTools() []gateTool {
	shellDescription := fmt.Sprintf(shellDescriptionTemplate, p.osName, p.shellName)

	return []gateTool{
		{
			name: gateNameShell,
			entry: map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        gateNameShell,
					"description": shellDescription,
					"parameters": map[string]any{
						"$schema": jsonSchemaDialect,
						"type":    "object",
						"properties": map[string]any{
							"command": map[string]any{
								"type":        "string",
								"description": "Shell command string to execute",
							},
							"workdir": map[string]any{
								"type":        "string",
								"description": workdirDescription,
							},
							"timeout": map[string]any{
								"type":        "integer",
								"minimum":     0,
								"description": timeoutDescription,
							},
							"background": map[string]any{
								"type":        "boolean",
								"description": backgroundDescription,
							},
						},
						"required": []any{"command"},
					},
				},
			},
		},
		{
			name: gateNameRead,
			entry: map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        gateNameRead,
					"description": readDescription,
					"parameters": map[string]any{
						"$schema": jsonSchemaDialect,
						"type":    "object",
						"properties": map[string]any{
							"path": map[string]any{
								"type":        "string",
								"description": "File or directory to read",
							},
							"offset": map[string]any{
								"type":        "integer",
								"minimum":     0,
								"description": "The line or directory entry to start reading from (1-based)",
							},
							"limit": map[string]any{
								"type":        "integer",
								"minimum":     0,
								"description": "The maximum number of lines or directory entries to read (defaults to and capped at 2000)",
							},
						},
						"required": []any{"path"},
					},
				},
			},
		},
	}
}

// buildHeaders собирает заголовки запроса.
//
// Разделение сессии и affinity — не деталь: клиент кладёт в
// `x-opencode-session-id` идентификатор сессии, а в `x-opencode-session` —
// affinity, то есть корень группы для маршрутизации кеша. Для плоской сессии
// значения совпадают, для вложенной расходятся.
//
// Заголовки родителя добавляются ТОЛЬКО когда родитель есть: у плоской сессии
// их не бывает, и всегда слать их нельзя.
func (p *Provider) buildHeaders(ids provider.SessionIDs) http.Header {
	h := make(http.Header)

	h.Set("content-type", "application/json")
	h.Set("user-agent", p.userAgent)
	h.Set("x-opencode-client", p.clientName)

	if ids.Session != "" {
		h.Set("x-opencode-session-id", ids.Session)
	}
	if ids.Session != "" || ids.Affinity != "" {
		h.Set("x-opencode-session", nonEmpty(ids.Affinity, ids.Session))
		h.Set("x-session-affinity", nonEmpty(ids.Affinity, ids.Session))
		h.Set("X-Session-Id", nonEmpty(ids.Affinity, ids.Session))
	}
	if ids.Message != "" {
		h.Set("x-opencode-request", ids.Message)
	}
	if ids.Project != "" {
		h.Set("x-opencode-project", ids.Project)
	}

	// Заголовки родителя — только для вложенных сессий.
	if ids.Parent != "" {
		h.Set("x-opencode-parent-session-id", ids.Parent)
		h.Set("x-parent-session-id", ids.Parent)
	}

	return h
}

// nonEmpty возвращает первый непустой аргумент.
func nonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
