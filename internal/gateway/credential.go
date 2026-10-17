package gateway

import (
	"free-ai-router/internal/capability"
	"free-ai-router/internal/provider"
)

// Выдача учётных данных.
//
// ЗАЧЕМ ЭТО ЗДЕСЬ, А НЕ В ПРОВАЙДЕРЕ. Провайдер собирает ЗАГОЛОВКИ, но не
// решает, какой ключ положить в них: ключи выдаёт общая механика ротации, и
// она одна на все провайдеры, работающие по ключам. Провайдеру остаётся форма
// — имя заголовка и префикс.
//
// Ротация подключается полем KeyRotation. Если механизма нет, шлюз работает
// анонимно: провайдер получает пустой ключ и подставляет свой маркер
// анонимности. Это нормальный режим, а не отсутствие настройки.

// CredentialSource выдаёт учётные данные для запроса.
type CredentialSource interface {
	// Next выдаёт следующий ключ.
	Next() (string, bool)
}

// credentialFor возвращает ключ для запроса.
//
// Если механизм ротации подключён, ключ берётся из него. Иначе возвращается
// пустая строка: провайдер подставит маркер анонимности сам, и это правильное
// поведение для развёртывания, не просившего тратить ключи.
func (h *Handler) credentialFor(p provider.Provider) string {
	if h.KeyRotation == nil {
		return ""
	}
	name := string(p.ID())
	kr, ok := h.KeyRotation[name]
	if !ok || kr == nil {
		return ""
	}
	key, _ := kr.Next()
	return key
}

// alternativeKey ищет другой ключ того же провайдера.
//
// Нужна при повторе: текущий ключ отвергнут, и нужен именно другой. Если
// другого нет, повтор бессмыслен, и вызывающий обязан отдать отказ клиенту.
func (h *Handler) alternativeKey(p provider.Provider, current string) (string, bool) {
	if h.KeyRotation == nil {
		return "", false
	}
	kr, ok := h.KeyRotation[string(p.ID())]
	if !ok || kr == nil {
		return "", false
	}
	return kr.Take(current)
}

// hasKeyRotation сообщает, подключён ли механизм для провайдера.
//
// Отдельный метод, потому что вопрос «есть ли ключи» задаётся дважды: при
// выдаче и при решении о повторе.
func (h *Handler) hasKeyRotation(p provider.Provider) bool {
	if h.KeyRotation == nil {
		return false
	}
	kr, ok := h.KeyRotation[string(p.ID())]
	return ok && kr != nil && kr.HasAlternatives()
}

// keyRotationRegistry — хранилище механизмов ротации по провайдерам.
//
// Ключ — идентификатор провайдера: у каждого свои ключи, и смешивать их
// нельзя. Механизм общий по реализации, но отдельный по состоянию: иначе
// ключи одного сервиса уходили бы в запросы к другому.
type keyRotationRegistry map[string]*capability.KeyRotation

// NewKeyRotationRegistry создаёт пустое хранилище.
func NewKeyRotationRegistry() keyRotationRegistry { return keyRotationRegistry{} }

// Add регистрирует механизм для провайдера.
func (r keyRotationRegistry) Add(id provider.ID, kr *capability.KeyRotation) {
	if kr == nil {
		return
	}
	r[string(id)] = kr
}

// Keys возвращает ключи провайдера для справки и интерфейсов.
func (r keyRotationRegistry) Keys(id provider.ID) []string {
	kr, ok := r[string(id)]
	if !ok || kr == nil {
		return nil
	}
	return kr.KeysSnapshot()
}

// KeyRotationRegistry — управление механизмами ротации.
//
// Интерфейс, а не конкретный тип: control API и интерфейсы не должны зависеть
// от того, как устроено хранение ключей. Им достаточно уметь спросить и
// изменить.
type KeyRotationRegistry interface {
	// Keys возвращает ключи провайдера.
	Keys(id provider.ID) []string
	// KeyFingerprints возвращает отпечатки ключей провайдера.
	KeyFingerprints(id provider.ID) []string
	// HasRotation сообщает, подключён ли механизм для провайдера.
	HasRotation(id provider.ID) bool
	// AddKey добавляет ключ и сообщает, был ли он новым.
	AddKey(id provider.ID, key string) bool
	// RemoveFingerprint удаляет ключ по отпечатку.
	RemoveFingerprint(id provider.ID, fingerprint string) bool
}
