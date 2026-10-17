package openai

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"free-ai-router/internal/provider"
)

// Классификация отказа для OpenAI-совместимого протокола.
//
// Задача одна: решить, осмысленно ли сменить ключ и повторить. Тело, статус и
// заголовки уходят клиенту дословно — их разбирает он сам.
//
// Признаки берутся из двух источников, потому что сервисы сообщают об одном и
// том же по-разному: статусом и кодом в теле. Текстовые эвристики здесь не
// применяются: разнообразие формулировок у разных сервисов слишком велико, и
// попытка угадать по тексту давала бы ложные срабатывания. Статус и явный код
// в теле — это то, о чём сервисы договорились.

// authCodes — коды, означающие отказ в учётных данных.
var authCodes = map[string]bool{
	"invalid_api_key":     true,
	"invalidapikey":       true,
	"authentication":      true,
	"authenticationerror": true,
	"permission_error":    true,
	"permission_denied":   true,
	"unauthorized":        true,
	"invalid_credential":  true,
	"invalidcredential":   true,
}

// quotaCodes — коды исчерпания квоты. Для решения о ключе они равнозначны
// отказу в ключе: ограничение сидит на учётных данных, и сменить ключ —
// единственный способ продолжить.
var quotaCodes = map[string]bool{
	"insufficient_quota":   true,
	"insufficientquota":    true,
	"quota_exceeded":       true,
	"quotaexceeded":        true,
	"insufficient_balance": true,
	"credits_exhausted":    true,
	"billing_error":        true,
	"usage_limit_exceeded": true,
	"free_tier_exhausted":  true,
	"rate_limit_exceeded":  false, // всплеск, а не квота: ключ тут ни при чём
}

// Classify реализует provider.Provider.
func (p *Provider) Classify(resp *http.Response, body []byte) provider.Failure {
	f := provider.Failure{
		Status:  resp.StatusCode,
		Body:    body,
		Headers: resp.Header,
	}
	f.RetryAfter = parseRetryAfter(resp.Header)

	// 401 — отказ в ключе по определению. 403 добавляется к нему: сервисы
	// отвечают им и на неверный ключ, и на исчерпанное право доступа, а для
	// решения о повторе различие не важно.
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		f.KeyRejected = true
	}

	for _, code := range extractCodes(body) {
		if authCodes[code] || quotaCodes[code] {
			f.KeyRejected = true
		}
	}
	return f
}

// extractCodes собирает коды из тела в тех местах, где их кладут сервисы.
//
// Разбор поверхностный намеренно: тело передаётся клиенту дословно, и здесь
// нужны только строки-признаки.
func extractCodes(body []byte) []string {
	var seen []string
	add := func(v any) {
		if s, ok := v.(string); ok && s != "" {
			seen = append(seen, strings.ToLower(s))
		}
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
		add(err["error_type"])
		if inner, ok := err["innererror"].(map[string]any); ok {
			add(inner["code"])
		}
	}
	// Некоторые сервисы отдают {"error":"строка"} вместо объекта — тогда
	// кодов нет, и решение принимается по статусу.
	return seen
}

// parseRetryAfter разбирает заголовки ожидания в том же порядке, что и клиент
// (packages/ai/src/route/executor.ts): миллисекунды, секунды, HTTP-дата.
func parseRetryAfter(h http.Header) time.Duration {
	if h == nil {
		return 0
	}
	if v := h.Get("retry-after-ms"); v != "" {
		if ms, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && ms >= 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	v := strings.TrimSpace(h.Get("retry-after"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil && secs >= 0 {
		return time.Duration(secs * float64(time.Second))
	}
	if at, err := http.ParseTime(v); err == nil {
		if d := time.Until(at); d > 0 {
			return d
		}
	}
	return 0
}
