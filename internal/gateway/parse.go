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

// sessionIDs derives the session identifiers by the contract.
//
// The client may send its own identifiers in the headers; if it did, they are
// respected. If not, they are derived by the formula, because the providers
// that have a check expect an identifier of the right shape.
func (h *Handler) sessionIDs(r *http.Request, body map[string]any) provider.SessionIDs {
	ids := provider.SessionIDs{
		Session:  firstNonEmpty(r.Header.Get("x-opencode-session-id"), r.Header.Get("x-session-id")),
		Affinity: firstNonEmpty(r.Header.Get("x-opencode-session"), r.Header.Get("x-session-affinity")),
		Project:  r.Header.Get("x-opencode-project"),
		Message:  r.Header.Get("x-opencode-request"),
		Parent:   firstNonEmpty(r.Header.Get("x-opencode-parent-session-id"), r.Header.Get("x-parent-session-id")),
	}

	if ids.Session == "" || !wire.IsCanonicalSessionID(ids.Session) {
		// The free lane refuses the whole call when a session header carries an
		// id of the wrong shape: measured on 2026-10-19, a request that differs
		// only in `x-opencode-session: ses_test` gets FreeTierError, while the
		// same request with `ses_` + 12 hex + 14 base62 is answered. The client
		// always mints ids of that shape, so a value that does not match came
		// from someone else's naming convention, and forwarding it would fail a
		// call over a header the model never sees.
		ids.Session = wire.NewSessionID()
	}
	if ids.Parent != "" && !wire.IsCanonicalSessionID(ids.Parent) {
		// A parent of the wrong shape is dropped rather than replaced: it only
		// feeds the affinity below, and deriving a parent would invent a
		// hierarchy the client never described.
		ids.Parent = ""
	}
	if ids.Affinity == "" || !wire.IsCanonicalSessionID(ids.Affinity) {
		// Without a parent, affinity equals the session identifier — that is
		// what the client does too.
		ids.Affinity = firstNonEmpty(ids.Parent, ids.Session)
	}
	if ids.Message == "" || isRejectedRequestID(ids.Message) {
		// The header wants the identifier of the MESSAGE being answered and
		// refuses a per-request one. A client that sent a `req_` value was
		// following the naming convention of a different identifier, so the
		// value is replaced rather than forwarded: the prefix alone fails the
		// whole call, and failing it over a header nobody reads is worse than
		// deriving a proper one.
		ids.Message = wire.NewMessageID()
	}
	if ids.Project == "" || isRejectedProjectID(ids.Project) {
		// Outside a git project the client sends the literal "global", and a
		// `prj_`-prefixed value is refused regardless of what follows it. The
		// project identifier belongs in the request body or path, never here.
		ids.Project = "global"
	}
	return ids
}

// Prefixes the upstream refuses to see in these two headers.
//
// MEASURED, one header at a time against the live service:
//
//	x-opencode-project: prj_f698f87ea99d390458196363 -> refused
//	x-opencode-project: global                       -> admitted
//	x-opencode-project: <absent>                     -> admitted
//	x-opencode-request: req_2cb728ee0be94ed0c7b20920b21fde87 -> refused
//	x-opencode-request: msg_13b2d53f2001ia66lVkDLegkmV       -> admitted
//	x-opencode-request: <absent>                             -> admitted
//
// What is refused is the PREFIX, not the value: any `prj_`-prefixed string is
// rejected regardless of what follows it. The check is therefore on the marker
// rather than on the contents.
const (
	rejectedProjectPrefix = "prj_"
	rejectedRequestPrefix = "req_"
)

// isRejectedProjectID reports whether a project header value carries the
// refused marker.
func isRejectedProjectID(v string) bool { return strings.HasPrefix(v, rejectedProjectPrefix) }

// isRejectedRequestID reports whether a request header value carries the
// refused marker.
func isRejectedRequestID(v string) bool { return strings.HasPrefix(v, rejectedRequestPrefix) }

// firstNonEmpty returns the first non-empty argument.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
