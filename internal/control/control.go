// Package control — управление работающим демоном.
//
// ЗАЧЕМ ОТДЕЛЬНАЯ ПОВЕРХНОСТЬ. Демон держит реестр провайдеров, механизмы
// ротации и счётчики; оператору нужно уметь посмотреть на них и изменить то,
// что меняется без перезапуска. Control API даёт для этого один набор
// маршрутов, и им одинаково пользуются CLI, TUI и сторонние скрипты.
//
// ЧТО МЕНЯЕТСЯ НА ЖИВОМ ДЕМОНЕ, а что нет. Меняется состав ключей и то, какие
// из них в работе. Не меняется всё, что относится к сокету и к реестру
// провайдеров: адрес прослушивания уже занят, а реестр собран из конфигурации
// при старте. Попытка сменить их на живом демоне вернула бы состояние, которое
// никто не исполняет, — и это хуже отказа.
//
// ТОКЕН. В режиме server проверка оборачивает и эту поверхность: control умеет
// израсходовать ключи провайдера, поэтому открытый control обнулил бы проверку
// шлюза.
package control

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"free-ai-router/internal/config"
	"free-ai-router/internal/gateway"
	"free-ai-router/internal/provider"
	"free-ai-router/internal/state"
)

// Prefix — префикс всех маршрутов управления.
const Prefix = "/_control/"

// Handler обслуживает маршруты управления.
type Handler struct {
	// Providers — реестр провайдеров.
	Providers *provider.Registry
	// KeyRotation — механизмы ротации ключей по провайдерам.
	KeyRotation gateway.KeyRotationRegistry
	// State — счётчики.
	State *state.Store
	// Log — журнал.
	Log *state.Logger
	// Config — действующая конфигурация. Только для чтения: смена значений,
	// влияющих на сокет, требует перезапуска.
	Config *config.Config

	// Transports — доступные транспорты. Заполняется точкой входа, потому
	// что список словарей живёт в одном месте, а не копируется в интерфейсы.
	Transports []string

	// Token и RequireToken — проверка доступа.
	Token        string
	RequireToken bool
}

// Routes регистрирует маршруты.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc(Prefix, h.serve)
}

// serve разбирает маршрут и вызывает обработчик.
func (h *Handler) serve(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error": "требуется токен",
		})
		return
	}

	switch strings.TrimPrefix(r.URL.Path, Prefix) {
	case "status":
		h.handleStatus(w, r)
	case "stats":
		h.handleStats(w, r)
	case "models":
		h.handleModels(w, r)
	case "keys":
		h.handleKeys(w, r)
	case "keys/add":
		h.handleKeyAdd(w, r)
	case "keys/remove":
		h.handleKeyRemove(w, r)
	case "options":
		h.handleOptions(w, r)
	default:
		http.NotFound(w, r)
	}
}

// authorized проверяет токен.
//
// Сравнение уже не побайтовое с ранним выходом: используется
// constant-time-сравнение, потому что побайтовый выход утекает префикс секрета
// через время ответа.
func (h *Handler) authorized(r *http.Request) bool {
	if !h.RequireToken {
		return true
	}
	presented := bearerToken(r)
	return subtleEquals(presented, h.Token)
}

// bearerToken достаёт токен из заголовка.
func bearerToken(r *http.Request) string {
	v := r.Header.Get("authorization")
	if len(v) > 7 && strings.EqualFold(v[:7], "bearer ") {
		return v[7:]
	}
	return v
}

// decodeBody разбирает тело запроса.
func decodeBody(r *http.Request, dst any) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

// writeJSON отвечает телом JSON.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// subtleEquals сравнивает строки за постоянное время.
//
// Длина утекает в любом случае: сравнивать разной длины значения за постоянное
// время нельзя, не зная заранее длину. Но содержимое — можно, и именно оно
// секрет: побайтовый выход с ранним возвратом позволил бы подобрать токен по
// времени ответа.
func subtleEquals(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
