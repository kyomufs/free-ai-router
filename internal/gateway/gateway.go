// Package gateway — поверхность OpenAI, которую видит клиент.
//
// ЧТО ЗДЕСЬ ГЛАВНОЕ. Шлюз — ПРОЗРАЧНЫЙ ПОСРЕДНИК. Он делает то, что сделал бы
// клиент при прямом обращении, и не добавляет поведения поверх. Отсюда два
// правила, которым подчинён весь пакет:
//
//  1. ОТКАЗ ПЕРЕДАЁТСЯ ДОСЛОВНО. Статус, тело и заголовки уходят клиенту
//     байт в байт. Клиент сам разбирает тело: он ищет там `error.type`,
//     `error.code`, вложенные коды и метки, и принимает по ним решение о
//     повторе. Любая переупаковка лишила бы его этих признаков.
//
//  2. ШЛЮЗ НЕ ПОВТОРЯЕТ ЗАПРОСЫ. У клиента своя политика повторов, подробно
//     разработанная: экспонента с потолком, до десяти раз, с ограничением на
//     таймауты. Вторая политика поверх неё дала бы тридцать запросов там, где
//     клиент рассчитывал на десять. Единственное исключение — смена ключа:
//     ключей у клиента нет, они у шлюза, и со стороны клиента это один запрос.
package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"free-ai-router/internal/capability"
	"free-ai-router/internal/provider"
	"free-ai-router/internal/state"
)

// Пределы. Тело запроса ограничено, потому что клиент может прислать
// что угодно, а память не бесконечна.
const (
	maxBodyBytes = 4 << 20 // 4 MiB
)

// Handler — HTTP-обработчик поверхности OpenAI.
type Handler struct {
	// Providers — реестр доступных провайдеров.
	Providers *provider.Registry
	// Transport отдаёт RoundTripper для запросов.
	Transport RoundTripperFactory
	// State — счётчики.
	State *state.Store
	// Log — журнал.
	Log *state.Logger

	// DefaultProvider — провайдер, используемый когда клиент не назвал его.
	// Пусто означает первого в реестре.
	DefaultProvider provider.ID

	// KeyRotation хранит механизмы ротации ключей по провайдерам. Пусто
	// означает анонимную работу: провайдер подставит свой маркер
	// анонимности, и это нормальный режим, а не отсутствие настройки.
	KeyRotation map[string]*capability.KeyRotation

	// Client — HTTP-клиент. Задаётся полем, а не создаётся на каждый запрос:
	// клиент держит пул соединений, и терять его нельзя.
	Client *http.Client
}

// RoundTripperFactory отдаёт транспорт для запросов.
//
// Интерфейс, а не конкретный тип: шлюзу не нужно знать, сколько транспортов
// существует и как они устроены.
type RoundTripperFactory interface {
	RoundTripper() (http.RoundTripper, error)
}

// Routes регистрирует маршруты на переданном mux.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("/v1/models", h.handleModels)
	mux.HandleFunc("/v1/chat/completions", h.handleChat)
}

// ProviderRoute — префикс для выбора провайдера прямо в пути.
//
// Форма: /p/<провайдер>/v1/chat/completions. Нужна, когда клиент умеет
// адресовать несколько маршрутов: тогда каждый провайдер виден как отдельный
// эндпоинт, и переключаться между ними можно на стороне клиента.
const ProviderRoute = "/p/"

// routes регистрирует оба вида маршрутов.
func (h *Handler) AllRoutes(mux *http.ServeMux) {
	h.Routes(mux)
	mux.HandleFunc(ProviderRoute, h.handleProviderRoute)
}

// resolveClient возвращает HTTP-клиент.
func (h *Handler) resolveClient() *http.Client {
	if h.Client != nil {
		return h.Client
	}
	return http.DefaultClient
}

// resolveProvider выбирает провайдера по запросу.
//
// Порядок: явное имя в теле запроса, затем имя в пути, затем провайдер по
// умолчанию. Явное имя в теле уважается, потому что клиент может отправлять
// `model` вида "провайдер/модель" — это распространённая форма, и её лучше
// понимать, чем отвергать.
func (h *Handler) resolveProvider(body map[string]any, pathID provider.ID) (provider.Provider, string, error) {
	if pathID != "" {
		p, err := h.Providers.Get(pathID)
		if err != nil {
			return nil, "", err
		}
		model, _ := body["model"].(string)
		return p, stripProviderPrefix(string(model), pathID), nil
	}

	rawModel, _ := body["model"].(string)
	if name, rest := splitProviderPrefix(rawModel); name != "" {
		p, err := h.Providers.Get(provider.ID(name))
		if err != nil {
			return nil, "", fmt.Errorf("unknown provider %q in the model field", name)
		}
		return p, rest, nil
	}

	if h.DefaultProvider != "" {
		p, err := h.Providers.Get(h.DefaultProvider)
		if err != nil {
			return nil, "", err
		}
		return p, rawModel, nil
	}

	all := h.Providers.All()
	if len(all) == 0 {
		return nil, "", fmt.Errorf("no providers configured")
	}
	return all[0], rawModel, nil
}

