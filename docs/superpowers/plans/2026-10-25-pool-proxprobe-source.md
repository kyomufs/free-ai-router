# Pool: источник узлов proxprobe Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Заменить встроенный конвейер пула (fetch → parse → TCP → проба) чтением артефактов `proxprobe`, оставив роутеру только допуск узлов у провайдера и ротацию.

**Architecture:** Прогон `proxprobe` (обновление, разбор, TCP, GET в zen) выполняется по расписанию пула либо внешней командой, либо командой из настроек; пул читает `merged.jsonl` + `http.jsonl` из рабочего каталога proxprobe, берёт узлы с `ok: true`, прогоняет через них один настоящий POST у провайдера (допуск по квоте) и публикует ранжированный список. Адреса узлов и exit-адреса нигде не выдумываются: адрес узла приходит из подписки, exit-адрес измеряется эхом провайдера через туннель.

**Tech Stack:** Go 1.26 (stdlib only), mihomo как ядро, proxprobe 0.1.0 как внешний сборщик/проверяльщик, NixOS-модуль `services.free-ai-router`.

**Spec:** `docs/superpowers/specs/2026-10-24-pool-transport-design.md` (разделы «Стадии» и «Источник списка» заменяются разделом «Контракт» ниже).

## Global Constraints

- Код, имена, ключи, сообщения — английский; комментарии в репозитории — русский (конвенция проекта).
- Новых зависимостей нет: только stdlib. YAML не парсится (поэтому источник — `.jsonl`, а не `best.yaml`).
- `CGO_ENABLED=0 go vet ./...`, `CGO_ENABLED=0 go test ./...`, `gofmt -l .` — зелёные перед каждым коммитом.
- Провайдерский каталог и `chat_path` не меняются; допуск узла — тот же `Prober`, что обслуживает запросы.
- Секреты прогона (ссылки подписок) не логируются; в логах — только идентификаторы источников.
- Контракт «пустой список → 503 `type=pool`» сохраняется без изменений.

## Контракт (delta к спеке)

**Откуда берётся IP.** Три разных вещи, которые больше не смешиваются:

1. адрес узла (`server:port`) — из подписки, разбирает proxprobe; пул только читает;
2. exit-адрес — `egress.Observe(ctx, rt, p.TraceURL)` через туннель, после вердикта; по нему дедуп (одна квота на адрес);
3. адрес провайдера — из `providers.<id>.base_url`, пулом не выбирается.

**Стадии.** Слот расписания пула (`first_eval_utc`, `interval`) запускает:

```
[прогон proxprobe]  fetch → parse → merge → ping → probe GET  (внешняя команда)
        ↓ merged.jsonl + http.jsonl
[чтение]            узлы с ok:true, не старше max_age
        ↓
[допуск]            один POST через каждый узел — 2xx принимает, 429/ошибка отбрасывает
        ↓
[публикация]        дедуп по exit-адресу, max_nodes, pool.json
```

`source: "proxprobe"` — значение по умолчанию. `source: "builtin"` остаётся на один релиз и удаляется в Task 9.

## Review Focus

- артефакты proxprobe отсутствуют или старше `max_age` — пул обязан сказать это словами и не публиковать пустой список взамен рабочего;
- `proxprobe` вернул ненулевой код (сеть, gitlab EOF) — старый список продолжает обслуживать, `eval_state` не должен становиться `ready`;
- узел с `ok:true` может получить 429 на POST — это нормальный отказ, а не ошибка конвейера;
- протокол, который ядро умеет, а рендерер пула нет (anytls) — учитывается в счётчике `unsupported`, прогон не падает;
- два узла за одним exit-адресом — в списке остаётся один (общая квота).

---

### Task 1: Настройки источника

**Files:**
- Modify: `internal/pool/settings.go`
- Test: `internal/pool/settings_test.go`

**Interfaces:**
- Consumes: `DecodeSettings(cfg map[string]any) (Settings, error)`
- Produces: `Settings.Source string`, `Settings.ProxprobeWorkDir string`, `Settings.ProxprobeCommand []string`, `Settings.ProxprobeMaxAge time.Duration`, `Settings.PassTimeout time.Duration`

