package opencode

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"free-ai-router/internal/provider"
)

// Классификация отказа.
//
// ЗАЧЕМ ОНА ВООБЩЕ НУЖНА, если шлюз передаёт отказ клиенту дословно.
// Ровно ради одного вопроса: осмысленно ли сменить ключ и повторить. Тело,
// статус и заголовки уходят клиенту без изменений — их разбирает он сам.
// Провайдер отвечает только на вопрос про ключ.
//
// Источник признаков — packages/ai/src/provider-error.ts (opencode 2.0.30),
// функция classifyProviderFailure. Провайдер использует ту же логику, что и
// клиент, чтобы решение шлюза не расходилось с решением клиента.

// quotaCodes — коды, означающие исчерпание квоты, а не всплеск запросов.
//
// В клиенте это QUOTA_CODES; совпадение важно: если шлюз решит, что дело в
// ключе, а клиент увидит квоту, они разойдутся в том, повторять ли запрос.
var quotaCodes = map[string]bool{
	"insufficient_quota":     true,
	"usage_not_included":     true,
	"billing_error":          true,
	"gousagelimiterror":      true,
	"freeusagelimiterror":    true,
	"creditlimitexceeded":    true,
	"freetiererror":          true,
	"blackusagelimiterror":   true,
	"gousagelimitexceeded":   true,
	"freeusagelimitexceeded": true,
	"insufficient_credit":    true,
	"quota_exceeded":         true,
	"quotaexceedederror":     true,
}

// authCodes — коды, означающие отказ в учётных данных.
var authCodes = map[string]bool{
	"authentication_error": true,
	"permission_error":     true,
	"permission_denied":    true,
	"api_key_invalid":      true,
	"invalid_api_key":      true,
	"invalidcredential":    true,
	"invalid_credential":   true,
	"autherror":            true,
	"unauthorized":         true,
}

// quotaText — текстовые признаки квоты. Применяются только к 429 и 402, где
// всплеск запросов и исчерпание квоты делят один статус.
var quotaText = regexp.MustCompile(`(?i)insufficient[-_\s]?(?:quota|balance)|quota[-_\s]?exceeded|budget exceeded|usage limit|limit exhausted|credit limit|free tier`)

// authText — текстовые признаки отказа в ключе.
var authText = regexp.MustCompile(`(?i)incorrect api key|invalid api key|api key is invalid|unauthenticated`)

// Classify реализует provider.Provider.
//
// Решение о ключе принимается по совокупности признаков, потому что сервисы
// сообщают об одном и том же разными способами: статусом, кодом в теле, типом
// или текстом. Провайдер обязан распознать отказ в ключе во всех формах,
// иначе шлюз не сменит ключ там, где это помогло бы.
func (p *Provider) Classify(resp *http.Response, body []byte) provider.Failure {
	f := provider.Failure{
		Status:  resp.StatusCode,
		Body:    body,
		Headers: resp.Header,
	}

	f.RetryAfter = parseRetryAfter(resp.Header)

	codes := extractCodes(body)
	lower := strings.ToLower(string(body))

	// Отказ в ключе. 401 и 403 относятся сюда: сервис отвечает ими и на
	// неверный ключ, и на исчерпанное право доступа.
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		f.KeyRejected = true
	case resp.StatusCode == http.StatusForbidden:
		// 403 бывает и региональным запретом, и отказом по аттестации.
		// Для решения о ключе различие не важно: если ключ у шлюза есть,
		// попробовать другой осмысленно, а если нет — повтор не состоится.
		f.KeyRejected = true
	}
	for _, code := range codes {
		if authCodes[code] {
			f.KeyRejected = true
		}
		// Коды квоты означают, что ограничение сидит на учётных данных:
		// смена ключа — единственный способ продолжить.
		if quotaCodes[code] {
			f.KeyRejected = true
		}
	}
	if authText.MatchString(lower) {
		f.KeyRejected = true
	}
	if (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusPaymentRequired) && quotaText.MatchString(lower) {
		f.KeyRejected = true
	}

	return f
}

// extractCodes вытаскивает коды из тела в тех местах, где их кладут разные
// сервисы.
//
// Разбор намеренно поверхностный: тело передаётся клиенту ДОСЛОВНО, и
// единственная задача здесь — собрать строки-признаки, не меняя сам ответ.
func extractCodes(body []byte) []string {
	var seen []string
	add := func(v any) {
		s, ok := v.(string)
		if !ok || s == "" {
			return
		}
		seen = append(seen, strings.ToLower(s))
	}

	var top map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(body), &top); err != nil {
		return seen
	}

	add(top["code"])
	add(top["error_type"])
	add(top["type"])

	if err, ok := top["error"].(map[string]any); ok {
		add(err["code"])
		add(err["type"])
		add(err["status"])
		add(err["error_type"])
		// Некоторые сервисы кладут причину в innererror.
		if inner, ok := err["innererror"].(map[string]any); ok {
			add(inner["code"])
		}
		// И в metadata.
		if md, ok := err["metadata"].(map[string]any); ok {
			add(md["error_type"])
		}
	}

	// Обёртка вида {"type":"error","error":{...}} уже разобрана выше.
	// Осталось вытащить метку из переписанного сообщения: сервис заменяет код
	// на свой и оставляет исходный в квадратных скобках в начале текста.
	if err, ok := top["error"].(map[string]any); ok {
		if msg, ok := err["message"].(string); ok {
			if label := gatewayCodeLabel(msg); label != "" {
				seen = append(seen, strings.ToLower(label))
			}
		}
	} else if msg, ok := top["message"].(string); ok {
		if label := gatewayCodeLabel(msg); label != "" {
			seen = append(seen, strings.ToLower(label))
		}
	}

	return seen
}

// gatewayCodeLabelMatch — метка исходного кода в начале переписанного
// сообщения, например "Gateway: [rate_limit_exceeded] ...".
var gatewayCodeLabelMatch = regexp.MustCompile(`^[^:\n]+: \[([A-Za-z0-9_.-]+)\]`)

// gatewayCodeLabel возвращает метку кода, если она есть.
func gatewayCodeLabel(message string) string {
	if m := gatewayCodeLabelMatch.FindStringSubmatch(message); len(m) == 2 {
		return m[1]
	}
	return ""
}

// parseRetryAfter разбирает заголовки ожидания.
//
// Порядок тот же, что у клиента (packages/ai/src/route/executor.ts):
// сначала миллисекундный заголовок, затем секунды, затем HTTP-дата.
// Значение может понадобиться ротации ключей, чтобы решить, ждать или менять
// ключ.
func parseRetryAfter(h http.Header) time.Duration {
	if h == nil {
		return 0
	}
	if v := h.Get("retry-after-ms"); v != "" {
		if ms, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && ms >= 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	v := h.Get("retry-after")
	if v == "" {
		return 0
	}
	v = strings.TrimSpace(v)
	if secs, err := strconv.ParseFloat(v, 64); err == nil && secs >= 0 {
		return time.Duration(secs * float64(time.Second))
	}
	if at, err := http.ParseTime(v); err == nil {
		d := time.Until(at)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}
