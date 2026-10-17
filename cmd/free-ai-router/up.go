package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"free-ai-router/internal/catalog"
	"free-ai-router/internal/config"
	"free-ai-router/internal/control"
	"free-ai-router/internal/gateway"
	"free-ai-router/internal/provider"
	"free-ai-router/internal/state"
	"free-ai-router/internal/transport"
)

// daemon — собранный демон со всеми частями.
type daemon struct {
	cfg       *config.Config
	providers *provider.Registry
	transport transport.Transport
	keys      *keyRegistry
	state     *state.Store
	log       *state.Logger
	handler   http.Handler
}

// build собирает демон из конфигурации.
//
// Порядок шагов важен: каждый следующий пользуется предыдущим. Журнал
// открывается первым, потому что о любой неудаче дальше нужно сказать в него;
// состояние — раньше провайдеров, потому что механизмы на него пишут счётчики.
func build(cfg *config.Config) (*daemon, error) {
	d := &daemon{cfg: cfg}

	// Журнал. Ротация делается при открытии: это единственный момент, когда
	// файл можно переместить, не гоняя наперегонки с писателями.
	logger, logErr := state.OpenLogger(cfg.Paths.LogFile)
	if logErr != nil {
		// Журнал не критичен: демон работает и без него, а причину видно в
		// stderr. Отказ стартовать из-за журнала был бы несоразмерен.
		fmt.Fprintf(os.Stderr, "предупреждение: журнал недоступен: %v\n", logErr)
	}
	d.log = logger

	// Состояние.
	st, err := state.Open(cfg.Paths.StateFile)
	if err != nil {
		// Повреждённый файл не блокирует старт: счётчики нужны для справки.
		if d.log != nil {
			d.log.Warn("состояние: %v", err)
		}
	}
	d.state = st

	// Провайдеры.
	providers, err := catalog.Build(cfg)
	if err != nil {
		return nil, fmt.Errorf("провайдеры: %w", err)
	}
	d.providers = providers

	// Транспорт.
	tr, err := transport.NewRegistryWithDefaults().Build(transport.ID(cfg.Transport.Val), cfg.TransportConfig)
	if err != nil {
		return nil, fmt.Errorf("транспорт: %w", err)
	}
	d.transport = tr

	// Механизмы ротации ключей.
	d.keys = newKeyRegistry(cfg, d.providers)

	d.handler = d.buildHandler()
	return d, nil
}

// buildHandler собирает поверхности в один обработчик.
//
// ПРОВЕРКА ТОКЕНА ОБОРАЧИВАЕТ ОБЕ ПОВЕРХНОСТИ. Это принципиально: поверхность
// моделей тратит квоту оператора — она ходит к провайдеру от его имени, — а
// поверхность управления умеет менять ключи. Открытая любая из двух делает
// режим server открытым релеем, поэтому проверка стоит вокруг всего
// маршрутизатора, а не внутри одной из поверхностей.
//
// Раньше проверка была только внутри управления, и поверхность моделей
// оставалась открытой: любой, кто нашёл порт, мог расходовать квоту.
func (d *daemon) buildHandler() http.Handler {
	root := http.NewServeMux()

	// Поверхность OpenAI.
	gw := &gateway.Handler{
		Providers:       d.providers,
		Transport:       d.transport,
		State:           d.state,
		Log:             d.log,
		KeyRotation:     d.keys.rotationMap(),
		DefaultProvider: d.keys.firstProvider(),
	}
	gw.AllRoutes(root)

	// Поверхность управления.
	ctrl := &control.Handler{
		Providers:   d.providers,
		KeyRotation: d.keys,
		State:       d.state,
		Log:         d.log,
		Config:      d.cfg,
		Transports:  transportIDs(),
	}
	ctrl.Routes(root)

	// Проверка доступа поверх обеих поверхностей.
	//
	// Управляющая поверхность получает признак «токен уже проверен», чтобы не
	// проверять его второй раз: двойная проверка не ошибка, но она означала бы
	// два места, где решается один вопрос.
	return requireToken(root, d.cfg)
}

// serve запускает демон и работает до сигнала остановки.
func (d *daemon) serve(ctx context.Context) error {
	if err := d.transport.Start(ctx); err != nil {
		return fmt.Errorf("запуск транспорта: %w", err)
	}
	defer d.transport.Stop()

	srv := &http.Server{
		Addr:              d.cfg.Listen.Val,
		Handler:           d.handler,
		ReadHeaderTimeout: 15 * time.Second,
	}

	ln, err := net.Listen("tcp", d.cfg.Listen.Val)
	if err != nil {
		return fmt.Errorf("адрес %s занят: %w", d.cfg.Listen.Val, err)
	}

	if d.log != nil {
		d.log.Info("демон запущен на %s, режим %s, транспорт %s, провайдеров %d",
			d.cfg.Listen.Val, d.cfg.Mode.Val, d.cfg.Transport.Val, d.providers.Len())
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ln)
	}()

	select {
	case <-ctx.Done():
		// Корректная остановка: ждём завершения текущих запросов, чтобы не
		// оборвать потоки на середине.
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		if d.log != nil {
			d.log.Info("демон остановлен")
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// cmdUp запускает демон.
func cmdUp(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	if f == nil {
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	f.applyToConfig(cfg)

	// Печать эффективной конфигурации полезна и сама по себе, и как способ
	// проверить слияние уровней, ничего не запуская.
	if f.printConfig {
		return printEffectiveConfig(cfg)
	}

	d, err := build(cfg)
	if err != nil {
		return err
	}
	defer func() {
		if d.log != nil {
			_ = d.log.Close()
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return d.serve(ctx)
}

// version возвращает версию сборки.
func version() string {
	if v := buildVersion; v != "" {
		return v
	}
	return "неизвестно"
}

// buildVersion подставляется при сборке через -ldflags.
var buildVersion string

// transportIDs возвращает доступные транспорты как строки.
//
// Интерфейсам нужны строки, а не типизированные идентификаторы: они строят из
// них выбор, и типизация там ничего не добавляет.
func transportIDs() []string {
	ids := transport.NewRegistryWithDefaults().IDs()
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, string(id))
	}
	return out
}
