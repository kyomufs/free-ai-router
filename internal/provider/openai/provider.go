// Package openai — ШАБЛОН провайдера для любого сервиса, говорящего на
// OpenAI-совместимом протоколе.
//
// ЗАЧЕМ ОН НУЖЕН. Большинство сервисов бесплатных моделей (ZenMux, Freebuff и
// подобные) принимают тот же протокол, что и OpenAI: поле `model`, список
// `messages`, поток `stream`. Отличаются они адресом, учётными данными и
// мелочами вроде имени поля для размышлений. Поэтому вместо нового пакета на
// каждого достаточно настроить этот.
//
// ЧТО ДЕЛАТЬ, ЧТОБЫ ДОБАВИТЬ СВОЙ СЕРВИС. Порядок такой:
//
//  1. Если сервис говорит на OpenAI-совместимом протоколе — ничего писать не
//     нужно. Достаточно записи в конфигурации:
//
//     providers:
//     zenmux:
//     type: openai
//     base_url: https://zenmux.ai/api/v1
//     api_key_env: ZENMUX_API_KEY
//
//  2. Если сервис требует отправки обязательных инструментов, особого вида
//     системного промпта или своих заголовков сессии — сначала проверьте,
//     покрывают ли это поля ниже (gate_tools, system_prompt, headers). Часто
//     этого достаточно.
//
//  3. Если всё-таки нужен свой код — скопируйте пакет opencode целиком: в нём
//     есть всё, что бывает нужно, и он читается как образец. Свой пакет
//     реализует три метода интерфейса provider.Provider и больше ничего.
//
// ЧЕГО ЗДЕСЬ НЕТ И НЕ ДОЛЖНО БЫТЬ: политики повторов, ротации ключей,
// транспорта. Это общая механика: internal/capability и internal/transport.
package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"free-ai-router/internal/provider"
)

// ID по умолчанию. Переопределяется конфигурацией, потому что один и тот же
// код обслуживает несколько сервисов и должен представляться под их именами.
const DefaultID provider.ID = "openai"

// Config — настройки провайдера.
//
// Все поля необязательны кроме BaseURL: остальное имеет разумные значения по
// умолчанию, чтобы минимальная конфигурация состояла из одной строки.
type Config struct {
	// ID — идентификатор провайдера, под которым он виден в шлюзе.
	ID provider.ID
	// BaseURL — адрес сервиса без хвоста пути.
	BaseURL string
	// APIKey — учётные данные. Пустая строка означает анонимный доступ.
	APIKey string
	// AnonymousKey — маркер анонимности, если сервис его понимает. По
	// умолчанию "public".
	AnonymousKey string
	// AuthHeader — заголовок для ключа. По умолчанию "Authorization", а
	// значение — "Bearer <ключ>".
	AuthHeader string
	// AuthPrefix — префикс перед ключом. По умолчанию "Bearer ".
	AuthPrefix string
	// UserAgent — строка User-Agent. Пусто означает не отправлять её.
	UserAgent string
	// ReasoningField — имя поля, в которое ответ несёт размышления.
	// По умолчанию "reasoning_content".
	ReasoningField string
	// Models — каталог. Пусто означает, что каталог берётся из
	// конфигурации или запрашивается у сервиса.
	Models []provider.Model
	// ExtraHeaders — дополнительные заголовки, которые требует сервис.
	ExtraHeaders map[string]string
	// ChatPath — путь дописываемый к BaseURL. По умолчанию
	// "/chat/completions".
	ChatPath string
}

// Provider — реализация provider.Provider на OpenAI-совместимом протоколе.
type Provider struct {
	cfg Config

	id             provider.ID
	baseURL        string
	anonymousKey   string
	authHeader     string
	authPrefix     string
	reasoningField string
	chatPath       string
	extraHeaders   map[string]string
	models         []provider.Model
}

// New создаёт провайдера из конфигурации.
func New(cfg Config) (*Provider, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, fmt.Errorf("openai: base_url is required")
	}

	p := &Provider{
		cfg:            cfg,
		id:             cfg.ID,
		baseURL:        strings.TrimRight(cfg.BaseURL, "/"),
		anonymousKey:   cfg.AnonymousKey,
		authHeader:     cfg.AuthHeader,
		authPrefix:     cfg.AuthPrefix,
		reasoningField: cfg.ReasoningField,
		chatPath:       cfg.ChatPath,
		extraHeaders:   cfg.ExtraHeaders,
		models:         cfg.Models,
	}

	if p.id == "" {
		p.id = DefaultID
	}
	if p.anonymousKey == "" {
		p.anonymousKey = "public"
	}
	if p.authHeader == "" {
		p.authHeader = "authorization"
	}
	if p.authPrefix == "" {
		p.authPrefix = "Bearer "
	}
	if p.reasoningField == "" {
		p.reasoningField = "reasoning_content"
	}
	if p.chatPath == "" {
		p.chatPath = "/chat/completions"
	}
	if !strings.HasPrefix(p.chatPath, "/") {
		p.chatPath = "/" + p.chatPath
	}

	return p, nil
}

