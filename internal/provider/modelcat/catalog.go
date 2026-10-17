// Package modelcat запрашивает у сервиса список доступных сейчас моделей.
//
// ПОЧЕМУ КАТАЛОГ ИЗ СЕТИ, А НЕ ИЗ КОДА. Список, записанный в коде, неизбежно
// расходится с сервисом: модели выводят из оборота, а двоичный файл остаётся
// прежним. Тогда клиент видит в селекторе то, чего больше нет, выбирает это и
// получает отказ. Отказ по несуществующей модели неотличим от отказа по квоте,
// и разобраться в нём нельзя. Каталог из сети этой рассинхронизации не имеет
// по построению: он физически не может пережить вывод модели из оборота.
//
// РАЗДЕЛЕНИЕ ОТВЕТСТВЕННОСТИ. Пакет знает, КАК спросить список, и не знает,
// что означает конкретная модель. Свойства моделей — пределы, размышления,
// приём изображений — добавляет источник метаданных (MetaSource). Так общий
// механизм остаётся общим: сервис, не имеющий каталога метаданных, просто
// передаёт пустой список источников.
package modelcat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"free-ai-router/internal/provider"
)

const (
	// catalogTimeout — предел ожидания каталога.
	//
	// Каталог запрашивается на пути живого запроса: список моделей и проверка
	// модели перед отправкой. Долгое ожидание здесь задержало бы сам запрос,
	// поэтому предел короткий: каталог либо есть, либо его нет, и ждать
	// дольше нечего.
	catalogTimeout = 8 * time.Second

	// listTTL — срок годности списка.
	//
	// Модели добавляются и исчезают редко, а спрашиваются часто. Без срока
	// годности каждый запрос клиента превращался бы в сетевой запрос к
	// сервису, и шлюз расходовал бы квоту на перечисление того, что и так
	// знает.
	listTTL = 5 * time.Minute

	// metaTTL — срок годности свойств модели. Меняются ещё реже списка.
	metaTTL = 6 * time.Hour

	// maxListBytes — предел чтения тела списка. Список мал, и предел нужен
	// только от неожиданно большого ответа.
	maxListBytes = 4 << 20
)

// MetaSource дополняет описание модели свойствами.
//
// Опрашивается отдельно от списка и может быть недоступен: он уточняет
// описание, а не определяет состав каталога. Ошибка источника не отменяет
// каталог — модель работает и без точных пределов.
type MetaSource interface {
	// Meta возвращает свойства моделей, ключ — идентификатор модели.
	Meta(ctx context.Context) (map[string]provider.Model, error)
	// Name называет источник для журнала.
	Name() string
}

// Catalog — каталог моделей сервиса.
type Catalog struct {
	// baseURL — адрес сервиса.
	baseURL string
	// listPath — путь списка моделей.
	listPath string
	// http — клиент. Общий с остальным шлюзом, чтобы не заводить второй пул
	// соединений к тому же хосту.
	http *http.Client
	// headers накладывает постоянные заголовки провайдера. Те же, что и у
	// запроса к модели: сервис узнаёт клиента по ним, и без них каталог может
	// быть урезан или отдан не тем.
	headers func(http.Header)
	// metas — источники свойств.
	metas []MetaSource

	mu sync.Mutex
	// list — последний список и время его получения.
	list   []string
	listAt time.Time
	// meta — последние свойства, слитые из источников.
	meta   map[string]provider.Model
	metaAt time.Time

	// fetching отделяет выборку от чтения кэша. Держать основную блокировку на
	// время сети значило бы останавливать чтение кэша всем остальным запросам.
	fetching sync.Mutex
}

// New создаёт каталог.
func New(baseURL, listPath string, httpClient *http.Client, headers func(http.Header), metas ...MetaSource) *Catalog {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Catalog{
		baseURL:  strings.TrimRight(baseURL, "/"),
		listPath: listPath,
		http:     httpClient,
		headers:  headers,
		metas:    metas,
	}
}

// Error описывает, почему каталог не получен.
//
// Отдельный тип, а не строка: вызывающий различает случаи — сервис ответил
// отказом, ответ не разобран, запрос не выполнен. В журнале это разные
// причины, и лечатся они по-разному.
type Error struct {
	// Op — что именно запрашивалось.
	Op string
	// Status — код ответа, если он был.
	Status int
	// Reason — краткая причина.
	Reason string
}

func (e *Error) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("каталог %s: %s (HTTP %d)", e.Op, e.Reason, e.Status)
	}
	return fmt.Sprintf("каталог %s: %s", e.Op, e.Reason)
}

// Models возвращает каталог моделей.
//
// Порядок: свежий кэш, затем сеть. Ошибка сети НЕ подменяется прежним списком.
// Подменённый список вернул бы в селектор модели, которых может уже не быть,
// то есть воспроизвёл бы ровно ту рассинхронизацию, ради устранения которой
// каталог берётся из сети. Отсутствие каталога — честное состояние, и клиент
// должен видеть его как отсутствие, а не как список.
func (c *Catalog) Models(ctx context.Context) ([]provider.Model, error) {
	ids, err := c.listIDs(ctx)
	if err != nil {
		return nil, err
	}

	meta := c.metaModels(ctx)

	out := make([]provider.Model, 0, len(ids))
	for _, id := range ids {
		out = append(out, Describe(id, meta[id]))
	}
	return out, nil
}

