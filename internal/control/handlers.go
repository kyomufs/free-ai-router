package control

import (
	"net/http"
	"sort"

	"free-ai-router/internal/provider"
	"free-ai-router/internal/state"
)

// Status — снимок состояния демона.
//
// Снимок, а не живой объект: ответ обязан быть согласованным между полями.
// Отдавая живые структуры, шлюз мог бы показать реестр до изменения, а
// счётчики после.
type Status struct {
	// Mode — форма развёртывания.
	Mode string `json:"mode"`
	// Listen — адрес прослушивания.
	Listen string `json:"listen"`
	// UptimeSeconds — сколько демон работает.
	UptimeSeconds int64 `json:"uptime_seconds"`
	// Providers — провайдеры с числом моделей и ключей.
	Providers []ProviderStatus `json:"providers"`
	// Totals — суммарные счётчики за сегодня.
	Totals state.DaySnapshot `json:"totals_today"`
}

// ProviderStatus — состояние одного провайдера.
type ProviderStatus struct {
	// ID — идентификатор.
	ID string `json:"id"`
	// Models — сколько моделей объявлено.
	Models int `json:"models"`
	// FreeModels — сколько из них бесплатных.
	FreeModels int `json:"free_models"`
	// Keys — сколько ключей в работе. Ноль означает анонимную работу.
	Keys int `json:"keys"`
	// KeyRotation — подключён ли механизм смены ключа.
	KeyRotation bool `json:"key_rotation"`
}

// handleStatus отвечает снимком состояния.
func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "status")
		return
	}

	st := Status{}
	if h.Config != nil {
		st.Mode = h.Config.Mode.Val
		st.Listen = h.Config.Listen.Val
	}
	if h.State != nil {
		snap := h.State.Snapshot()
		if len(snap.Days) > 0 {
			st.Totals = snap.Days[0]
		}
	}

	if h.Providers != nil {
		for _, p := range h.Providers.All() {
			ps := ProviderStatus{ID: string(p.ID())}
			if models, err := p.Models(r.Context()); err == nil {
				ps.Models = len(models)
				for _, m := range models {
					if m.Free {
						ps.FreeModels++
					}
				}
			}
			if h.KeyRotation != nil {
				keys := h.KeyRotation.Keys(p.ID())
				ps.Keys = len(keys)
				ps.KeyRotation = h.KeyRotation.HasRotation(p.ID())
			}
			st.Providers = append(st.Providers, ps)
		}
	}

	writeJSON(w, http.StatusOK, st)
}

// handleStats отвечает счётчиками по дням.
func (h *Handler) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "stats")
		return
	}
	if h.State == nil {
		writeJSON(w, http.StatusOK, state.Snapshot{})
		return
	}
	writeJSON(w, http.StatusOK, h.State.Snapshot())
}

// handleModels отвечает каталогом всех провайдеров.
func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "models")
		return
	}

	type entry struct {
		Provider  string `json:"provider"`
		ID        string `json:"id"`
		Name      string `json:"name"`
		Context   int    `json:"context"`
		MaxOutput int    `json:"max_output"`
		Vision    bool   `json:"vision"`
		Reasoning bool   `json:"reasoning"`
		Free      bool   `json:"free"`
	}

	var out []entry
	if h.Providers != nil {
		for _, p := range h.Providers.All() {
			models, err := p.Models(r.Context())
			if err != nil {
				continue
			}
			for _, m := range models {
				out = append(out, entry{
					Provider: string(p.ID()), ID: m.ID, Name: m.Name,
					Context: m.Context, MaxOutput: m.MaxOutput,
					Vision: m.Vision, Reasoning: m.Reasoning, Free: m.Free,
				})
			}
		}
	}
	if out == nil {
		out = []entry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": out})
}

// KeysResponse — список ключей по провайдерам.
//
// Отдаются ОТПЕЧАТКИ, а не сами ключи: справка нужна, чтобы понять, сколько
// ключей в работе, а не чтобы увидеть секрет.
type KeysResponse struct {
	Providers []ProviderKeys `json:"providers"`
}

// ProviderKeys — ключи одного провайдера.
type ProviderKeys struct {
	Provider string   `json:"provider"`
	Keys     []string `json:"keys"`
	Rotation bool     `json:"rotation"`
}

// handleKeys отвечает списком ключей.
func (h *Handler) handleKeys(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "keys")
		return
	}
	writeJSON(w, http.StatusOK, h.keysResponse())
}