- [ ] **Step 1: Тест** — в `settings_test.go` таблица: `source` по умолчанию `"proxprobe"`, `proxprobe.command` из строки `"proxprobe all -refresh"` разбирается в три элемента, неизвестный `source` даёт ошибку `unknown pool source "x"`, `proxprobe.max_age` по умолчанию равен `Settings.Interval`.
- [ ] **Step 2:** `CGO_ENABLED=0 go test ./internal/pool/ -run TestDecodeSettings -v` → FAIL (поля отсутствуют).
- [ ] **Step 3:** Добавить поля и разбор в `DecodeSettings`; `command` — `strings.Fields`, `max_age` по умолчанию `0` означает «взять `Interval`», `pass_timeout` по умолчанию `10m`; `source=proxprobe` без `workdir` — ошибка `proxprobe.workdir is required for source "proxprobe"`.
- [ ] **Step 4:** `CGO_ENABLED=0 go test ./internal/pool/ -run TestDecodeSettings -v` → PASS.
- [ ] **Step 5:** Commit: `feat: add proxprobe source settings to the pool`.

### Task 2: Чтение прогона proxprobe

**Files:**
- Create: `internal/pool/proxprobe.go`
- Test: `internal/pool/proxprobe_test.go`

**Interfaces:**
- Consumes: рабочий каталог proxprobe (`merged.jsonl`, `http.jsonl`).
- Produces: `type Pass struct { Dir string; RefreshedAt time.Time; Nodes []Node; Counts PassCounts }`, `type PassCounts struct { Parsed, HTTPOK, Unsupported int }`, `func LoadPass(dir string, maxAge time.Duration, now time.Time) (Pass, error)`.

- [ ] **Step 1: Тест** — фикстура `testdata/pass/` (три строки в каждом файле): узел `ok:true`, узел `ok:false`, узел `ok:true` с `proto:"anytls"`. Ожидание: `LoadPass` отдаёт 1 узел, `Counts{HTTPOK:2, Unsupported:1}`, `RefreshedAt` = mtime `applied`; при mtime старше `maxAge` — ошибка, содержащая `stale` и возраст.
- [ ] **Step 2:** `CGO_ENABLED=0 go test ./internal/pool/ -run TestLoadPass -v` → FAIL.
- [ ] **Step 3:** Реализовать: `bufio.Scanner` по обоим файлам, join по `id`, `ok:false` и `http.jsonl` без пары в `merged.jsonl` пропускаются; `RefreshedAt` = минимум mtime двух файлов; отсутствие файла — `proxprobe has no merged.jsonl in <dir>: run proxprobe all first`.
- [ ] **Step 4:** `CGO_ENABLED=0 go test ./internal/pool/ -run TestLoadPass -v` → PASS.
- [ ] **Step 5:** Commit: `feat: read pooled nodes from proxprobe artifacts`.

### Task 3: Clash-профиль → Node

**Files:**
- Modify: `internal/pool/proxprobe.go`
- Test: `internal/pool/proxprobe_test.go`

**Interfaces:**
- Consumes: `raw` из `merged.jsonl` (объект Clash-прокси, тот же, что отдаёт `proxprobe export`).
- Produces: `func nodeFromClash(raw json.RawMessage) (Node, error)`; заполняет `params` так, чтобы `mihomoProxy` не менялся (ключи `uuid`, `password`, `servername`, `reality-opts.public-key`, `reality-opts.short-id`, `client-fingerprint`, `flow`, `network`, `ws-opts`…).

- [ ] **Step 1: Тест** — четыре табличных случая: vless+reality (проверить `Host`, `SNI`, `params["pbk"]`, `params["flow"]`), trojan, `ss` с `cipher`, hysteria2; и один `anytls` → `ErrUnsupportedProto`.
- [ ] **Step 2:** `CGO_ENABLED=0 go test ./internal/pool/ -run TestNodeFromClash -v` → FAIL.
- [ ] **Step 3:** Реализовать маппинг; `Node.Raw` = исходный JSON; `Node.ID` = `nodeID(n)` (тот же, что у ссылок, чтобы дедуп хранилища не расходился).
- [ ] **Step 4:** `CGO_ENABLED=0 go test ./internal/pool/ -run TestNodeFromClash -v` → PASS.
- [ ] **Step 5:** Commit: `feat: build pool nodes from clash proxies`.

### Task 4: Запуск прогона

