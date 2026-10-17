package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"free-ai-router/internal/config"
	"free-ai-router/internal/provider"
	"free-ai-router/internal/state"
)

// stubProvider — провайдер-подставка для проверок control API.
type stubProvider struct {
	id     provider.ID
	models []provider.Model
}

func (s *stubProvider) ID() provider.ID { return s.id }

func (s *stubProvider) Models(context.Context) ([]provider.Model, error) {
	return s.models, nil
}

func (s *stubProvider) Build(provider.Request) (provider.Prepared, error) {
	return provider.Prepared{}, nil
}

func (s *stubProvider) Classify(*http.Response, []byte) provider.Failure {
	return provider.Failure{}
}

// stubKeys — механизм ротации ключей для проверок.
type stubKeys struct {
	keys map[provider.ID][]string
}

func (s *stubKeys) Keys(id provider.ID) []string { return s.keys[id] }

func (s *stubKeys) KeyFingerprints(id provider.ID) []string {
	out := make([]string, 0, len(s.keys[id]))
	for _, k := range s.keys[id] {
		out = append(out, "fp-"+k)
	}
	return out
}

func (s *stubKeys) HasRotation(id provider.ID) bool { return len(s.keys[id]) > 1 }

func (s *stubKeys) AddKey(id provider.ID, key string) bool {
	for _, existing := range s.keys[id] {
		if existing == key {
			return false
		}
	}
	s.keys[id] = append(s.keys[id], key)
	return true
}

func (s *stubKeys) RemoveFingerprint(id provider.ID, fingerprint string) bool {
	want := strings.TrimPrefix(fingerprint, "fp-")
	for i, k := range s.keys[id] {
		if k == want {
			s.keys[id] = append(s.keys[id][:i], s.keys[id][i+1:]...)
			return true
		}
	}
	return false
}

// newTestControl собирает обработчик для проверок.
func newTestControl(t *testing.T) (*Handler, *http.ServeMux) {
	t.Helper()

	reg := provider.NewRegistry()
	reg.Register(&stubProvider{
		id: "первый",
		models: []provider.Model{
			{ID: "м1", Name: "Модель 1", Free: true, Context: 1000},
			{ID: "м2", Name: "Модель 2", Free: true, Context: 2000, Reasoning: true},
		},
	})
	reg.Register(&stubProvider{id: "второй", models: []provider.Model{{ID: "м3", Free: false}}})

	st, err := state.Open(t.TempDir() + "/state.json")
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Mode.Set("local", config.SourceDefault)
	cfg.Listen.Set("127.0.0.1:8787", config.SourceDefault)

	h := &Handler{
		Providers: reg,
		KeyRotation: &stubKeys{keys: map[provider.ID][]string{
			"первый": {"ключ1", "ключ2"},
		}},
		State:      st,
		Config:     cfg,
		Transports: []string{"direct"},
	}

	mux := http.NewServeMux()
	h.Routes(mux)
	return h, mux
}

