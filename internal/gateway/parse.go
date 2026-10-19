package gateway

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"free-ai-router/internal/provider"
)

// Parsing the client request into the neutral form.
//
// All the conversion is done HERE, once. The provider receives a ready neutral
// structure and translates it into its own protocol — it does not have to parse
// the client request, and therefore cannot diverge from the gateway in its
// understanding of it.

// parseMessages translates the client's messages into the neutral form.
func parseMessages(raw any) ([]provider.Message, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("the messages field is required and must be a list")
	}

	out := make([]provider.Message, 0, len(list))
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("messages[%d]: an object was expected", i)
		}
		msg, err := parseMessage(m)
		if err != nil {
			return nil, fmt.Errorf("messages[%d]: %w", i, err)
		}
		// System messages are delivered to the provider SEPARATELY, as the
		// system part (see systemFrom). Keeping them in the history as well
		// would hand the provider the same instruction twice: the system part is
		// rendered in front of the messages, so the wire carried two identical
		// system entries and the model was billed for both.
		if msg.Role == "system" {
			continue
		}
		out = append(out, msg)
	}
	return out, nil
}

// parseMessage parses a single message.
func parseMessage(m map[string]any) (provider.Message, error) {
	role, _ := m["role"].(string)
	if role == "" {
		return provider.Message{}, fmt.Errorf("the role field is required")
	}

	msg := provider.Message{Role: role}

	// Text content is either a string or a list of parts. Both forms are
	// legitimate, and both must be understood.
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
		// Empty content is acceptable for a tool call.
	default:
		return provider.Message{}, fmt.Errorf("content: unexpected type %T", content)
	}

	// The field name for reasoning differs between models, so both of the
	// common ones are checked.
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

// appendContentPart adds one content part.
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
		// An unknown part is skipped rather than dropping the request: the
		// protocol evolves, and a refusal over a new part would be worse than
		// losing it.
		_ = kind
	}
	return nil
}

// parseImagePart parses an image part.
//
// The data-URL form is the one parsed: it is self-contained and requires no
// network. A link to an external resource is deliberately unsupported — the
// gateway must not walk arbitrary addresses on the client's behalf.
func parseImagePart(part map[string]any) (provider.Image, error) {
	holder, ok := part["image_url"].(map[string]any)
	if !ok {
		if s, ok := part["image_url"].(string); ok {
			holder = map[string]any{"url": s}
		} else {
			return provider.Image{}, fmt.Errorf("image_url: an object or a string was expected")
		}
	}
	url, _ := holder["url"].(string)
	if url == "" {
		return provider.Image{}, fmt.Errorf("image_url: the url field is required")
	}

	const prefix = "data:"
	if !strings.HasPrefix(url, prefix) {
		return provider.Image{}, fmt.Errorf("image_url: only data-URLs are supported")
	}
	rest := url[len(prefix):]
	sep := strings.Index(rest, ";base64,")
	if sep <= 0 {
		return provider.Image{}, fmt.Errorf("image_url: the form data:<type>;base64,<data> was expected")
	}
	mediaType := rest[:sep]
	data, err := base64.StdEncoding.DecodeString(rest[sep+len(";base64,"):])
	if err != nil {
		return provider.Image{}, fmt.Errorf("image_url: the data does not decode: %w", err)
	}
	return provider.Image{MediaType: mediaType, Data: data}, nil
}

// parseTools translates tool declarations into the neutral form.
func parseTools(raw any) ([]provider.Tool, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("the tools field must be a list")
	}

	out := make([]provider.Tool, 0, len(list))
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("tools[%d]: an object was expected", i)
		}
		fn, ok := m["function"].(map[string]any)
		if !ok {
			continue // not a function — skipping
		}
		tool := provider.Tool{}
		if name, ok := fn["name"].(string); ok {
			tool.Name = name
		}
		if tool.Name == "" {
			return nil, fmt.Errorf("tools[%d]: the name is required", i)
		}
		if desc, ok := fn["description"].(string); ok {
			tool.Description = desc
		}
		if params, ok := fn["parameters"]; ok {
			encoded, err := json.Marshal(params)
			if err != nil {
				return nil, fmt.Errorf("tools[%d]: the parameters do not encode: %w", i, err)
			}
			tool.Schema = encoded
		}
		out = append(out, tool)
	}
	return out, nil
}

// systemFrom assembles the system part from the client request.
//
// There can be several system messages; the gateway merges them into one,
// because the provider receives the system part separately from the history.
// The order is preserved: merging in reverse order would change the meaning of
// the instructions.
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

// identifiers asks the provider for the session identifiers of this request.
//
// The rules behind them belong to the service's admission check — which header
// names it reads, which shapes it refuses, whether it reads them at all — so the
// derivation is the provider's, reached through provider.IdentifierSource. A
// provider that does not implement it gets an empty set: nothing is invented
// here, because a value minted by the gateway would encode one service's naming
// convention for everyone.
func identifiers(p provider.Provider, r *http.Request) provider.SessionIDs {
	if src, ok := p.(provider.IdentifierSource); ok {
		return src.SessionIDs(r)
	}
	return provider.SessionIDs{}
}
