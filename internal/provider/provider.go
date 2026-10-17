// Package provider описывает, что такое «провайдер бесплатных моделей» и как
// шлюз с ним разговаривает.
//
// РАЗДЕЛЕНИЕ ОТВЕТСТВЕННОСТИ — главное в этом пакете. Провайдер НЕ описывает
// свою политику повторов, ротацию ключей или транспорт. Он отвечает ровно на
// три вопроса:
//
//  1. Как называются его модели и что они умеют?     → Models()
//  2. Как собрать запрос к нему из общего описания?   → Build()
//  3. Как понять, что он ответил отказом?            → Classify()
//
// Всё остальное — общая механика шлюза, и она одинакова для всех провайдеров:
//
//	ротация ключей      internal/capability/keyrotation
//	отказ и его передача internal/gateway
//	транспорт           internal/transport
//
// Такое разделение нужно потому, что МЕХАНИЗМЫ ОБХОДА часто совпадают у
// непохожих провайдеров. Ротация ключей работает одинаково для любого
// провайдера, выдающего ключи. Подмена формы запроса нужна всем, кто фильтрует
// клиентов. Проксирование трафика через чужой сервер вообще не зависит от
// провайдера. Если бы каждый провайдер нёс свою копию этого кода, похожие
// провайдеры требовали бы его дублировать, а исправление расходилось бы по
// копиям.
//
// Обратное тоже верно: то, что специфично, обязано остаться у провайдера.
// Набор обязательных инструментов, вид системного промпта, форма заголовков
// сессии — свойства конкретной проверки, а не общей природы вещей.
package provider

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// ID identifies one provider. It appears in configuration, in the control API,
// in the CLI, and as the DSH route name, so it must be stable and lowercase:
// changing it renames a user-visible route.
type ID string

// Model — одна модель, которую провайдер предлагает.
//
// Поля описывают модель, а не способ её вызова: как её зовут, сколько она
// вмещает и что умеет. Способ вызова решает Build().
type Model struct {
	// ID — идентификатор в терминах провайдера, уходит в поле "model".
	ID string
	// Name — отображаемое имя.
	Name string
	// Description — краткое описание для интерфейсов.
	Description string
	// Context — размер окна в токенах.
	Context int
	// MaxOutput — потолок ответа в токенах.
	MaxOutput int
	// Vision — принимает ли модель изображения.
	Vision bool
	// Reasoning — умеет ли модель рассуждать.
	Reasoning bool
	// ReasoningOptions — объявленная лестница усилий. Пусто означает, что
	// модель либо не рассуждает, либо принимает свободную форму.
	ReasoningOptions []ReasoningOption
	// ReasoningField — поле, в которое ответ несёт размышления
	// (например "reasoning_content"). Пусто — размышления не отделяются.
	ReasoningField string
	// Free — объявлена ли модель бесплатной. Используется только для
	// фильтрации в списках и никогда не влияет на поведение запроса.
	Free bool
}

// ReasoningOption — одна форма управления усилием рассуждения.
//
// Формы взяты из каталогов моделей и повторяются у разных провайдеров, поэтому
// описаны типами, а не строкой.
type ReasoningOption struct {
	// Kind — toggle, effort или budget_tokens.
	Kind ReasoningOptionKind
	// Values — допустимые значения для Kind == ReasoningEffort.
	Values []string
	// Max — потолок для Kind == ReasoningBudget.
	Max int
}

// ReasoningOptionKind различает формы управления усилием.
type ReasoningOptionKind string

const (
	// ReasoningToggle — рассуждение включается и выключается.
	ReasoningToggle ReasoningOptionKind = "toggle"
	// ReasoningEffort — усилие задаётся словом из Values.
	ReasoningEffort ReasoningOptionKind = "effort"
	// ReasoningBudget — усилие задаётся числом токенов.
	ReasoningBudget ReasoningOptionKind = "budget_tokens"
)

// Request — общее описание запроса, которое шлюз передаёт провайдеру.
//
// Шлюз собирает эту структуру из запроса клиента и метаданных модели; провайдер
// превращает её в байты тела и набор заголовков. Провайдер видит ровно то, что
// ему нужно, и не знает ничего о форме клиентского запроса.
type Request struct {
	// Model — разрешённая модель (не исходная строка клиента).
	Model Model
	// Messages — сообщения в нейтральной форме.
	Messages []Message
	// System — системная часть, уже собранная шлюзом.
	System string
	// Tools — инструменты, которые клиент объявил.
	Tools []Tool
	// ToolChoice — требование к выбору инструмента. Пусто означает «поле не
	// отправлять»: клиент opencode именно так и делает, когда инструментов нет.
	ToolChoice string
	// MaxTokens — резерв на ответ.
	MaxTokens int
	// Temperature — температура, если клиент её задал.
	Temperature *float64
	// ReasoningEffort — затребованное усилие после клампинга.
	ReasoningEffort string
	// Stream — всегда истина: шлюз работает потоком.
	Stream bool
	// Session — идентификаторы сессии, которые провайдер может положить в
	// заголовки. Провайдер решает, нужны ли они ему.
	Session SessionIDs
}