// get выполняет GET-запрос к control API.
func get(t *testing.T, mux *http.ServeMux, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestTokenIsEnforced — в режиме server проверка оборачивает И control API.
//
// Control умеет израсходовать ключи провайдера, поэтому открытый control
// обнулил бы проверку шлюза.
func TestTokenIsEnforced(t *testing.T) {
	_, mux := newTestControl(t)
	h := &Handler{RequireToken: true, Token: "секрет", Providers: provider.NewRegistry()}
	mux2 := http.NewServeMux()
	h.Routes(mux2)

	// Без токена — отказ.
	rec := get(t, mux2, Prefix+"status", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status without a token = %d, want 401", rec.Code)
	}

	// С неверным токеном — тоже отказ.
	rec = get(t, mux2, Prefix+"status", map[string]string{"authorization": "Bearer неверный"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status with a wrong token = %d, want 401", rec.Code)
	}

	// С верным — доступ есть.
	rec = get(t, mux2, Prefix+"status", map[string]string{"authorization": "Bearer секрет"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status with the right token = %d, want 200", rec.Code)
	}

	_ = mux
}

// TestLocalModeNeedsNoToken — в local проверка не действует.
func TestLocalModeNeedsNoToken(t *testing.T) {
	_, mux := newTestControl(t)
	if rec := get(t, mux, Prefix+"status", nil); rec.Code != http.StatusOK {
		t.Fatalf("status in local mode = %d, want 200", rec.Code)
	}
}

// TestStatusReportsProvidersAndCounters — снимок содержит провайдеров и
// сегодняшние счётчики.
func TestStatusReportsProvidersAndCounters(t *testing.T) {
	h, mux := newTestControl(t)
	h.State.RecordSuccess("м1", "")
	h.State.RecordRefusal("м2", "")

	rec := get(t, mux, Prefix+"status", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var st Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("the status body is not JSON: %v", err)
	}
	if st.Mode != "local" {
		t.Fatalf("Mode = %q, want local", st.Mode)
	}
	if len(st.Providers) != 2 {
		t.Fatalf("Providers = %d, want 2", len(st.Providers))
	}

	byID := map[string]ProviderStatus{}
	for _, p := range st.Providers {
		byID[p.ID] = p
	}
	if got := byID["первый"]; got.Models != 2 || got.FreeModels != 2 {
		t.Fatalf("первый = %+v, want 2 models both free", got)
	}
	if got := byID["первый"]; got.Keys != 2 || !got.KeyRotation {
		t.Fatalf("первый keys = %+v, want 2 keys with rotation", got)
	}
	if got := byID["второй"]; got.Keys != 0 || got.KeyRotation {
		t.Fatalf("второй = %+v, want no keys", got)
	}

	if st.Totals.OK != 1 || st.Totals.Refused != 1 {
		t.Fatalf("totals = ok %d refused %d, want 1 and 1", st.Totals.OK, st.Totals.Refused)
	}
}

// TestStatsReportsDays — счётчики отдаются по дням.
func TestStatsReportsDays(t *testing.T) {
	h, mux := newTestControl(t)
	h.State.RecordSuccess("м1", "")

	rec := get(t, mux, Prefix+"stats", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("stats = %d", rec.Code)
	}
	var snap state.Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("the stats body is not JSON: %v", err)
	}
	if len(snap.Days) != 1 || snap.Days[0].OK != 1 {
		t.Fatalf("days = %+v", snap.Days)
	}
}

// TestModelsListsEveryProvider — каталог содержит модели всех провайдеров.
func TestModelsListsEveryProvider(t *testing.T) {
	_, mux := newTestControl(t)

	rec := get(t, mux, Prefix+"models", nil)
	var body struct {
		Models []struct {
			Provider string `json:"provider"`
			ID       string `json:"id"`
			Free     bool   `json:"free"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if len(body.Models) != 3 {
		t.Fatalf("models = %d, want 3", len(body.Models))
	}
	providers := map[string]int{}
	for _, m := range body.Models {
		providers[m.Provider]++
	}
	if providers["первый"] != 2 || providers["второй"] != 1 {
		t.Fatalf("provider split = %v", providers)
	}
}

// TestKeysAreFingerprinted — справка отдаёт отпечатки, а не секреты.
//
// Оператору нужно понять, сколько ключей в работе, а не увидеть ключ.
func TestKeysAreFingerprinted(t *testing.T) {
	_, mux := newTestControl(t)

	rec := get(t, mux, Prefix+"keys", nil)
	body := rec.Body.String()
	if strings.Contains(body, `"ключ1"`) {
		t.Fatalf("the raw key leaked into the response: %s", body)
	}
	if !strings.Contains(body, "fp-ключ1") {
		t.Fatalf("the fingerprint is missing: %s", body)
	}
}

// TestKeyAddAndRemove — ключ добавляется и удаляется, при этом повторное
// добавление не создаёт дубликат.
func TestKeyAddAndRemove(t *testing.T) {
	_, mux := newTestControl(t)

	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// Добавление нового ключа.
	rec := post(Prefix+"keys/add", `{"provider":"первый","key":"ключ3"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("add = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"added": true`) {
		t.Fatalf("the first add must report a change: %s", rec.Body.String())
	}

	// Повторное добавление того же ключа изменений не вносит.
	rec = post(Prefix+"keys/add", `{"provider":"первый","key":"ключ3"}`)
	if !strings.Contains(rec.Body.String(), `"added": false`) {
		t.Fatalf("a duplicate add must report no change: %s", rec.Body.String())
	}

	// Удаление по отпечатку.
	rec = post(Prefix+"keys/remove", `{"provider":"первый","key":"fp-ключ3"}`)
	if !strings.Contains(rec.Body.String(), `"removed": true`) {
		t.Fatalf("remove must report a change: %s", rec.Body.String())
	}
	rec = post(Prefix+"keys/remove", `{"provider":"первый","key":"fp-нет-такого"}`)
	if !strings.Contains(rec.Body.String(), `"removed": false`) {
		t.Fatalf("removing an absent key must report no change: %s", rec.Body.String())
	}
}
