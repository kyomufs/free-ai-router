package capability

import (
	"fmt"
	"sync"
)

// KeyRotation — механизм замены учётных данных при отказе по ключу.
//
// ОБЛАСТЬ ПРИМЕНЕНИЯ. Механизм не знает, какому провайдеру он служит и как
// называются его модели. Он отвечает ровно за одно: выдать следующую строку
// учётных данных и сказать, остались ли ещё варианты. Поэтому он годится для
// любого провайдера, работающего по ключам, и не требует от провайдера ничего,
// кроме признака «отвергнут именно ключ».
//
// ПОЧЕМУ НЕ В ПРОВАЙДЕРЕ. Ротация — не свойство сервиса, а способ обойти
// ограничение на количество запросов с одного ключа. Сервисы разные, способ
// один. Держать его в провайдере значило бы копировать одинаковый код туда,
// где сервисы просто оказались похожи.
type KeyRotation struct {
	mu sync.Mutex

	// keys — доступные ключи. Пустой список означает «ключа нет», и тогда
	// механизм честно сообщает, что менять нечего.
	keys []string

	// next — индекс следующего ключа. Механизм выдаёт ключи по кругу, чтобы
	// расход распределялся равномерно и ни один не сгорал первым.
	next int

	// lastUsed — индекс последнего выданного ключа. Нужен, чтобы не выдать
	// тот же ключ повторно.
	lastUsed int
}

// KeyRotationConfig — настройки механизма, разобранные из конфигурации.
type KeyRotationConfig struct {
	// Keys — список ключей.
	Keys []string
	// StartAt — с какого ключа начинать. Отрицательное значение означает
	// «первый».
	StartAt int
}

// NewKeyRotation создаёт механизм из конфигурации.
//
// Пустой список ключей не является ошибкой: провайдер может работать без них,
// и тогда механизм просто всегда отвечает «менять нечего». Ошибкой был бы
// отказ зарегистрировать механизм из-за отсутствия ключей — это лишило бы
// оператора возможности добавить их позже.
func NewKeyRotation(cfg map[string]any) (any, error) {
	var parsed KeyRotationConfig

	raw, ok := cfg["keys"]
	if ok {
		switch v := raw.(type) {
		case []string:
			parsed.Keys = append(parsed.Keys, v...)
		case []any:
			for i, item := range v {
				s, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("key_rotation: keys[%d] is %T, want a string", i, item)
				}
				parsed.Keys = append(parsed.Keys, s)
			}
		default:
			return nil, fmt.Errorf("key_rotation: keys is %T, want a list of strings", raw)
		}
	}

	if raw, ok := cfg["startAt"]; ok {
		switch v := raw.(type) {
		case int:
			parsed.StartAt = v
		case float64:
			parsed.StartAt = int(v)
		default:
			return nil, fmt.Errorf("key_rotation: startAt is %T, want a number", raw)
		}
	}

	start := parsed.StartAt
	if start < 0 || start >= len(parsed.Keys) {
		start = 0
	}

	// Отбрасываем пустые ключи: пустая строка в списке — почти всегда след
	// лишней запятой в конфигурации, и оставлять её значило бы выдавать
	// заведомо отвергаемый ключ.
	keys := make([]string, 0, len(parsed.Keys))
	for _, k := range parsed.Keys {
		if k != "" {
			keys = append(keys, k)
		}
	}

	return &KeyRotation{keys: keys, next: start, lastUsed: -1}, nil
}

// Len сообщает, сколько ключей доступно.
func (k *KeyRotation) Len() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.keys)
}

// HasAlternatives сообщает, есть ли ключ, отличный от текущего.
//
// False означает, что повтор смысла не имеет: либо ключ один, либо их нет
// вовсе. Это тот случай, когда шлюз обязан отдать отказ клиенту, а не тратить
// время на заведомо бесплодную попытку.
func (k *KeyRotation) HasAlternatives() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.keys) > 1
}

// Next выдаёт следующий ключ и признак того, что ключ есть.
//
// Ключи выдаются по кругу. Возвращённый ключ запоминается как использованный,
// чтобы следующий вызов дал другой: при повторе нужен именно другой ключ, а не
// тот же самый.
func (k *KeyRotation) Next() (string, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.keys) == 0 {
		return "", false
	}

	if len(k.keys) == 1 {
		key := k.keys[0]
		k.lastUsed = 0
		return key, true
	}

	// Пропускаем ключ, выданный в прошлый раз: повтор должен идти с другим.
	for i := 0; i < len(k.keys); i++ {
		idx := k.next % len(k.keys)
		k.next++
		if idx == k.lastUsed {
			continue
		}
		k.lastUsed = idx
		return k.keys[idx], true
	}

	// Достижимо только при одном ключе, что разобрано выше.
	return "", false
}

// Take возвращает ключ, отличный от указанного.
//
// Используется при повторе: текущий ключ известен, и нужен именно другой.
// Если другого нет, второй результат false — повтор бессмыслен.
func (k *KeyRotation) Take(current string) (string, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.keys) <= 1 {
		return "", false
	}
	for i := 0; i < len(k.keys); i++ {
		idx := k.next % len(k.keys)
		k.next++
		if k.keys[idx] == current {
			continue
		}
		k.lastUsed = idx
		return k.keys[idx], true
	}
	return "", false
}

// Add добавляет ключ, если его ещё нет.
//
// Возвращает false, если ключ уже был: повторное добавление не должно менять ни
// порядок обхода, ни счётчики.
func (k *KeyRotation) Add(key string) bool {
	if key == "" {
		return false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, existing := range k.keys {
		if existing == key {
			return false
		}
	}
	k.keys = append(k.keys, key)
	return true
}

// Remove удаляет ключ.
func (k *KeyRotation) Remove(key string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	for i, existing := range k.keys {
		if existing != key {
			continue
		}
		k.keys = append(k.keys[:i], k.keys[i+1:]...)
		if k.lastUsed >= len(k.keys) {
			k.lastUsed = -1
		}
		if k.next > len(k.keys) {
			k.next = 0
		}
		return true
	}
	return false
}

// KeysSnapshot возвращает копию списка ключей.
//
// Именно копию, а не срез: вызывающий не должен получить доступ к внутреннему
// состоянию, иначе блокировка перестанет что-либо защищать.
func (k *KeyRotation) KeysSnapshot() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]string, len(k.keys))
	copy(out, k.keys)
	return out
}

// Info описывает механизм для справки и интерфейсов.
func (k *KeyRotation) Info() Info {
	return Info{
		Kind:    KindKeyRotation,
		Name:    "pool",
		Summary: "меняет ключ из списка, когда провайдер отверг текущий",
	}
}