// Message — одно сообщение в нейтральной форме.
//
// Нейтральная форма нужна, чтобы провайдер не разбирал клиентский запрос: шлюз
// приводит его к этому виду один раз, и каждый провайдер переводит его в свой.
type Message struct {
	// Role — system, user, assistant или tool.
	Role string
	// Text — текстовая часть.
	Text string
	// Reasoning — размышления ассистента, если есть.
	Reasoning string
	// ToolCalls — вызовы инструментов, сделанные ассистентом.
	ToolCalls []ToolCall
	// ToolCallID — идентификатор вызова, на который отвечает сообщение роли tool.
	ToolCallID string
	// Images — изображения в сообщении.
	Images []Image
}

// ToolCall — вызов инструмента ассистентом.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// Image — вложение изображения.
type Image struct {
	MediaType string
	Data      []byte
}

// Tool — объявление инструмента.
type Tool struct {
	Name        string
	Description string
	// Schema — JSON-схема параметров, уже сериализованная.
	Schema []byte
}

// SessionIDs — идентификаторы, выведенные шлюзом по контракту клиента.
//
// Провайдер волен их игнорировать; те, кому они нужны, кладут их в свои
// заголовки.
type SessionIDs struct {
	// Session — идентификатор этой сессии.
	Session string
	// Affinity — идентификатор корня группы для маршрутизации кеша.
	Affinity string
	// Project — идентификатор проекта.
	Project string
	// Message — идентификатор обрабатываемого сообщения.
	Message string
	// Parent — идентификатор родительской сессии, пусто для плоской.
	Parent string
}

// Prepared — результат сборки запроса.
//
// Провайдер возвращает то, что нужно отправить, и больше ничего. Транспорт,
// повтор и классификация отказа — не его дело.
type Prepared struct {
	// URL — полный адрес, по которому отправлять запрос.
	URL string
	// Headers — заголовки запроса целиком. Шлюз их не дополняет: набор
	// заголовков и есть часть контракта провайдера.
	Headers http.Header
	// Body — сериализованное тело.
	Body []byte
	// IdleTimeout — бюджет молчания потока для этого запроса. Ноль означает
	// общий бюджет шлюза.
	IdleTimeout time.Duration
	// FirstEventTimeout — бюджет до первых заголовков. Ноль означает общий.
	FirstEventTimeout time.Duration
}

// Failure — классифицированный отказ провайдера.
//
// Отказ описывается МИНИМАЛЬНО: что ответил провайдер и повторяемо ли это.
// Как поступить — решает общая механика (ротация ключей) и, в конечном счёте,
// клиент.
type Failure struct {
	// Status — код, который провайдер вернул.
	Status int
	// Body — тело ответа ДОСЛОВНО. Шлюз передаёт его клиенту без изменений,
	// потому что клиент сам разбирает его поля.
	Body []byte
	// Headers — заголовки ответа. Нужны целиком: retry-after и подобные
	// влияют на решение клиента.
	Headers http.Header
	// RetryAfter — разобранный retry-after, если он был. Отдельное поле
	// нужно ротации ключей: она решает по нему, ждать или сменить ключ.
	RetryAfter time.Duration
	// KeyRejected — признак того, что отказ вызван именно учётными данными.
	//
	// Это единственное суждение, которое провайдер делает об отказе, и оно
	// существует ради ротации ключей: сменить ключ осмысленно только тогда,
	// когда отвергнут ключ.
	KeyRejected bool
}

// Provider — то, что реализует конкретный сервис бесплатных моделей.
type Provider interface {
	// ID возвращает стабильный идентификатор.
	ID() ID
	// Models возвращает каталог. Вызывается при старте и по запросу; сеть не
	// предполагается, но и не запрещена — провайдер вправе закешировать
	// результат.
	Models(ctx context.Context) ([]Model, error)
	// Build собирает запрос к провайдеру.
	Build(req Request) (Prepared, error)
	// Classify разбирает отказ провайдера.
	//
	// Вызывается только для статусов вне 2xx. Провайдер вправе не распознать
	// отказ: тогда он возвращает Failure с KeyRejected == false, и шлюз
	// просто передаст ответ клиенту.
	Classify(resp *http.Response, body []byte) Failure
}

// Registry — набор доступных провайдеров, собранный из конфигурации.
type Registry struct {
	providers map[ID]Provider
	order     []ID
}

// NewRegistry создаёт пустой реестр.
func NewRegistry() *Registry {
	return &Registry{providers: make(map[ID]Provider)}
}

// Register добавляет провайдера. Повторная регистрация того же ID заменяет
// прежнего и сохраняет позицию в порядке — так горячая перезагрузка
// конфигурации не меняет порядок в списках.
func (r *Registry) Register(p Provider) {
	id := p.ID()
	if _, exists := r.providers[id]; !exists {
		r.order = append(r.order, id)
		sort.SliceStable(r.order, func(i, j int) bool { return r.order[i] < r.order[j] })
	}
	r.providers[id] = p
}

// Get возвращает провайдера по идентификатору.
func (r *Registry) Get(id ID) (Provider, error) {
	p, ok := r.providers[id]
	if !ok {
		return nil, fmt.Errorf("provider: %q is not configured", id)
	}
	return p, nil
}

// IDs возвращает идентификаторы в стабильном порядке.
func (r *Registry) IDs() []ID {
	out := make([]ID, len(r.order))
	copy(out, r.order)
	return out
}

// All возвращает провайдеров в стабильном порядке.
func (r *Registry) All() []Provider {
	out := make([]Provider, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.providers[id])
	}
	return out
}

// Len reports how many providers are registered.
func (r *Registry) Len() int { return len(r.providers) }