// keysResponse собирает ответ со списком ключей.
func (h *Handler) keysResponse() KeysResponse {
	var resp KeysResponse
	if h.Providers == nil || h.KeyRotation == nil {
		resp.Providers = []ProviderKeys{}
		return resp
	}
	for _, id := range h.Providers.IDs() {
		fingerprints := h.KeyRotation.KeyFingerprints(id)
		resp.Providers = append(resp.Providers, ProviderKeys{
			Provider: string(id),
			Keys:     fingerprints,
			Rotation: h.KeyRotation.HasRotation(id),
		})
	}
	if resp.Providers == nil {
		resp.Providers = []ProviderKeys{}
	}
	return resp
}

// keyRequest — тело запроса на добавление или удаление ключа.
type keyRequest struct {
	// Provider — к какому провайдеру относится ключ.
	Provider string `json:"provider"`
	// Key — сам ключ (при добавлении) или его отпечаток (при удалении).
	Key string `json:"key"`
}

// handleKeyAdd добавляет ключ.
//
// Ключ принимается в теле, а не в адресе: адрес попал бы в журналы доступа и в
// историю оболочки.
func (h *Handler) handleKeyAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "keys/add")
		return
	}
	var req keyRequest
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "тело не разбирается: " + err.Error()})
		return
	}
	if req.Provider == "" || req.Key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "нужны поля provider и key"})
		return
	}
	if h.KeyRotation == nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "механизм ротации ключей не подключён"})
		return
	}

	added := h.KeyRotation.AddKey(provider.ID(req.Provider), req.Key)
	h.info("ключ добавлен для %q (новый: %t)", req.Provider, added)
	writeJSON(w, http.StatusOK, map[string]any{"added": added, "keys": h.keysResponse()})
}

// handleKeyRemove убирает ключ по отпечатку.
//
// Отпечаток, а не сам ключ: удаляющий не обязан знать секрет, ему достаточно
// того, что показала справка.
func (h *Handler) handleKeyRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "keys/remove")
		return
	}
	var req keyRequest
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "тело не разбирается: " + err.Error()})
		return
	}
	if req.Provider == "" || req.Key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "нужны поля provider и key"})
		return
	}
	if h.KeyRotation == nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "механизм ротации ключей не подключён"})
		return
	}

	removed := h.KeyRotation.RemoveFingerprint(provider.ID(req.Provider), req.Key)
	h.info("ключ удалён для %q (найден: %t)", req.Provider, removed)
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed, "keys": h.keysResponse()})
}

// OptionsResponse — то, что можно выбрать в интерфейсах.
//
// Отдаётся демоном, а не хранится в CLI и TUI: список значений живёт в одном
// месте, и его нельзя забыть обновить. Копия в интерфейсе устаревает молча —
// именно так и случалось раньше.
type OptionsResponse struct {
	// Providers — идентификаторы провайдеров.
	Providers []string `json:"providers"`
	// Modes — формы развёртывания.
	Modes []string `json:"modes"`
	// Transports — доступные транспорты.
	Transports []string `json:"transports"`
	// Toggle — словарь включения и выключения.
	Toggle []string `json:"toggle"`
	// Scopes — области очистки данных.
	Scopes []string `json:"scopes"`
}

// handleOptions отвечает словарями значений.
func (h *Handler) handleOptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "options")
		return
	}

	resp := OptionsResponse{
		Modes:  []string{"local", "server"},
		Toggle: []string{"on", "off"},
		Scopes: []string{"logs", "stats", "all"},
	}
	if h.Providers != nil {
		for _, id := range h.Providers.IDs() {
			resp.Providers = append(resp.Providers, string(id))
		}
	}
	if resp.Providers == nil {
		resp.Providers = []string{}
	}
	if h.Transports != nil {
		resp.Transports = h.Transports
	} else {
		resp.Transports = []string{}
	}
	writeJSON(w, http.StatusOK, resp)
}

// methodNotAllowed отвечает на неверный метод.
func methodNotAllowed(w http.ResponseWriter, route string) {
	writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
		"error": "метод не поддерживается для " + route,
	})
}

// info пишет сообщение в журнал, если он есть.
func (h *Handler) info(format string, args ...any) {
	if h.Log != nil {
		h.Log.Info(format, args...)
	}
}

// sortedKeys возвращает ключи карты в стабильном порядке.
//
// Нужна для диагностического вывода: без неё порядок менялся бы между
// вызовами, и два снимка нельзя было бы сравнить.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