// splitProviderPrefix разбирает форму "провайдер/модель".
//
// Разделитель ищется до первого слэша; имя провайдера не может содержать
// слэш, а идентификаторы моделей часто содержат (например
// "poolside/laguna-s-2.1:free"), поэтому первая часть проверяется по реестру
// вызывающим.
func splitProviderPrefix(model string) (name, rest string) {
	i := strings.Index(model, "/")
	if i <= 0 || i == len(model)-1 {
		return "", ""
	}
	return model[:i], model[i+1:]
}

// stripProviderPrefix убирает префикс провайдера из имени модели.
func stripProviderPrefix(model string, id provider.ID) string {
	prefix := string(id) + "/"
	if strings.HasPrefix(model, prefix) {
		return strings.TrimPrefix(model, prefix)
	}
	return model
}

// handleProviderRoute разбирает путь вида /p/<провайдер>/v1/...
func (h *Handler) handleProviderRoute(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, ProviderRoute)
	slash := strings.Index(rest, "/")
	if slash <= 0 {
		http.NotFound(w, r)
		return
	}
	id := provider.ID(rest[:slash])
	inner := rest[slash:]

	switch inner {
	case "/v1/models":
		h.handleModelsFor(w, r, id)
	case "/v1/chat/completions":
		h.handleChatFor(w, r, id)
	default:
		http.NotFound(w, r)
	}
}

// handleModels отвечает списком моделей всех провайдеров.
func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "не поддерживается",
			"GET /v1/models: метод должен быть GET")
		return
	}
	h.serveModels(w, r, "")
}

// handleModelsFor отвечает списком моделей одного провайдера.
func (h *Handler) handleModelsFor(w http.ResponseWriter, r *http.Request, id provider.ID) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "не поддерживается",
			"GET /v1/models: метод должен быть GET")
		return
	}
	if _, err := h.Providers.Get(id); err != nil {
		writeError(w, http.StatusNotFound, "провайдер не найден", err.Error())
		return
	}
	h.serveModels(w, r, id)
}

// serveModels собирает список моделей.
//
// Формат — список OpenAI. Идентификатор модели составной: "провайдер/модель",
// иначе две модели с одинаковым именем у разных провайдеров были бы
// неразличимы, а клиент не смог бы выбрать нужную.
func (h *Handler) serveModels(w http.ResponseWriter, r *http.Request, only provider.ID) {
	type modelEntry struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}

	var entries []modelEntry
	for _, p := range h.Providers.All() {
		if only != "" && p.ID() != only {
			continue
		}
		models, err := p.Models(r.Context())
		if err != nil {
			h.warn("провайдер %q: каталог недоступен: %v", p.ID(), err)
			continue
		}
		for _, m := range models {
			entries = append(entries, modelEntry{
				ID:      string(p.ID()) + "/" + m.ID,
				Object:  "model",
				Created: modelsCreatedAt,
				OwnedBy: string(p.ID()),
			})
		}
	}

	if entries == nil {
		entries = []modelEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   entries,
	})
}

// modelsCreatedAt — постоянная отметка создания для списка моделей. Список
// статичен, поэтому выдумывать каждый раз новое время было бы нечестно.
const modelsCreatedAt int64 = 1759718400

// handleChat обрабатывает запрос к модели.
func (h *Handler) handleChat(w http.ResponseWriter, r *http.Request) {
	h.handleChatFor(w, r, "")
}

// handleChatFor обрабатывает запрос, при необходимости адресуя его
// конкретному провайдеру.
func (h *Handler) handleChatFor(w http.ResponseWriter, r *http.Request, pathID provider.ID) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "не поддерживается",
			"POST /v1/chat/completions: метод должен быть POST")
		return
	}

	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "неверный запрос", "чтение тела: "+err.Error())
		return
	}

	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		writeError(w, http.StatusBadRequest, "неверный запрос", "разбор тела: "+err.Error())
		return
	}

	p, modelID, err := h.resolveProvider(body, pathID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "провайдер не найден", err.Error())
		return
	}
	if modelID == "" {
		writeError(w, http.StatusBadRequest, "неверный запрос", "поле model обязательно")
		return
	}

	model, err := h.resolveModel(r, p, modelID)
	if err != nil {
		writeError(w, http.StatusNotFound, "модель не найдена", err.Error())
		return
	}

	req, err := h.buildRequest(r, body, p, model)
	if err != nil {
		writeError(w, http.StatusBadRequest, "неверный запрос", err.Error())
		return
	}

	h.forward(w, r, p, req, model)
}