// listIDs возвращает идентификаторы доступных сейчас моделей.
func (c *Catalog) listIDs(ctx context.Context) ([]string, error) {
	c.mu.Lock()
	if c.list != nil && time.Since(c.listAt) < listTTL {
		ids := c.list
		c.mu.Unlock()
		return ids, nil
	}
	c.mu.Unlock()

	c.fetching.Lock()
	defer c.fetching.Unlock()

	// Пока ждали очередь, список мог обновиться другим запросом.
	c.mu.Lock()
	if c.list != nil && time.Since(c.listAt) < listTTL {
		ids := c.list
		c.mu.Unlock()
		return ids, nil
	}
	c.mu.Unlock()

	ids, err := c.fetchList(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		// Прежний список не сохраняется как ответ: он бы выглядел свежим.
		return nil, err
	}
	c.list, c.listAt = ids, time.Now()
	return ids, nil
}

// fetchList запрашивает список моделей.
func (c *Catalog) fetchList(ctx context.Context) ([]string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, catalogTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.baseURL+c.listPath, nil)
	if err != nil {
		return nil, &Error{Op: "список моделей", Reason: err.Error()}
	}
	if c.headers != nil {
		c.headers(req.Header)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &Error{Op: "список моделей", Reason: "запрос не выполнен: " + err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Тело читается и попадает в причину: сервис объясняет отказ в нём, и
		// без тела причина была бы только в коде ответа.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, &Error{
			Op:     "список моделей",
			Status: resp.StatusCode,
			Reason: strings.TrimSpace(string(body)),
		}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxListBytes))
	if err != nil {
		return nil, &Error{Op: "список моделей", Reason: "чтение ответа: " + err.Error()}
	}

	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, &Error{Op: "список моделей", Reason: "ответ не разобран: " + err.Error()}
	}

	ids := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 {
		// Пустой список означает, что разбор разошёлся с формой ответа. Молча
		// вернуть пустой каталог значило бы показать оператору пустой селектор
		// без объяснения.
		return nil, &Error{Op: "список моделей", Reason: "сервис не объявил ни одной модели"}
	}
	return ids, nil
}

// metaModels собирает свойства моделей из источников.
//
// Источники опрашиваются по очереди, каждый со своим сроком годности. Ошибка
// любого из них не прерывает сбор: свойства — уточнение, и его отсутствие
// означает лишь более скромное описание модели.
func (c *Catalog) metaModels(ctx context.Context) map[string]provider.Model {
	c.mu.Lock()
	if c.meta != nil && time.Since(c.metaAt) < metaTTL {
		m := c.meta
		c.mu.Unlock()
		return m
	}
	c.mu.Unlock()

	c.fetching.Lock()
	defer c.fetching.Unlock()

	c.mu.Lock()
	if c.meta != nil && time.Since(c.metaAt) < metaTTL {
		m := c.meta
		c.mu.Unlock()
		return m
	}
	c.mu.Unlock()

	merged := make(map[string]provider.Model)
	got := false
	for _, src := range c.metas {
		part, err := src.Meta(ctx)
		if err != nil {
			// Источник не ответил: его свойства просто не участвуют.
			// Остальные источники опрашиваются дальше.
			continue
		}
		got = true
		for id, m := range part {
			merged[id] = m
		}
	}
	if !got {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.meta, c.metaAt = merged, time.Now()
	return merged
}

// Умолчания для модели без свойств.
//
// Значения занижены намеренно. Заниженный предел приводит к раннему усечению
// беседы, завышенный — к отказу сервиса на середине запроса. Первое заметно и
// поправимо, второе теряет уже сделанную работу.
const (
	DefaultContext        = 200000
	DefaultMaxOutput      = 32000
	DefaultReasoningField = "reasoning_content"
)

// Describe собирает описание модели из идентификатора и известных свойств.
//
// Свойств может не быть: сервис объявляет модель раньше, чем каталог
// метаданных о ней узнаёт. Такая модель ВСЁ РАВНО ОПИСЫВАЕТСЯ — с
// умолчаниями. Отвергать её значило бы показать оператору, что модель есть у
// сервиса, но выбрать её нельзя.
func Describe(id string, meta provider.Model) provider.Model {
	m := provider.Model{
		ID:             id,
		Name:           id,
		Context:        DefaultContext,
		MaxOutput:      DefaultMaxOutput,
		ReasoningField: DefaultReasoningField,
		Free:           true,
	}
	if meta.Name != "" {
		m.Name = meta.Name
	}
	if meta.Description != "" {
		m.Description = meta.Description
	}
	if meta.Context > 0 {
		m.Context = meta.Context
	}
	if meta.MaxOutput > 0 {
		m.MaxOutput = meta.MaxOutput
	}
	m.Vision = meta.Vision
	m.Reasoning = meta.Reasoning
	m.ReasoningOptions = meta.ReasoningOptions
	if meta.ReasoningField != "" {
		m.ReasoningField = meta.ReasoningField
	}
	return m
}
