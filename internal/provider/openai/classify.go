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

// Refusal classification for the OpenAI-compatible protocol.
//
// The task is one: decide whether it makes sense to switch the key and retry.
// The body, status and headers go to the client verbatim — it parses them
// itself.
//
// The signals come from two sources, because services report the same thing
// differently: by status and by a code in the body. Text heuristics are not
// applied here: the variety of wordings across services is too great, and
// guessing from the text would produce false positives. The status and an
// explicit code in the body are what the services have agreed upon.

// authCodes are the codes that mean the credentials were refused.
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

// quotaCodes are the codes that mean the quota is exhausted. For the key
// decision they are equivalent to a key refusal: the limit sits on the
// credentials, and switching the key is the only way to continue.
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
	"rate_limit_exceeded":  false, // a burst, not a quota: the key is irrelevant here
}

// Classify implements provider.Provider.
func (p *Provider) Classify(resp *http.Response, body []byte) provider.Failure {
	f := provider.Failure{
		Status:  resp.StatusCode,
		Body:    body,
		Headers: resp.Header,
	}
	f.RetryAfter = parseRetryAfter(resp.Header)

	// 401 is a key refusal by definition. 403 is added to it: services answer
	// with it both for a wrong key and for an exhausted access right, and for
	// the retry decision the difference does not matter.
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

// extractCodes collects codes from the body in the places services put them.
//
// The parsing is deliberately shallow: the body is passed to the client
// verbatim, and only marker strings are needed here.
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
	// some services return {"error":"string"} instead of an object — then
	// there are no codes, and the decision is made by status.
	return seen
}

// parseRetryAfter parses the wait headers in the same order as the client
// (packages/ai/src/route/executor.ts): milliseconds, seconds, HTTP date.
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