**Files:**
- Create: `internal/pool/pass.go`
- Test: `internal/pool/pass_test.go`

**Interfaces:**
- Consumes: `Settings.ProxprobeCommand`, `Settings.PassTimeout`.
- Produces: `type PassRunner struct { Command []string; Dir string; Timeout time.Duration; LogDir string }`, `func (r PassRunner) Run(ctx context.Context) error`.

- [ ] **Step 1: Тест** — фейковый `proxprobe` это `sh -c 'printf ... > "$1/merged.jsonl"'`; проверяется код 0, запись `proxprobe.log` в `LogDir`, и что при `exit 3` ошибка содержит `proxprobe exited 3` и хвост stderr.
- [ ] **Step 2:** `CGO_ENABLED=0 go test ./internal/pool/ -run TestPassRunner -v` → FAIL.
- [ ] **Step 3:** Реализовать через `exec.CommandContext`, stdout+stderr в `proxprobe.log` (перезапись), `Timeout` → `context.WithTimeout` и `exec.ErrWaitDelay`-безопасное ожидание; пустой `Command` — no-op.
- [ ] **Step 4:** `CGO_ENABLED=0 go test ./internal/pool/ -run TestPassRunner -v` → PASS.
- [ ] **Step 5:** Commit: `feat: run a proxprobe pass from the pool`.

### Task 5: Оценка из proxprobe

**Files:**
- Modify: `internal/pool/evaluate.go`, `internal/pool/settings.go`
- Test: `internal/pool/evaluate_test.go`

**Interfaces:**
- Consumes: `LoadPass`, `PassRunner.Run`, `Prober.Run`, `Registry.Publish`.
- Produces: `Config.Source string`, `Config.LoadPass func(now time.Time) (Pass, error)`, `Config.RunPass func(ctx context.Context) error`; в `Report` — поля `Source string`, `HTTPOK int`, `Unsupported int`, `PassRan bool`.

- [ ] **Step 1: Тест** — `Source:"proxprobe"`, `RunPass` считает вызовы, `LoadPass` отдаёт два узла; фейковый провайдер отвечает 2xx на один и 429 на другой → `Report{HTTPOK:2, Probed:2, Accepted:1}`, `Registry` содержит один узел; второй тест: `RunPass` возвращает ошибку → `Report.Err != nil`, реестр не изменился.
- [ ] **Step 2:** `CGO_ENABLED=0 go test ./internal/pool/ -run TestEvaluateProxprobe -v` → FAIL.
- [ ] **Step 3:** В `Run` ветка `if e.cfg.Source == "proxprobe"`: `RunPass` → `LoadPass` → `ProbeStage(nodes)` → `Publish`; стадии fetch/parse/tcp пропускаются; `MaxProbe` ограничивает допуск (сортировка по `delay_ms`).
- [ ] **Step 4:** `CGO_ENABLED=0 go test ./internal/pool/ -run TestEvaluateProxprobe -v` → PASS.
- [ ] **Step 5:** Commit: `feat: evaluate the pool from a proxprobe pass`.

### Task 6: Наблюдаемость

**Files:**
- Modify: `internal/pool/store.go`, `internal/pool/transport.go`, `internal/control/handlers.go`, `internal/tui/pool.go`
- Test: `internal/pool/store_test.go`, `internal/control/pool_test.go`

**Interfaces:**
- Consumes: `Report`, `State`.
- Produces: `State.Source string`, `State.LastPass *PassReport` где `PassReport{Started, Finished time.Time; ExitCode int; Parsed, HTTPOK, Unsupported, Accepted int; Error string}`; в `Status()` — те же поля.

- [ ] **Step 1: Тест** — запись/чтение `pool.json` с `LastPass` (round-trip), `/_control/pool` отдаёт `source` и `last_pass.accepted`; при `Err` поле `error` непустое.
- [ ] **Step 2:** `CGO_ENABLED=0 go test ./internal/... -run 'TestStore|TestPool' -v` → FAIL.
- [ ] **Step 3:** Реализовать поля, маппинг в `handlePool` и строку в TUI-блоке: `source proxprobe · pass 16:02 ok (507 parsed, 147 http, 12 accepted)`.
- [ ] **Step 4:** `CGO_ENABLED=0 go test ./internal/... -run 'TestStore|TestPool' -v` → PASS.
- [ ] **Step 5:** Commit: `feat: expose the proxprobe pass in the pool surfaces`.

