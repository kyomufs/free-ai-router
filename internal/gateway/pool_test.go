package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"free-ai-router/internal/transport"
)

// notReadyTransport is a transport whose only path is missing, which is what the
// pool looks like before its first evaluation finishes.
type notReadyTransport struct{ msg string }

func (n notReadyTransport) RoundTripper() (http.RoundTripper, error) {
	return roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("%w: %s", transport.ErrNotReady, n.msg)
	}), nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A pool with no usable server is not a provider failure and must say so: the client
// is asked to come back, not to go looking for a fault in the model or the key.
func TestPoolNotReadyIsReportedAsItsOwnCondition(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstream.Close()

	h, _, st := newTestHandler(t, upstream)
	h.Transport = notReadyTransport{msg: "the pool has no usable server yet: 3 of 40 servers tested; next check in 41s"}

	rec := postChat(t, h, `{"model":"test/model","messages":[{"role":"user","content":"hello"}]}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	if got := rec.Header().Get("retry-after"); got != "30" {
		t.Fatalf("retry-after %q", got)
	}
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("body %s: %v", rec.Body.String(), err)
	}
	if envelope.Error.Type != "pool" || envelope.Error.Code != "pool" {
		t.Fatalf("error %+v", envelope.Error)
	}
	if envelope.Error.Message == "" {
		t.Fatal("the message must say what is happening")
	}
	if day := st.Snapshot().Days[0]; day.Aborted != 1 || day.Refused != 0 {
		t.Fatalf("a missing path is not a refusal: %+v", day)
	}
}