// resolveModel находит модель в каталоге провайдера.
//
// Модель, которой нет в каталоге, отвергается: каталог — это то, что
// провайдер объявил, и отправлять запрос к неизвестной модели значило бы
// получить отказ позже и менее понятный.
func (h *Handler) resolveModel(r *http.Request, p provider.Provider, id string) (provider.Model, error) {
	models, err := p.Models(r.Context())
	if err != nil {
		return provider.Model{}, fmt.Errorf("каталог провайдера недоступен: %w", err)
	}
	for _, m := range models {
		if m.ID == id {
			return m, nil
		}
	}
	return provider.Model{}, fmt.Errorf("провайдер %q не объявляет модель %q", p.ID(), id)
}

// buildRequest переводит клиентский запрос в форму провайдера.
func (h *Handler) buildRequest(r *http.Request, body map[string]any, p provider.Provider, model provider.Model) (provider.Request, error) {
	messages, err := parseMessages(body["messages"])
	if err != nil {
		return provider.Request{}, err
	}
	tools, err := parseTools(body["tools"])
	if err != nil {
		return provider.Request{}, err
	}

	ids := h.sessionIDs(r, body)

	req := provider.Request{
		Model:    model,
		Messages: messages,
		System:   systemFrom(body),
		Tools:    tools,
		// Stream всегда истина: апстрим обязан стримить, потому что на этом
		// держится передача потока клиенту.
		Stream:  true,
		Session: ids,
	}

	if raw, ok := body["tool_choice"].(string); ok {
		req.ToolChoice = raw
	}
	if v, ok := body["max_tokens"].(float64); ok && v > 0 {
		req.MaxTokens = int(v)
	}
	if v, ok := body["temperature"].(float64); ok {
		req.Temperature = &v
	}
	if v, ok := body["reasoning_effort"].(string); ok {
		req.ReasoningEffort = v
	}

	return req, nil
}

// forward отправляет запрос провайдеру и передаёт ответ клиенту.
func (h *Handler) forward(w http.ResponseWriter, r *http.Request, p provider.Provider, req provider.Request, model provider.Model) {
	prepared, err := p.Build(req)
	if err != nil {
		h.warn("провайдер %q: сборка запроса: %v", p.ID(), err)
		writeError(w, http.StatusInternalServerError, "ошибка шлюза", err.Error())
		h.recordRefusal(model.ID, "")
		return
	}

	// Учётные данные подставляет шлюз: у клиента ключей нет.
	key := h.credentialFor(p)
	h.applyCredential(p, prepared, key)

	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, prepared.URL, bytes.NewReader(prepared.Body))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ошибка шлюза", err.Error())
		return
	}
	upstream.Header = prepared.Headers

	resp, err := h.do(upstream, p)
	if err != nil {
		// Апстрим недостижим. Это НЕ отказ провайдера, и выдавать его за
		// отказ нельзя: клиент должен видеть транспортный сбой.
		h.warn("провайдер %q: соединение: %v", p.ID(), err)
		writeError(w, http.StatusBadGateway, "транспорт", "провайдер недоступен: "+err.Error())
		h.recordRefusal(model.ID, h.fingerprint(key))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		h.passThroughError(w, r, p, resp, model, key)
		return
	}

	h.passThroughStream(w, resp)
	h.recordSuccess(model.ID, h.fingerprint(key))
}

// do выполняет запрос через транспорт шлюза.
func (h *Handler) do(req *http.Request, p provider.Provider) (*http.Response, error) {
	client := *h.resolveClient()
	if h.Transport != nil {
		if rt, err := h.Transport.RoundTripper(); err == nil && rt != nil {
			client.Transport = rt
		}
	}
	return client.Do(req)
}

