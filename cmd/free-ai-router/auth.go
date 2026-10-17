package main

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"free-ai-router/internal/config"
)

// requireToken оборачивает обработчик проверкой общего секрета.
//
// ЗАЧЕМ ОБЁРТКА, А НЕ ПРОВЕРКА ВНУТРИ ПОВЕРХНОСТЕЙ. Поверхностей две, и обе
// дают доступ к квоте оператора: одна тратит её запросами, другая умеет менять
// ключи. Проверка, поставленная внутри одной из них, оставляет вторую
// открытой — и именно это случилось: управление было закрыто, а поверхность
// моделей отвечала кому угодно.
//
// Обёртка вокруг всего маршрутизатора решает вопрос разом и не требует помнить
// о проверке при добавлении третьей поверхности.
//
// В режиме local проверка не выполняется вовсе: до loopback-слушателя кроме
// этой машины никто не дотянется, и токен там не нужен.
func requireToken(next http.Handler, cfg *config.Config) http.Handler {
	if cfg == nil || !cfg.IsServer() {
		return next
	}
	token := cfg.Token.Val

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !tokenMatches(r, token) {
			writeAuthError(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// tokenMatches сравнивает предъявленный токен с ожидаемым.
//
// Сравнение идёт за постоянное время. Побайтовый выход с ранним возвратом
// позволил бы подобрать секрет по времени ответа.
//
// Длина при этом утекает: сравнивать значения разной длины за постоянное время
// нельзя, не зная длину заранее. Но утекает именно длина, а не содержимое.
func tokenMatches(r *http.Request, want string) bool {
	presented := bearerToken(r)
	return subtle.ConstantTimeCompare([]byte(presented), []byte(want)) == 1
}

// bearerToken достаёт токен из заголовка.
//
// Принимается и форма "Bearer <токен>", и голое значение: разные клиенты ведут
// себя по-разному, а требовать строго одну форму значит ломать чужой код без
// причины.
func bearerToken(r *http.Request) string {
	v := r.Header.Get("authorization")
	if len(v) > 7 && strings.EqualFold(v[:7], "bearer ") {
		return v[7:]
	}
	return v
}

// writeAuthError отвечает отказом доступа.
//
// Форма та же, что у остальных ошибок шлюза: у клиента один словарь для
// ветвления, и второй, отличающийся, заставил бы его держать соответствие.
func writeAuthError(w http.ResponseWriter) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": "требуется токен: шлюз работает в режиме server",
			"type":    "AuthError",
			"code":    "AuthError",
		},
	})
}