// ID реализует provider.Provider.
func (p *Provider) ID() provider.ID { return p.id }

// Models реализует provider.Provider.
func (p *Provider) Models(_ context.Context) ([]provider.Model, error) {
	out := make([]provider.Model, len(p.models))
	copy(out, p.models)
	return out, nil
}

// Credential возвращает значение для заголовка учётных данных.
func (p *Provider) Credential(key string) string {
	if key == "" {
		return p.anonymousKey
	}
	return key
}

// Build реализует provider.Provider.
func (p *Provider) Build(req provider.Request) (provider.Prepared, error) {
	body, err := p.buildBody(req)
	if err != nil {
		return provider.Prepared{}, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return provider.Prepared{}, fmt.Errorf("openai: encoding body: %w", err)
	}

	headers := make(http.Header)
	headers.Set("content-type", "application/json")
	if p.cfg.UserAgent != "" {
		headers.Set("user-agent", p.cfg.UserAgent)
	}
	for k, v := range p.extraHeaders {
		headers.Set(k, v)
	}

	return provider.Prepared{
		URL:               p.baseURL + p.chatPath,
		Headers:           headers,
		Body:              raw,
		IdleTimeout:       idleTimeoutFor(req.Model),
		FirstEventTimeout: firstEventTimeout,
	}, nil
}

// buildBody собирает тело запроса.
//
// Никакие обязательные инструменты и системные промпты здесь не
// подставляются: у обычных сервисов такой проверки нет. Если конкретному
// сервису они нужны, это признак того, что для него стоит завести свой пакет —
// как сделано для opencode.
func (p *Provider) buildBody(req provider.Request) (map[string]any, error) {
	messages, err := p.buildMessages(req)
	if err != nil {
		return nil, err
	}

	body := map[string]any{
		"model":    req.Model.ID,
		"messages": messages,
		"stream":   true,
	}
	if req.Stream {
		body["stream_options"] = map[string]any{"include_usage": true}
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
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, toolEntry(t))
		}
		body["tools"] = tools
		if req.ToolChoice != "" {
			body["tool_choice"] = req.ToolChoice
		}
	}
	return body, nil
}

// buildMessages переводит нейтральные сообщения в форму протокола.
func (p *Provider) buildMessages(req provider.Request) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(req.Messages)+1)
	if req.System != "" {
		out = append(out, map[string]any{"role": "system", "content": req.System})
	}

	for _, m := range req.Messages {
		switch m.Role {
		case "assistant":
			if m.Text == "" && m.Reasoning == "" && len(m.ToolCalls) == 0 {
				continue // пустой ход ассистента на проводе недопустим
			}
			msg := map[string]any{"role": "assistant", "content": m.Text}
			if m.Reasoning != "" {
				msg[p.reasoningField] = m.Reasoning
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
			out = append(out, msg)

		case "tool":
			content := m.Text
			if content == "" {
				content = "(no output)"
			}
			out = append(out, map[string]any{
				"role":         "tool",
				"tool_call_id": m.ToolCallID,
				"content":      content,
			})

		default:
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

// toolEntry переводит объявление инструмента в форму протокола.
func toolEntry(t provider.Tool) map[string]any {
	var schema any
	if len(t.Schema) > 0 {
		if err := json.Unmarshal(t.Schema, &schema); err != nil {
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

// Бюджеты потоков. Совпадают с остальными провайдерами: несовпадение
// привело бы к тому, что шлюз рвал бы соединение раньше клиента.
const (
	firstEventTimeout    = 30 * time.Second
	chatIdleTimeout      = 120 * time.Second
	reasoningIdleTimeout = 300 * time.Second
)

// idleTimeoutFor выбирает бюджет молчания по возможностям модели.
func idleTimeoutFor(m provider.Model) time.Duration {
	if m.Reasoning {
		return reasoningIdleTimeout
	}
	return chatIdleTimeout
}