### Task 7: CLI

**Files:**
- Modify: `cmd/free-ai-router/flags.go`, `cmd/free-ai-router/up.go`
- Test: `cmd/free-ai-router/pool_flags_test.go`

**Interfaces:**
- Produces: флаги `--pool-source`, `--pool-proxprobe-workdir`, `--pool-proxprobe-command`, `--pool-proxprobe-max-age`, `--pool-pass-timeout`; переменные `FREE_AI_ROUTER_POOL_SOURCE`, `…_PROXPROBE_WORKDIR`, `…_PROXPROBE_COMMAND`, `…_PROXPROBE_MAX_AGE`, `…_PASS_TIMEOUT`.

- [ ] **Step 1: Тест** — флаг перекрывает env, env перекрывает файл, `--pool-proxprobe-command` с пробелами разбирается как argv без шелла.
- [ ] **Step 2:** `CGO_ENABLED=0 go test ./cmd/... -run TestPoolFlags -v` → FAIL.
- [ ] **Step 3:** Реализовать по образцу существующих pool-флагов.
- [ ] **Step 4:** `CGO_ENABLED=0 go test ./cmd/... -run TestPoolFlags -v` → PASS.
- [ ] **Step 5:** Commit: `feat: add proxprobe flags to the pool`.

### Task 8: NixOS

**Files:**
- Modify: `~/.config/nixos-config/modules/services/free-ai-router.nix`
- Test: `nix eval .#nixosConfigurations.asusfx506hc.config.systemd.services.free-ai-router.serviceConfig` (только eval и build, без switch)

**Interfaces:**
- Produces: `services.free-ai-router.transport ∈ {direct, pool}`, `services.free-ai-router.transportSettings` (freeform attrs, рендерится в `transport_settings`), `services.free-ai-router.poolWorkdir` (str, по умолчанию `/var/lib/free-ai-router/proxprobe`).

- [ ] **Step 1:** Расширить `enum`, добавить `transportSettings = lib.mkOption { type = lib.types.attrsOf lib.types.anything; default = {}; }` и `poolWorkdir`; в `configFile` дописать `transport_settings = cfg.transportSettings;`.
- [ ] **Step 2:** В юните: `path = [ pkgs.mihomo ] ++ (if cfg.transport == "pool" then [ pkgs.proxprobe ] else [ ])`, `serviceConfig.ReadWritePaths = [ cfg.poolWorkdir ]`, `StateDirectory` для каталога состояния.
- [ ] **Step 3:** `nix eval` конфигурации с `transport = "pool"` и примером `transportSettings.pool` — eval проходит, `transport_settings` в `/nix/store`-файле содержит `source = "proxprobe"`.
- [ ] **Step 4:** `nh os build .` (или `nixos-rebuild build`) → успешно; switch не выполнять.
- [ ] **Step 5:** Commit в nixos-config: `feat(free-ai-router): pool transport with proxprobe source`.

### Task 9: Живой прогон и удаление встроенного конвейера

**Files:**
- Delete: `internal/pool/fetch.go`, `internal/pool/share.go` (оставить `Node`, `nodeID`), TCP-стадия в `evaluate.go`, `subscriptions`/`tcp_*` в `settings.go`
- Modify: `README.md`, `docs/superpowers/specs/2026-10-24-pool-transport-design.md`

- [ ] **Step 1:** Прогнать `~/opencode-probe/probe.sh` → `merged.jsonl`, `http.jsonl`; запустить демон с `source=proxprobe`, `workdir` на этот каталог; проверить `/_control/pool` → `last_pass.http_ok=68`, `accepted>0` или явный 429-отказ.
- [ ] **Step 2:** Ротация вручную (`POST /_control/pool`) → `last_pass` обновился, `eval_state=ready`.
- [ ] **Step 3:** Удалить `proxprobe`-артефакты → `503 type=pool` с текстом про отсутствующий `merged.jsonl`.
- [ ] **Step 4:** Удалить встроенный конвейер и его тесты; `CGO_ENABLED=0 go test ./...` зелёный; `gofmt -l .` пусто.
- [ ] **Step 5:** Переписать раздел README про стадии; Commit: `refactor: drop the builtin pool pipeline`.
