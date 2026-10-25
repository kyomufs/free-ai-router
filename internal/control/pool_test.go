package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"free-ai-router/internal/pool"
)

// stubPool is a pool transport that reports a fixed state.
type stubPool struct {
	state    pool.Status
	rechecks int
}

func (s *stubPool) Status() pool.Status { return s.state }

func (s *stubPool) Recheck(context.Context) { s.rechecks++ }

func TestPoolReportsItsList(t *testing.T) {
	h, mux := newTestControl(t)
	h.Pool = &stubPool{state: pool.Status{
		Relevant:  2,
		EvalState: "ready",
		Nodes: []pool.View{
			{ID: "a", Label: "🇩🇪 de", Proto: "vless", Host: "h.example", Port: 443, ExitIP: "203.0.113.7"},
			{ID: "b", Label: "🇫🇷 fr", Proto: "trojan", Host: "f.example", Port: 443, ExitIP: "203.0.113.8"},
		},
		Quarantined: []pool.QuarantineView{{Node: pool.View{ID: "c", Host: "x.example"}, Reason: "silence"}},
	}}

	rec := get(t, mux, Prefix+"pool", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got pool.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body %s: %v", rec.Body.String(), err)
	}
	if got.Relevant != 2 || len(got.Nodes) != 2 || got.Nodes[0].ExitIP != "203.0.113.7" {
		t.Fatalf("state %+v", got)
	}
	if len(got.Quarantined) != 1 || got.Quarantined[0].Reason != "silence" {
		t.Fatalf("quarantine %+v", got.Quarantined)
	}
	// The link carries the node's credentials and is not part of the report.
	if body := rec.Body.String(); len(body) > 0 && jsonHasKey(rec.Body.Bytes(), "raw") {
		t.Fatalf("the report exposes the link: %s", body)
	}
}

func TestPoolRecheckIsAccepted(t *testing.T) {
	h, mux := newTestControl(t)
	stub := &stubPool{}
	h.Pool = stub

	req := httptest.NewRequest(http.MethodPost, Prefix+"pool", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if stub.rechecks != 1 {
		t.Fatalf("rechecks %d", stub.rechecks)
	}
}

func TestPoolIsAbsentInDirectMode(t *testing.T) {
	_, mux := newTestControl(t)
	rec := get(t, mux, Prefix+"pool", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

// jsonHasKey reports whether the encoded object carries the key anywhere in it.
func jsonHasKey(raw []byte, key string) bool {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return false
	}
	return walkFor(v, key)
}

func walkFor(v any, key string) bool {
	switch t := v.(type) {
	case map[string]any:
		for k, inner := range t {
			if k == key || walkFor(inner, key) {
				return true
			}
		}
	case []any:
		for _, inner := range t {
			if walkFor(inner, key) {
				return true
			}
		}
	}
	return false
}