// passThroughError передаёт отказ клиенту ДОСЛОВНО.
//
// Здесь и находится главное правило пакета. Ни статус, ни тело, ни заголовки
// не меняются. Клиент ищет в теле признаки, по которым принимает решение:
// `error.type`, `error.code`, вложенные коды, метку исходного кода в
// переписанном сообщении. Переупаковка лишила бы его этих признаков, и решение
// разошлось бы с тем, что клиент принял бы при прямом обращении.
func (h *Handler) passThroughError(w http.ResponseWriter, r *http.Request, p provider.Provider, resp *http.Response, model provider.Model, key string) {
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if readErr != nil {
		writeError(w, http.StatusBadGateway, "транспорт", "чтение ответа: "+readErr.Error())
		return
	}

	failure := p.Classify(resp, body)

	// Заголовки, влияющие на решение клиента, пробрасываются как есть.
	// retry-after и retry-after-ms клиент читает сам.
	for _, name := range []string{"retry-after", "retry-after-ms", "content-type"} {
		if v := resp.Header.Get(name); v != "" {
			w.Header().Set(name, v)
		}
	}
	if w.Header().Get("content-type") == "" {
		w.Header().Set("content-type", "application/json")
	}

	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)

	h.recordRefusal(model.ID, h.fingerprint(key))
	h.warn("провайдер %q: отказ %d, ключ отвергнут: %t", p.ID(), failure.Status, failure.KeyRejected)
}

// passThroughStream передаёт поток клиенту.
func (h *Handler) passThroughStream(w http.ResponseWriter, resp *http.Response) {
	if ct := resp.Header.Get("content-type"); ct != "" {
		w.Header().Set("content-type", ct)
	} else {
		w.Header().Set("content-type", "text/event-stream")
	}
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(resp.StatusCode)

	flusher, canFlush := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			// Поток отдаётся по мере поступления: задержка до полного
			// ответа сделала бы стриминг бессмысленным.
			if canFlush {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// applyCredential подставляет учётные данные в собранный запрос.
//
// Провайдер собирает заголовки, но НЕ знает, какой ключ выдан на этот запрос:
// ключи выдаёт общая механика. Поэтому заголовок ставится здесь.
func (h *Handler) applyCredential(p provider.Provider, prepared provider.Prepared, key string) {
	if prepared.Headers == nil {
		return
	}
	value := credentialValue(p, key)
	if value == "" {
		return
	}
	// Имя заголовка и префикс — часть контракта провайдера, поэтому берутся
	// у него, а не задаются здесь.
	name, prefix := credentialShape(p)
	prepared.Headers.Set(name, prefix+value)
}

// credentialScheme описывает, куда провайдер ждёт учётные данные.
type credentialScheme interface {
	Credential(key string) string
}

// credentialValue спрашивает у провайдера, что положить в заголовок.
func credentialValue(p provider.Provider, key string) string {
	if cs, ok := p.(credentialScheme); ok {
		return cs.Credential(key)
	}
	if key == "" {
		return "public"
	}
	return key
}

// credentialShape возвращает имя заголовка и префикс значения.
//
// Провайдер, которому это небезразлично, сообщает форму сам; остальные
// получают стандартную.
func credentialShape(p provider.Provider) (name, prefix string) {
	if shaped, ok := p.(interface{ CredentialHeader() (string, string) }); ok {
		return shaped.CredentialHeader()
	}
	return "authorization", "Bearer "
}

// warn пишет предупреждение в журнал, если он есть.
func (h *Handler) warn(format string, args ...any) {
	if h.Log != nil {
		h.Log.Warn(format, args...)
	}
}

// recordSuccess записывает успешный запрос.
func (h *Handler) recordSuccess(model, key string) {
	if h.State != nil {
		h.State.RecordSuccess(model, key)
	}
}

// recordRefusal записывает отказ.
func (h *Handler) recordRefusal(model, key string) {
	if h.State != nil {
		h.State.RecordRefusal(model, key)
	}
}

// writeJSON отвечает телом JSON.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError отвечает конвертом ошибки OpenAI.
//
// Конверт используется ТОЛЬКО для ошибок самого шлюза: неверный запрос,
// недостижимый апстрим, незнакомая модель. Отказы провайдера через него не
// ПРОХОДЯТ — они передаются дословно (см. passThroughError).
//
// type и code несут одну строку: у клиента один словарь для ветвления, и
// второй, отличающийся, заставил бы его держать соответствие между ними.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    code,
			"code":    code,
		},
	})
}

// Timeout подсказывает вызывающему бюджет молчания для запроса.
//
// Провайдер объявляет его сам: рассуждающие модели держат паузы заметно
// дольше обычных, и общий бюджет рвал бы их поток.
func Timeout(prepared provider.Prepared, fallback time.Duration) time.Duration {
	if prepared.IdleTimeout > 0 {
		return prepared.IdleTimeout
	}
	return fallback
}

// fingerprint возвращает отпечаток ключа для счётчиков.
//
// Сам ключ в состояние не попадает никогда: это секрет, а для ответа на
// вопрос «что израсходовано» достаточно короткого отпечатка.
func (h *Handler) fingerprint(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:4])
}
