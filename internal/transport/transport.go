// Package transport описывает, КУДА уходит трафик к провайдеру.
//
// ЗАЧЕМ ОТДЕЛЬНАЯ ОСЬ. Транспорт не зависит ни от того, какой это сервис, ни
// от того, как он защищается. Проксирование через чужой сервер работает
// одинаково для всех провайдеров: меняется только адрес, через который
// выходит запрос. Поэтому транспорт вынесен из провайдера — иначе каждый
// провайдер нёс бы копию одного и того же механизма.
//
// Что транспорт делает НЕ:
//   - не знает о моделях, ключах и заголовках провайдера;
//   - не решает, повторять ли запрос;
//   - не разбирает ответ.
//
// Он отвечает ровно на один вопрос: как получить http.RoundTripper, через
// который пойдёт запрос.
package transport

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
)

// ID identifies one transport implementation. It appears in configuration and
// in the CLI, so it must be stable.
type ID string

const (
	// Direct — прямая маршрутизация хоста. Единственный транспорт, который
	// существует сегодня.
	Direct ID = "direct"
)

// Transport — то, что реализует конкретный способ выхода в сеть.
//
// Интерфейс намеренно мал: одна фабрика транспортов и жизненный цикл. Всё,
// что делает выход сложным (процессы-помощники, ротация адресов, прогрев
// соединений), остаётся внутри реализации.
type Transport interface {
	// ID возвращает стабильный идентификатор.
	ID() ID
	// RoundTripper возвращает транспорт для одного запроса.
	//
	// Возвращается на каждый запрос, а не хранится: у реализации может быть
	// несколько полос, и выбор полосы — её внутреннее дело.
	RoundTripper() (http.RoundTripper, error)
	// Start поднимает всё, что нужно транспорту для работы. Вызывается один
	// раз при старте демона.
	Start(ctx context.Context) error
	// Stop освобождает ресурсы.
	Stop() error
	// Info описывает транспорт для интерфейсов.
	Info() Info
}

// Info описывает транспорт для справки и интерфейсов.
type Info struct {
	// ID — идентификатор.
	ID ID
	// Summary — одна строка о том, что транспорт делает.
	Summary string
	// NeedsExternalProcess — поднимает ли транспорт сторонний процесс.
	//
	// Это поле существует, чтобы интерфейсы и установочные проверки могли
	// предупредить оператора: транспорту, которому нужен внешний бинарь,
	// требуется не только конфигурация.
	NeedsExternalProcess bool
}

// Factory создаёт транспорт из конфигурации.
type Factory func(cfg map[string]any) (Transport, error)

// Registry хранит доступные транспорты.
type Registry struct {
	mu        sync.RWMutex
	factories map[ID]Factory
	infos     map[ID]Info
}

// NewRegistry создаёт пустой реестр.
func NewRegistry() *Registry {
	return &Registry{factories: make(map[ID]Factory), infos: make(map[ID]Info)}
}

// Register добавляет транспорт.
func (r *Registry) Register(info Info, factory Factory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[info.ID] = factory
	r.infos[info.ID] = info
}

// Build создаёт транспорт по идентификатору.
func (r *Registry) Build(id ID, cfg map[string]any) (Transport, error) {
	r.mu.RLock()
	factory, ok := r.factories[id]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("transport: %q is not registered", id)
	}
	if factory == nil {
		return nil, fmt.Errorf("transport: %q has no factory", id)
	}
	return factory(cfg)
}

// Infos возвращает описания в стабильном порядке.
func (r *Registry) Infos() []Info {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Info, 0, len(r.infos))
	for _, info := range r.infos {
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// IDs возвращает идентификаторы в стабильном порядке.
func (r *Registry) IDs() []ID {
	infos := r.Infos()
	out := make([]ID, 0, len(infos))
	for _, info := range infos {
		out = append(out, info.ID)
	}
	return out
}
