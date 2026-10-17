package gateway

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"free-ai-router/internal/provider"
	"free-ai-router/internal/wire"
)

// Разбор клиентского запроса в нейтральную форму.
//
// Всё преобразование делается ЗДЕСЬ, один раз. Провайдер получает готовую
// нейтральную структуру и переводит её в свой протокол — ему не приходится
// разбирать клиентский запрос, а значит и расходиться с шлюзом в его
// понимании.

// parseMessages переводит сообщения клиента в нейтральную форму.
func parseMessages(raw any) ([]provider.Message, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("поле messages обязательно и должно быть списком")
	}

	out := make([]provider.Message, 0, len(list))
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("messages[%d]: ожидался объект", i)
		}
		msg, err := parseMessage(m)
		if err != nil {
			return nil, fmt.Errorf("messages[%d]: %w", i, err)
		}
		out = append(out, msg)
	}
	return out, nil
}

// parseMessage разбирает одно сообщение.
func parseMessage(m map[string]any) (provider.Message, error) {
	role, _ := m["role"].(string)
	if role == "" {
		return provider.Message{}, fmt.Errorf("поле role обязательно")
	}

	msg := provider.Message{Role: role}

	// Текстовое содержимое бывает строкой или списком частей. Обе формы
	// законны, и обе надо понимать.
	switch content := m["content"].(type) {
	case string:
		msg.Text = content
	case []any:
		for _, part := range content {
			p, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if err := appendContentPart(&msg, p); err != nil {
				return provider.Message{}, err
			}
		}
	case nil:
		// Пустое содержимое допустимо у вызова инструмента.
	default:
		return provider.Message{}, fmt.Errorf("content: неожиданный тип %T", content)
	}

	// Имя поля для размышлений у разных моделей разное, поэтому проверяются
	// оба распространённых.
	if v, ok := m["reasoning_content"].(string); ok {
		msg.Reasoning = v
	} else if v, ok := m["reasoning"].(string); ok {
		msg.Reasoning = v
	}

	if id, ok := m["tool_call_id"].(string); ok {
		msg.ToolCallID = id
	}

	if calls, ok := m["tool_calls"].([]any); ok {
		for _, c := range calls {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			call := provider.ToolCall{}
			if id, ok := cm["id"].(string); ok {
				call.ID = id
			}
			if fn, ok := cm["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok {
					call.Name = name
				}
				if args, ok := fn["arguments"].(string); ok {
					call.Arguments = args
				}
			}
			msg.ToolCalls = append(msg.ToolCalls, call)
		}
	}

	return msg, nil
}

// appendContentPart добавляет одну часть содержимого.
func appendContentPart(msg *provider.Message, part map[string]any) error {
	kind, _ := part["type"].(string)
	switch kind {
	case "text", "input_text":
		if text, ok := part["text"].(string); ok {
			if msg.Text != "" {
				msg.Text += "\n"
			}
			msg.Text += text
		}
	case "image_url":
		img, err := parseImagePart(part)
		if err != nil {
			return err
		}
		msg.Images = append(msg.Images, img)
	default:
		// Неизвестную часть пропускаем, а не роняем запрос: протокол
		// развивается, и отказ из-за новой части был бы хуже её потери.
		_ = kind
	}
	return nil
}

