// Package capability содержит переиспользуемый инструментарий обхода.
//
// ЗАЧЕМ ОТДЕЛЬНЫЙ ПАКЕТ. Разные провайдеры бесплатных моделей часто
// защищаются похожими способами, и один приём помогает нескольким сразу.
// Ротация ключей работает у любого, кто выдаёт ключи. Подмена формы запроса
// нужна каждому, кто фильтрует клиентов по виду запроса. Кеш ответов полезен
// там, где провайдер считает запросы.
//
// Если бы каждый провайдер нёс свою копию такого кода, получилось бы две
// беды: похожие провайдеры заводили бы одинаковые копии, а исправление,
// найденное на одном, не доходило бы до остальных. Поэтому механизмы живут
// здесь, а провайдер ССЫЛАЕТСЯ на нужные ему — по имени, из конфигурации.
//
// Пакет намеренно не зависит ни от provider, ни от gateway: он ничего не знает
// ни о запросах, ни о моделях. Каждый механизм — маленькая функция над общим
// типом данных, которую можно собрать в цепочку.
package capability

import (
	"fmt"
	"sort"
	"sync"
)

// Kind различает виды механизмов, чтобы их можно было выбирать по имени из
// конфигурации, не заводя отдельный тип на каждый.
type Kind string

const (
	// KindKeyRotation — замена учётных данных при отказе по ключу.
	KindKeyRotation Kind = "key_rotation"
	// KindDisguise — подмена формы запроса под вид другого клиента.
	KindDisguise Kind = "disguise"
	// KindRetry — повтор запроса по своей политике.
	KindRetry Kind = "retry"
)

// Info описывает механизм для интерфейсов и справки.
//
// Описание нужно, чтобы `free-ai-router capabilities` и TUI могли показать, что
// вообще есть в системе, не зная ни одной реализации.
type Info struct {
	// Kind — вид механизма.
	Kind Kind
	// Name — имя, которым механизм выбирается в конфигурации.
	Name string
	// Summary — одна строка о том, что механизм делает.
	Summary string
	// Providers — провайдеры, для которых механизм предназначен. Пусто
	// означает «годится для любого».
	Providers []string
}

// Factory создаёт экземпляр механизма.
//
// Механизм получает настройки как map[string]any: у разных механизмов разные
// параметры, и общий тип описывал бы только пересечение. Разбор значений —
// дело самого механизма, он же и сообщает об ошибке.
type Factory func(cfg map[string]any) (any, error)

// Registry хранит доступные механизмы.
//
// Реестр существует, чтобы провайдер не создавал механизмы сам: он лишь
// объявляет, какие ему нужны, и получает готовые. Так один и тот же механизм
// используется несколькими провайдерами в одном процессе.
type Registry struct {
	mu        sync.RWMutex
	factories map[Kind]map[string]Factory
	infos     map[Kind]map[string]Info
}

// NewRegistry создаёт пустой реестр.
func NewRegistry() *Registry {
	return &Registry{
		factories: make(map[Kind]map[string]Factory),
		infos:     make(map[Kind]map[string]Info),
	}
}

// Register добавляет механизм.
func (r *Registry) Register(info Info, factory Factory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.factories[info.Kind] == nil {
		r.factories[info.Kind] = make(map[string]Factory)
		r.infos[info.Kind] = make(map[string]Info)
	}
	r.factories[info.Kind][info.Name] = factory
	r.infos[info.Kind][info.Name] = info
}

// Build создаёт механизм по виду и имени.
func (r *Registry) Build(kind Kind, name string, cfg map[string]any) (any, error) {
	r.mu.RLock()
	factory, ok := r.factories[kind][name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("capability: %s %q is not registered", kind, name)
	}
	return factory(cfg)
}

// Infos возвращает описания механизмов одного вида в стабильном порядке.
func (r *Registry) Infos(kind Kind) []Info {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Info, 0, len(r.infos[kind]))
	for _, info := range r.infos[kind] {
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Names возвращает имена механизмов одного вида.
func (r *Registry) Names(kind Kind) []string {
	infos := r.Infos(kind)
	out := make([]string, 0, len(infos))
	for _, info := range infos {
		out = append(out, info.Name)
	}
	return out
}
