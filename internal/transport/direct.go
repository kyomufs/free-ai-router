package transport

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

// direct реализует транспорт прямой маршрутизации хоста.
//
// ЭТО НЕ «заглушка по умолчанию», а часть задачи. Агент живёт долго, а его ход
// состоит из многих запросов; без удержания соединения каждый ход платит новый
// TCP- и TLS-handshake. Поэтому транспорт держит тёплый пул соединений — это
// ровно то, ради чего шлюз существует как отдельный процесс, а не как
// библиотека внутри агента.
type direct struct {
	rt *http.Transport
}

// DirectConfig — настройки прямого транспорта.
type DirectConfig struct {
	// Family пинит адресное семейство: "auto", "v4" или "v6".
	//
	// Смысл не в производительности: у некоторых провайдеров лимит привязан к
	// строке адреса, и разные семейства могут попадать в разные бакеты.
	// Пиннинг делает выбор семейства явным.
	Family string
}

// Числа пула выбраны под профиль нагрузки шлюза: много последовательных
// запросов от одного клиента к одному хосту.
const (
	maxIdleConns        = 128
	maxIdleConnsPerHost = 64
	idleConnTimeout     = 90 * time.Second
	tlsHandshakeTimeout = 15 * time.Second
	dialTimeout         = 20 * time.Second
	keepAliveInterval   = 30 * time.Second
)

// NewDirect создаёт прямой транспорт.
func NewDirect(cfg map[string]any) (Transport, error) {
	var parsed DirectConfig
	if raw, ok := cfg["family"]; ok {
		if s, ok := raw.(string); ok {
			parsed.Family = s
		}
	}
	return &direct{rt: buildDirectTransport(parsed.Family)}, nil
}

// buildDirectTransport собирает http.Transport с тёплым пулом.
func buildDirectTransport(family string) *http.Transport {
	dialer := &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: keepAliveInterval,
	}

	// Семейство задано: берём дозвон во владение и подменяем имя сети.
	// Ошибка семейства становится обычной ошибкой соединения — так её и
	// увидит вызывающий, и отдельного пути для неё не нужно.
	dial := dialer.DialContext
	if network := networkFor(family); network != "" {
		dial = func(ctx context.Context, _, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		}
	}

	return &http.Transport{
		DialContext:           dial,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          maxIdleConns,
		MaxIdleConnsPerHost:   maxIdleConnsPerHost,
		IdleConnTimeout:       idleConnTimeout,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
}

// networkFor переводит настройку семейства в имя сети.
//
// Пустая строка означает «пусть решает система»: так работает "auto" и любое
// нераспознанное значение. Неизвестное значение не является ошибкой — оно
// означает ровно то же, что и "auto".
func networkFor(family string) string {
	switch family {
	case "v4":
		return "tcp4"
	case "v6":
		return "tcp6"
	default:
		return ""
	}
}

// ID реализует Transport.
func (d *direct) ID() ID { return Direct }

// RoundTripper реализует Transport.
func (d *direct) RoundTripper() (http.RoundTripper, error) { return d.rt, nil }

// Start реализует Transport. Прямому транспорту поднимать нечего.
func (d *direct) Start(context.Context) error { return nil }

// Stop реализует Transport.
func (d *direct) Stop() error {
	d.rt.CloseIdleConnections()
	return nil
}

// Info реализует Transport.
func (d *direct) Info() Info {
	return Info{
		ID:      Direct,
		Summary: "прямая маршрутизация хоста с тёплым пулом соединений",
	}
}

// NewRegistryWithDefaults создаёт реестр со всеми встроенными транспортами.
//
// Функция нужна, чтобы точка входа не собирала реестр руками: новый транспорт
// регистрируется в одном месте, и ни один вызывающий не забывает его добавить.
func NewRegistryWithDefaults() *Registry {
	r := NewRegistry()
	r.Register(Info{
		ID:      Direct,
		Summary: "прямая маршрутизация хоста с тёплым пулом соединений",
	}, NewDirect)
	return r
}