// parseImagePart разбирает часть с изображением.
//
// Разбирается форма data-URL: она самодостаточна и не требует сети. Ссылка на
// внешний ресурс не поддерживается намеренно — шлюз не должен ходить по
// произвольным адресам от имени клиента.
func parseImagePart(part map[string]any) (provider.Image, error) {
	holder, ok := part["image_url"].(map[string]any)
	if !ok {
		if s, ok := part["image_url"].(string); ok {
			holder = map[string]any{"url": s}
		} else {
			return provider.Image{}, fmt.Errorf("image_url: ожидался объект или строка")
		}
	}
	url, _ := holder["url"].(string)
	if url == "" {
		return provider.Image{}, fmt.Errorf("image_url: поле url обязательно")
	}

	const prefix = "data:"
	if !strings.HasPrefix(url, prefix) {
		return provider.Image{}, fmt.Errorf("image_url: поддерживаются только data-URL")
	}
	rest := url[len(prefix):]
	sep := strings.Index(rest, ";base64,")
	if sep <= 0 {
		return provider.Image{}, fmt.Errorf("image_url: ожидалась форма data:<тип>;base64,<данные>")
	}
	mediaType := rest[:sep]
	data, err := base64.StdEncoding.DecodeString(rest[sep+len(";base64,"):])
	if err != nil {
		return provider.Image{}, fmt.Errorf("image_url: данные не декодируются: %w", err)
	}
	return provider.Image{MediaType: mediaType, Data: data}, nil
}

// parseTools переводит объявления инструментов в нейтральную форму.
func parseTools(raw any) ([]provider.Tool, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("поле tools должно быть списком")
	}

	out := make([]provider.Tool, 0, len(list))
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("tools[%d]: ожидался объект", i)
		}
		fn, ok := m["function"].(map[string]any)
		if !ok {
			continue // не функция — пропускаем
		}
		tool := provider.Tool{}
		if name, ok := fn["name"].(string); ok {
			tool.Name = name
		}
		if tool.Name == "" {
			return nil, fmt.Errorf("tools[%d]: имя обязательно", i)
		}
		if desc, ok := fn["description"].(string); ok {
			tool.Description = desc
		}
		if params, ok := fn["parameters"]; ok {
			encoded, err := json.Marshal(params)
			if err != nil {
				return nil, fmt.Errorf("tools[%d]: параметры не кодируются: %w", i, err)
			}
			tool.Schema = encoded
		}
		out = append(out, tool)
	}
	return out, nil
}

// systemFrom собирает системную часть из запроса клиента.
//
// Системных сообщений бывает несколько; шлюз объединяет их в одно, потому что
// провайдер получает системную часть отдельно от истории. Порядок сохраняется:
// склейка в обратном порядке изменила бы смысл указаний.
func systemFrom(body map[string]any) string {
	list, ok := body["messages"].([]any)
	if !ok {
		return ""
	}

	var parts []string
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := m["role"].(string); role != "system" {
			continue
		}
		if text, ok := m["content"].(string); ok && text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

// sessionIDs выводит идентификаторы сессии по контракту.
//
// Клиент может прислать свои идентификаторы в заголовках; если прислал,
// уважаются они. Если нет — выводятся по формуле, потому что провайдеры, у
// которых есть проверка, ждут идентификатор правильной формы.
func (h *Handler) sessionIDs(r *http.Request, body map[string]any) provider.SessionIDs {
	ids := provider.SessionIDs{
		Session:  firstNonEmpty(r.Header.Get("x-opencode-session-id"), r.Header.Get("x-session-id")),
		Affinity: firstNonEmpty(r.Header.Get("x-opencode-session"), r.Header.Get("x-session-affinity")),
		Project:  r.Header.Get("x-opencode-project"),
		Message:  r.Header.Get("x-opencode-request"),
		Parent:   firstNonEmpty(r.Header.Get("x-opencode-parent-session-id"), r.Header.Get("x-parent-session-id")),
	}

	if ids.Session == "" {
		ids.Session = wire.NewSessionID()
	}
	if ids.Affinity == "" {
		// Без родителя affinity совпадает с идентификатором сессии — так
		// делает и клиент.
		ids.Affinity = firstNonEmpty(ids.Parent, ids.Session)
	}
	if ids.Message == "" {
		ids.Message = wire.NewMessageID()
	}
	if ids.Project == "" {
		// Вне git-проекта клиент отправляет литерал "global".
		ids.Project = "global"
	}
	return ids
}

// firstNonEmpty возвращает первый непустой аргумент.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
