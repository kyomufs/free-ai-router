package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"free-ai-router/internal/capability"
	"free-ai-router/internal/config"
	"free-ai-router/internal/gateway"
	"free-ai-router/internal/provider"
)

// keyRegistry хранит механизмы ротации ключей по провайдерам.
//
// ОТДЕЛЬНЫЙ МЕХАНИЗМ НА ПРОВАЙДЕРА, а не один общий. Реализация одинакова, но
// состояние раздельное: смешав ключи, шлюз отправлял бы ключ одного сервиса в
// запросы к другому, и счётчики расхода приписывались бы не тому.
//
// Механизм подключается только там, где ключи вообще есть. Провайдер без ключей
// работает анонимно, и это НОРМАЛЬНЫЙ режим, а не отсутствие настройки: сервисы
// с бесплатным тиром именно так и устроены.
type keyRegistry struct {
	byProvider map[provider.ID]*capability.KeyRotation
	order      []provider.ID
}

// newKeyRegistry собирает механизмы из конфигурации.
//
// Ключи провайдера берутся из его секции: сначала значение в файле, затем
// переменная, названная рядом. Отпечатки для справки считаются здесь же, чтобы
// сам ключ дальше по коду не ходил.
func newKeyRegistry(cfg *config.Config, providers *provider.Registry) *keyRegistry {
	r := &keyRegistry{byProvider: make(map[provider.ID]*capability.KeyRotation)}

	for _, p := range providers.All() {
		id := p.ID()
		pc, ok := cfg.Providers[string(id)]
		if !ok {
			continue
		}
		r.order = append(r.order, id)

		keys := keysFromConfig(pc)
		if len(keys) == 0 {
			// Ключей нет: механизм не создаётся, и провайдер пойдёт
			// анонимно.
			continue
		}

		built, err := capability.NewKeyRotation(map[string]any{"keys": keys})
		if err != nil {
			continue // форма настроек уже проверена каталогом
		}
		kr, ok := built.(*capability.KeyRotation)
		if !ok {
			continue
		}
		r.byProvider[id] = kr
	}
	return r
}

// keysFromConfig извлекает список ключей провайдера.
func keysFromConfig(pc config.ProviderConfig) []any {
	// Основной источник — поля секции. Список может быть и в настройках:
	// так удобнее, когда ключей много и они лежат отдельно.
	var raw []any
	if k := pc.ProviderKey(); k != "" {
		raw = append(raw, k)
	}
	if extra, ok := pc.Settings["keys"].([]any); ok {
		raw = append(raw, extra...)
	}
	if extra, ok := pc.Settings["keys"].([]string); ok {
		for _, k := range extra {
			raw = append(raw, k)
		}
	}
	return raw
}

// rotationMap отдаёт карту для поверхности запросов.
func (r *keyRegistry) rotationMap() map[string]*capability.KeyRotation {
	out := make(map[string]*capability.KeyRotation, len(r.byProvider))
	for id, kr := range r.byProvider {
		out[string(id)] = kr
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// firstProvider возвращает провайдера по умолчанию.
//
// Первый в порядке конфигурации: когда клиент не назвал провайдера, разумнее
// взять предсказуемый, чем случайный.
func (r *keyRegistry) firstProvider() provider.ID {
	if len(r.order) == 0 {
		return ""
	}
	return r.order[0]
}

// Keys реализует gateway.KeyRotationRegistry.
func (r *keyRegistry) Keys(id provider.ID) []string {
	kr, ok := r.byProvider[id]
	if !ok {
		return nil
	}
	return kr.KeysSnapshot()
}

// KeyFingerprints реализует gateway.KeyRotationRegistry.
//
// Отдаются ОТПЕЧАТКИ, а не ключи: справка нужна, чтобы понять, сколько ключей
// в работе, а не чтобы увидеть секрет. Отпечаток короткий — его достаточно для
// различения и недостаточно для восстановления.
func (r *keyRegistry) KeyFingerprints(id provider.ID) []string {
	kr, ok := r.byProvider[id]
	if !ok {
		return nil
	}
	keys := kr.KeysSnapshot()
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, fingerprint(k))
	}
	return out
}

// HasRotation реализует gateway.KeyRotationRegistry.
func (r *keyRegistry) HasRotation(id provider.ID) bool {
	kr, ok := r.byProvider[id]
	return ok && kr.Len() > 1
}

// AddKey реализует gateway.KeyRotationRegistry.
func (r *keyRegistry) AddKey(id provider.ID, key string) bool {
	kr, ok := r.byProvider[id]
	if !ok {
		return false
	}
	return kr.Add(key)
}

// RemoveFingerprint реализует gateway.KeyRotationRegistry.
//
// Удаление идёт по отпечатку: удаляющий не обязан знать секрет — ему
// достаточно того, что показала справка.
func (r *keyRegistry) RemoveFingerprint(id provider.ID, fp string) bool {
	kr, ok := r.byProvider[id]
	if !ok {
		return false
	}
	for _, k := range kr.KeysSnapshot() {
		if fingerprint(k) == fp {
			return kr.Remove(k)
		}
	}
	return false
}

// fingerprint возвращает короткий отпечаток ключа.
//
// Длина выбрана так, чтобы случайное совпадение было невероятным, а значение
// оставалось удобным для глаза и для копирования.
func fingerprint(key string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return hex.EncodeToString(sum[:4])
}

// Проверка на этапе сборки: реестр обязан удовлетворять интерфейсу, которого
// ждёт поверхность управления.
var _ gateway.KeyRotationRegistry = (*keyRegistry)(nil)
