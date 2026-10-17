# free-ai-router — план

OpenAI-совместимый шлюз-агрегатор **бесплатных AI-провайдеров** для любого
агента, умеющего смотреть своим `baseURL` на HTTP-эндпоинт — DSH, Crush,
opencode CLI, `curl`.

Задача: **дать агенту доступ к бесплатным моделям, говоря с каждым сервисом
ровно так, как говорит с ним его собственный клиент.**

```
local    агент ─────┐              server   агент (ПК) ────┐
         шлюз ──────┴─ ПК                   шлюз ──────────┴─ сервер
```

> **Статус: проектный документ.**
>
> Контракт провода выведен из исходников opencode 2.0.30
> (`/home/kyomufs/Projects/opencode`, коммит `fe87944b6a`) и из каталога
> моделей `packages/core/src/models-dev/snapshot.txt`. opencode — первый
> разобранный провайдер, но не единственный: см. §3.
>
> Пометки:
> - *(источник)* — прочитано в исходниках opencode, указан файл;
> - *(решение)* — выбор, сделанный здесь;
> - *(проверить)* — требует живого замера.

> **Границы объёма.** Шлюз — **прозрачный посредник**. Он делает то, что делал
> бы клиент, и **не добавляет поведения поверх**. Отсюда три правила:
>
> 1. **Шлюз не повторяет запросы.** У клиента своя политика повторов (§2.1) —
>    вторая политика поверх неё дала бы втрое больше запросов, чем он
>    рассчитывал.
> 2. **Шлюз не подменяет отказ.** Любой отказ провайдера отдаётся клиенту
>    дословно — статус, тело, заголовки — в той же форме, в какой его увидел бы
>    клиент при прямом обращении.
> 3. **Один транспорт.** Запрос уходит прямой маршрутизацией хоста.

---

## 1. Что делает клиент opencode

Это ядро проекта. Всё остальное — инфраструктура вокруг одного вопроса: **что
именно уходит на провод**.

### 1.1 Карта исходников

*(источник)*

| Что | Файл |
|---|---|
| HTTP-заголовки запроса | `packages/core/src/session/model-request.ts` |
| session id | `packages/schema/src/identifier.ts`, `session-id.ts` |
| message id | `packages/schema/src/session-message.ts` |
| affinity | `packages/core/src/session/affinity.ts` |
| Тело запроса, chat-провод | `packages/ai/src/protocols/openai-chat.ts` |
| Системный промпт | `packages/core/src/session/runner/prompt/system.txt` |
| Рендер промпта | `packages/core/src/session/system-prompt.ts` |
| Инструменты | `packages/core/src/tool/plugin/shell.ts`, `read.ts` |
| Метаданные моделей | `packages/core/src/models-dev/snapshot.txt` |
| Классификация отказов | `packages/ai/src/provider-error.ts` |
| Политика повторов | `packages/core/src/session/runner/retry.ts` |
| Разбор `retry-after` | `packages/ai/src/route/executor.ts` |
| XDG-корни | `packages/util/src/global-roots.ts` |
| Анонимность | `packages/console/app/src/lib/inference-proxy.ts` |

### 1.2 Заголовки

*(источник: `model-request.ts`)*

```ts
const affinity = SessionAffinity.get(session)
LLM.request({
  model: model.model,
  http: {
    headers: {
      "x-opencode-session-id": session.id,
      ...(session.parentID ? { "x-opencode-parent-session-id": session.parentID } : {}),
      "x-session-affinity":  affinity,
      "X-Session-Id":        affinity,
      ...(session.parentID ? { "x-parent-session-id": session.parentID } : {}),
      "User-Agent":          App.useragent(app),
      "x-opencode-project":  session.projectID,
      "x-opencode-session":  affinity,
      "x-opencode-client":   app.name,
    },
  },
  promptCacheKey: /^ses_[0-9a-f]{64}$/.test(affinity) ? affinity.slice(4) : affinity,
  ...
})
```

Четыре следствия:

**1. `x-opencode-session` и `x-opencode-session-id` несут разное.**

*(источник: `affinity.ts`)*

```ts
export const get = (session) =>
  session.parentID ?? session.fork?.sessionID ?? session.id
```

Первый получает **affinity** (корень группы кеша), второй — id самой сессии.
Без родителя значения совпадают, с родителем расходятся.

**2. `x-opencode-project` — это `session.projectID`.**

*(источник: `packages/schema/src/project-id.ts`, `packages/core/src/project.ts`)*

Вне git-чекаута — литеральная строка `global`, внутри чекаута — `prj_<hex>`.

**3. Заголовки родителя — только для вложенных сессий.** Условие явное в коде.

**4. `User-Agent` = `opencode/${channel}/${version}/${name}`.**

*(источник: `packages/core/src/app.ts`)*

Три части зависят от сборки. Шлюз обязан воспроизводить **ту же строку**, что
отправил бы целевой клиент, а не собирать свою.

### 1.3 Идентификаторы

*(источник: `packages/schema/src/identifier.ts`)*

```ts
const length = 26
const chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

export function descending() { return create(true) }

export function create(descending: boolean, timestamp = Date.now()) {
  if (timestamp !== lastTimestamp) { lastTimestamp = timestamp; counter = 0 }
  counter++
  const current = BigInt(timestamp) * 0x1000n + BigInt(counter)
  const value = descending ? ~current : current
  const time = Array.from({ length: 6 }, (_, index) =>
    Number((value >> BigInt(40 - 8 * index)) & 0xffn).toString(16).padStart(2, "0"),
  ).join("")
  const bytes = crypto.getRandomValues(new Uint8Array(length - 12))
  return time + Array.from(bytes, (byte) => chars[byte % 62]).join("")
}
```

| Часть | Размер | Содержимое |
|---|---|---|
| префикс | `ses_` | *(источник: `session-id.ts` — `schema.make("ses_" + descending())`)* |
| time | 12 hex | 6 байт `~current`, big-endian |
| random | 14 base62 | 14 байт, каждый `% 62` |

`current = timestamp * 4096 + counter`; счётчик сбрасывается при смене
миллисекунды. Значение инвертируется — id **убывают** во времени, и это часть
формулы, а не косметика.

Префикс message id — `msg_` *(источник: `session-message.ts` —
`Schema.isStartingWith("msg_")`)*.

**Свойство для теста:** id, построенный по формуле, декодируется обратно в
исходный момент времени. Это проверяется без сети.

### 1.4 Инструменты

Инструмент **называется `shell`**, не `bash`.

*(источник: `packages/core/src/database/v1-migration.bun.ts`)*

```ts
const RENAMED_TOOLS: Readonly<Record<string, string>> = {
  bash: "shell", task: "subagent", apply_patch: "patch",
}
```

*(источник: `packages/core/src/tool/plugin/shell.ts`)*

```ts
export const name = "shell"
const description = (shell?: string) =>
  [
    "Execute a shell command and return its output.",
    ...(shell ? [`Commands run on ${OS} using ${shell}.`] : []),
    "Quote file paths containing spaces or special characters.",
    "Prefer dedicated tools over shell commands when possible.",
    "When output is large, the full result is saved to a file and a truncated preview is returned.",
    "Rely on automatic truncation unless filtering the output is more useful.",
    "Commands accept an optional timeout, background commands have no timeout by default.",
    "Background commands return immediately, and you will be notified when they complete.",
  ].join(" ")

const OS = process.platform === "darwin" ? "macOS"
  : process.platform === "win32" ? "Windows"
  : process.platform === "linux" ? "Linux" : process.platform

export const Input = Schema.Struct({
  command:    Schema.String,
  workdir:    Schema.optionalKey(Schema.String),
  timeout:    Schema.optionalKey(NonNegativeInt),
  background: Schema.optionalKey(Schema.Boolean),
})
```

*(источник: `packages/core/src/tool/plugin/read.ts`)*

```ts
export const name = "read"
const LocationInput = Schema.Struct({
  path:   Schema.String,
  offset: ReadToolFileSystem.PageInput.fields.offset,
  limit:  ReadToolFileSystem.PageInput.fields.limit,
})
```

**Имена не транслируются при отправке** *(источник:
`packages/ai/src/protocols/shared.ts` — `flattenTools`/`flattenToolRequest`)*:
инструмент уходит под тем именем, под которым зарегистрирован.

> Выбор имени — **условие прохождения**: гейт отвечает на записи, поэтому имя,
> описание и схема должны совпадать с тем, что шлёт клиент. Целевой клиент —
> 2.0.30, он шлёт `shell` *(проверить — §11.1)*.

### 1.5 Системный промпт

*(источник: `packages/core/src/session/runner/prompt/system.txt`)*

Шаблон с одной подстановкой `${OPENCODE_TOOL_GUIDANCE}`:

```
You are an AI agent running in OpenCode, a coding agent harness. Help the user
accomplish their goals using the tools you have available.

# Harness
- Responses are rendered as GitHub-flavored Markdown.
- `<system-reminder>` blocks are harness instructions, not user-authored content. Read and follow them.
- Prefer parallelizing independent tool calls.
${OPENCODE_TOOL_GUIDANCE}

# Communication
- Use clear file paths when referring to files.
- Keep responses clear and concise, and avoid unnecessary technical jargon.

# Working in codebases
- Keep changes consistent with the structure, naming, style, and patterns of the surrounding code.
- Treat unfamiliar files or changes as potential user work and investigate before deleting or overwriting them.
```

Подстановка собирается из **имён активных инструментов**
*(источник: `packages/core/src/session/system-prompt.ts`)*:

```ts
if (tools.includes("shell")) instructions.push(/* два пункта про shell */)
if (tools.includes("write")) instructions.push(/* пункт про write */)
if (tools.includes("edit"))  instructions.push(/* пункт про edit */)
return prompt.replace("${OPENCODE_TOOL_GUIDANCE}", instructions.join("\n"))
```

**Промпт не хранится — он рендерится.** Это чистая функция от списка имён,
и её можно проверить тестом без сети.

### 1.6 Модели

Каталог — данные, а не константа *(источник: `snapshot.txt`, снапшот
`https://models.opencode.ai/api.json`)*. Провайдер opencode:

```jsonc
{
  "id": "opencode",
  "npm": "@ai-sdk/openai-compatible",
  "api": "https://opencode.ai/inference/v1",
  "env": ["OPENCODE_API_KEY"]
}
```

**124 модели, из них 43 бесплатные** (`cost.input == 0`). Схема:

```jsonc
{
  "id": "mimo-v2.6-flash-free",
  "name": "MiMo-V2.6-Flash Free",
  "description": "…",
  "family": "mimo",
  "attachment": true,            // принимает картинки
  "reasoning": true,             // умеет думать
  "reasoning_options": [],       // типизированная лестница усилий
  "tool_call": true,
  "interleaved": { "field": "reasoning_content" },   // куда писать reasoning
  "temperature": true,
  "modalities": { "input": ["text","image","audio","video"], "output": ["text"] },
  "limit": { "context": 200000, "output": 32000 },
  "cost": { "input": 0, "output": 0, "cache_read": 0 },
  "canonical_model_id": "xiaomi/mimo-v2.6-flash"
}
```

**`reasoning_options` типизированы** *(источник: снапшот)*:

```jsonc
[{"type":"toggle"}]                                        // вкл/выкл
[{"type":"effort","values":["minimal","low","medium","high","xhigh"]}]
[{"type":"budget_tokens","max":81920}]
```

Глобальный словарь усилий *(источник: `packages/ai/src/schema/options.ts`)*:

```ts
export const ReasoningEfforts = ["none","minimal","low","medium","high","xhigh","max"] as const
```

**`interleaved.field`** — имя поля, в которое ответ несёт reasoning
*(источник: `openai-chat.ts`)*. Для `mimo` это `reasoning_content`. Шлюз
обязан писать размышления туда, куда их ждёт клиент.

### 1.7 Бюджет токенов

Для будущего §4 нужны правила вывода `max_tokens` *(источник:
`packages/core/src/session/model-request.ts`)*:

| Значение | Величина |
|---|---|
| фолбэк, если каталог не дал лимит | `32_000` |
| потолок просьбы модели | `256_000` |
| потолок для суммаризации | `32_000` |
| минимум | `1_024` |
| оценка текста | 4 символа на токен, с запасом 15% |

Резерв на ответ вычисляется из окна за вычетом запроса; при нехватке места
просьба снижается, но не ниже минимума.

### 1.8 Анонимность

*(источник: `packages/console/app/src/lib/inference-proxy.ts`)*

```ts
const anonymous = !key || key === "public"
if (anonymous) for (const name of ["authorization","x-api-key","x-goog-api-key"])
  forwarded.headers.delete(name)
if (!anonymous) forwarded.headers.set("authorization", `Bearer ${key}`)
```

`Bearer public` — **маркер анонимности**, а не ключ. Прокси его снимает.
Реальный адрес запроса прокси берёт из `cf-connecting-ip` и пересылает как
`x-real-ip`.

---

## 2. Поведение при отказе

Это место, где легче всего испортить систему, поэтому оно описано первым
среди решений.

### 2.1 Шлюз не повторяет запросы

Клиент уже имеет политику повторов, и она подробно разработана
*(источник: `packages/core/src/session/runner/retry.ts`)*:

```
экспонента от 2s с потолком 10s на промежуток, до 10 повторов:
2, 4, 8, затем 10 × 7 — около 84s ожидания, когда падают все попытки
```

Отдельно: **таймауты ограничены тремя повторами** — попытка, уже ждавшая
минуты, не должна держать шаг почти час.

Плюс клиент уважает `retry-after` *(источник: `retry.ts`)*:

```ts
const RETRY_AFTER_MAX = Duration.toMillis("15 minutes")
const retryAfter = (input: Input) => {
  if (input.cause.reason._tag === "RateLimit" || input.cause.reason._tag === "ProviderInternal")
    return input.cause.reason.retryAfterMs === undefined
      ? undefined
      : Math.min(input.cause.reason.retryAfterMs, RETRY_AFTER_MAX)
  return undefined
}
```

**Следствие для шлюза.** Если шлюз добавит свою политику повторов, клиент
получит `3 × 10` запросов вместо `10`, и время ответа вырастет втрое. Шлюз
отдаёт отказ наверх с первого раза.

### 2.2 Шлюз отдаёт отказ дословно

Клиент классифицирует отказ сам *(источник: `packages/ai/src/provider-error.ts`,
функция `classifyProviderFailure`)*, причём по богатому набору признаков:
статус, коды из тела, фразы в тексте, `error.type`, `error.code`,
`innererror`, `metadata`, `[label]` в начале переписанного сообщения.

Из этого же файла — точная таблица приоритетов, которую шлюз обязан
**не нарушать**, потому что клиент уже умеет её применять:

| Условие | Класс клиента |
|---|---|
| `402`, коды квоты, `429` с текстом квоты | `QuotaExceeded` |
| `401`, `403`, коды аутентификации | `Authentication` |
| `429`, коды и текст лимита | `RateLimit` |
| `408`, `409`, `5xx` | `ProviderInternal` |
| прочие `4xx` | `InvalidRequest` |
| ничего не распознано | `UnknownProvider` |

Два следствия, критичные для нас:

**Первое: `403` — это аутентификация.** В таблице `403` стоит рядом с `401`
в строке `Authentication`. Значит `403 FreeTierError` клиент видит как
отказ в доступе, а не как «попробуй ещё раз».

**Второе: что считается повторяемым** *(источник: там же, `isRetryable`)*:

| Класс | Повторять? |
|---|---|
| `RateLimit`, `ProviderInternal` | да |
| `Transport` | да, кроме `delivery === "rejected"` |
| `UnknownProvider` | да |
| `Authentication`, `QuotaExceeded`, `ContentPolicy`, `InvalidRequest`, `Timeout` | **нет** |

То есть клиент повторит `429` сам. А `403` — не повторит никогда.

**Что из этого следует для шлюза:** он **ничего не переклассифицирует**.
Отдаёт клиенту тот же статус, то же тело и те же заголовки, что получил от
апстрима, и позволяет клиенту применить его собственную логику.

### 2.3 Единственная работа шлюза здесь

Одна: **не потерять информацию**. Тело отказа бывает больше, чем нужно для
одного сообщения, и содержит поля, по которым клиент принимает решения. Шлюз
передаёт тело целиком, не разбирая его и не переупаковывая.

Заголовки, влияющие на решение клиента, тоже пробрасываются как есть — в
первую очередь `retry-after` и `retry-after-ms` *(источник:
`packages/ai/src/route/executor.ts`)*:

```ts
const retryAfterMs = (headers: Record<string, string>) => {
  const millis = Number(headers["retry-after-ms"])
  if (Number.isFinite(millis)) return Math.max(0, millis)
  const value = headers["retry-after"]
  if (!value) return undefined
  const seconds = Number(value)
  if (Number.isFinite(seconds)) return Math.max(0, seconds * 1000)
  const date = Date.parse(value)
  if (!Number.isNaN(date)) return Math.max(0, date - Date.now())
  return undefined
}
```

Клиент читает оба заголовка, и оба должны дойти.

### 2.4 Что шлюз делает при своей собственной ошибке

Отдельный случай: апстрим недостижим, соединение разорвано, запрос не собрался.
Это **не** отказ апстрима, и выдать его за отказ нельзя.

Ответ — `502` в форме, которую awaiting клиент поймёт как транспортный сбой.
Тело называет причину; статус однозначен.

---

## 3. Устройство

**ТРИ ОРТОГОНАЛЬНЫЕ ОСИ.** Каждая отвечает на свой вопрос, и ни одна не зависит
от двух других. Это главное решение в устройстве проекта.

```
ЧТО за сервис          КАК обходить            КУДА уходит трафик
provider               capability              transport
├─ модели              ├─ ротация ключей        └─ direct
├─ сборка запроса      ├─ … (добавляется)
└─ разбор отказа
```

```
┌──────────────────────────────────────────────────────────────┐
│ gateway    поверхность OpenAI. Приём, разрешение модели,      │
│            стриминг, дословная передача отказа                │
├───────────────────────────┬──────────────────────────────────┤
│ provider                  │ capability                       │
│ что за сервис: модели,    │ переиспользуемые механизмы:      │
│ сборка запроса, разбор    │ ротация ключей, …                │
│ отказа                    │                                  │
├───────────────────────────┴──────────────────────────────────┤
│ transport  как запрос покидает машину                         │
└──────────────────────────────────────────────────────────────┘
```

### 3.1 Почему оси разделены именно так

**`provider` отвечает ровно на три вопроса:** как называются его модели и что
они умеют, как собрать к нему запрос, и как понять, что он ответил отказом.
Больше ничего. Провайдер НЕ описывает свою политику повторов, ротацию ключей
или транспорт.

**`capability` — это механизмы обхода.** Ротация ключей работает одинаково у
любого сервиса, выдающего ключи. Подмена формы запроса нужна каждому, кто
фильтрует клиентов по виду запроса. Проксирование трафика через чужой сервер
вообще не зависит от провайдера.

Разделение нужно потому, что **механизмы обхода часто совпадают у непохожих
провайдеров**. Если бы каждый сервис нёс свою копию общего механизма, похожие
сервисы заводили бы одинаковые копии, а исправление, найденное на одном, не
доходило бы до остальных.

**`transport` — куда уходит трафик.** Проксирование через чужой сервер
работает для всех провайдеров одинаково: меняется только адрес выхода.

### 3.2 Как добавить провайдера

Порядок такой *(решение)*:

1. **Сервис говорит на OpenAI-совместимом протоколе** — писать код не нужно.
   Достаточно секции в конфигурации:

   ```jsonc
   "providers": {
     "zenmux": {
       "type": "openai",
       "base_url": "https://zenmux.ai/api/v1",
       "api_key_env": "ZENMUX_API_KEY",
       "models": [{"id": "free-model", "context": 128000}]
     }
   }
   ```

2. **Сервису нужны обязательные инструменты, особый системный промпт или свои
   заголовки сессии** — сначала проверяются поля настройки. Часто этого
   достаточно.

3. **Всё-таки нужен свой код** — берётся пакет `opencode` как образец: в нём
   есть всё, что бывает нужно. Свой пакет реализует три метода интерфейса и
   больше ничего.

Регистрация нового типа — **одна строка** в таблице сборки, а не ветка в логике:

```go
var builders = map[string]Builder{
    "opencode": buildOpenCode,
    "openai":   buildOpenAI,
}
```

### 3.3 Слои

**`wire`** — общий контракт, не привязанный к провайдеру: идентификаторы сессии
и сообщения, нейтральная форма запроса. Чистый: без HTTP, без горутин, без
неявного времени. Поэтому проверяется свойствами без сети.

**`gateway`** — HTTP. Принимает запрос, разрешает провайдера и модель, зовёт
`provider.Build`, ведёт поток, передаёт отказ дословно. Никакой политики
повторов (§2.1), кроме смены ключа.

**`catalog`** — связка конфигурации и провайдеров. Существует отдельным пакетом
именно потому, что `provider` не знает о конфигурации, а `config` — о
провайдерах; связывает их только он.

### 3.4 `mode` — форма развёртывания

Ровно два режима. Различаются **не адресом, а тем, где живёт агент**.

```
local    free-ai-router ─┐
         агент ──────┴─ обе стороны на ПК

server   free-ai-router ──── сервер
         агент ───────── ПК
```

**`local`** — free-ai-router и агент на одной машине.

```jsonc
{ "mode": "local", "listen": "127.0.0.1:8787" }
```

Токен не проверяется вовсе: до loopback-слушателя кроме этой машины никто не
дотянется.

**`server`** — free-ai-router на сервере, агент на ПК.

```jsonc
{ "mode": "server", "listen": "0.0.0.0:8787", "token": "…" }
```

Токен обязателен, проверка оборачивает **обе** поверхности, включая
`/_zenctl/*`: control API умеет израсходовать ключи пула, поэтому открытый
control обнулил бы проверку шлюза. Сравнение constant-time — побайтовый ранний
выход утекает префикс секрета через тайминг.

Пустой токен при `server` — **отказ старта**. Открытый релей на достижимом
адресе расходует квоту оператора для любого, кто найдёт порт.

`server` означает ровно одно: шлюз стоит там, где нужен стабильный адрес и
постоянно включённая машина. Агент остаётся локальным.

### 3.5 Транспорт

Запросы уходят прямой маршрутизацией хоста, через `http.Transport` с тёплым
keep-alive-пулом *(решение)*:

```go
MaxIdleConns:        128
MaxIdleConnsPerHost: 64
IdleConnTimeout:     90 * time.Second
ForceAttemptHTTP2:   true
```

Пул — часть задачи, а не оптимизация: агент живёт долго, а его ход состоит из
многих запросов. Без удержания соединения каждый ход платит новый
TCP+TLS-handshake.

`family: auto | v4 | v6` пинит адресное семейство дайлера. Смысл — в §5.1.

### 3.6 `credential` — что уходит апстриму

Ось выбирает **источник** учётных данных (§1.8):

- **`anonymous`** — положить `Bearer public`, пул не читать. Развёртывание,
  не просившее тратить ключи, их и не тронет.
- **`pool`** — взять ключ из файла пула.

Живое переключение действует со следующей попытки.

#### Ротация ключей

При `credential: pool` работает флаг **`keyRotation`** (`on` | `off`, по
умолчанию `on`) — *(решение)*. Он отвечает на один вопрос:

> Если ключ отвергнут, может ли шлюз взять другой ключ из пула и повторить
> запрос, прежде чем отдать отказ клиенту?

| `keyRotation` | Поведение |
|---|---|
| `on` | ключ отвергнут → следующий ключ из пула, один повтор |
| `off` | ключ отвергнут → отказ уходит клиенту |

Это **единственный** случай, когда шлюз повторяет запрос, и он не противоречит
§2.1: клиент не может сменить ключ, потому что ключей у него нет — они у
шлюза. Со стороны клиента это один запрос, и его политика повторов не
затронута.

Бюджет — **один** повтор *(решение)*. Ключ меняется один раз: если и второй
ключ отвергнут, дело не в ключе, и дальнейшие попытки сожгут пул впустую.

Зачем `off`: он делает расход ключей предсказуемым. При `on` один запрос
может израсходовать два ключа, и по состоянию потом не понять, какой именно
привёл к отказу.

При `credential: anonymous` флаг **не действует**: там ровно один токен
`public`. Значение принимается и игнорируется — запрет сделал бы переключение
источника двушаговым.

---

## 4. Gateway

```
POST /v1/chat/completions
GET  /v1/models
```

`GET /v1/models` отвечается из каталога, без обращения к апстриму.

Поток chat-запроса:

1. Буферизовать тело (≤ 4 MiB).
2. **Разрешить модель** — неизвестный id → ошибка в форме §2.2.
3. **Вывести идентификаторы** — session id по формуле §1.3, message id, affinity.
4. **Собрать промпт** — отрендерить из имён активных инструментов (§1.5).
5. **Выбрать инструменты** — те, что прислал клиент, плюс gate-набор (§1.4).
6. **Клэмпнуть усилие** по лестнице модели (§1.6).
7. **Вывести резерв ответа** (§1.7).
8. **Исполнить запрос** и вести поток (§4.1).

### 4.1 Стриминг

Апстрим всегда стримит *(решение)*: на этом держатся relay-фрейминг и
watchdog'и. Флаг клиента выбирает только локальный приёмник — passthrough SSE
или единое JSON-завершение.

Бюджеты *(решение)*:

| Бюджет | Значение | Что покрывает |
|---|---|---|
| first event | 30s | connect + заголовки ответа |
| idle (chat) | 120s | промежуток после старта потока |
| idle (responses) | 300s | reasoning держит более длинные паузы |

Тайм-аут не является поводом для повтора (§2.1) — он передаётся как отказ.

**Отказ после первого байта, дошедшего клиенту**, не отменяется: строка
статуса зафиксирована, поток просто заканчивается.

### 4.2 Конверт ошибки для клиента

Для собственных ошибок шлюза (§2.4) — форма OpenAI:

```json
{"error": {"message": "…", "type": "…", "code": "…"}}
```

`type` и `code` несут одну строку: у клиента один словарь для ветвления.

Отказы апстрима через этот конверт **не проходят** — они передаются дословно
(§2.2).

---

## 5. Состояние

### 5.1 Что вообще нужно хранить

Пересмотрено с нуля. Шлюз — посредник, поэтому большая часть привычного
состояния ему не нужна:

| Данные | Нужны? | Почему |
|---|---|---|
| Настройки | да | §7 |
| Ключи пула | да, если `credential: pool` | это секреты, они должны быть на диске |
| Суточные счётчики успехов и отказов | да | чтобы ответить «сколько израсходовано» |
| Журнал запросов | да, но как лог | §5.4 |
| Что именно уже израсходовано, чтобы не тратить повторно | **нет** | шлюз не ротирует адрес и не выбирает адрес; повторно расходовать нечего |
| Прошлые списки провайдеров, ротации, идентичности | **нет** | этих механизмов в проекте нет |

### 5.2 Раскладка

Три корня, по природе данных *(решение, по образцу
`packages/util/src/global-roots.ts`)*:

| Корень | Что | Владелец |
|---|---|---|
| `$XDG_CONFIG_HOME/free-ai-router/` | `config.json` | ты; никогда не создаётся машиной, никогда не чистится |
| `$XDG_STATE_HOME/free-ai-router/` | `state.json`, `app.log` | переживает рестарты |
| `$XDG_RUNTIME_DIR/free-ai-router/` | временное | процесс |

Файлов ровно два: конфигурация и состояние. Отдельного файла под журнал
запросов нет — он слит с логом (§5.4).

Пути определяют XDG-переменные, а не конфигурация: иначе была бы циклическая
зависимость — файл, сообщающий, где лежит файл.

### 5.3 `state.json` — счётчики

Один файл, одна структура *(решение)*:

```jsonc
{
  "version": 1,
  "days": {
    "2026-10-17": {
      "requests": { "ok": 41, "refused": 3 },
      "byModel":  { "mimo-v2.6-flash-free": { "ok": 12, "refused": 1 } }
    }
  },
  "keys": {
    "a1b2c3d4": { "ok": 30, "refused": 2, "lastRefusalAt": 1792237794169 }
  }
}
```

**Счёт по семейству адресов, а не по адресу** *(решение; требует проверки —
§11.6)*. Лимитер ключует бакет по строке адреса *(источник: §1.8)*; если он
усекает адрес до префикса, то IPv4 и IPv6 — независимые бакеты, и `family` —
переключатель бакета.

**Точный лимит не угадывается.** Хранится число успехов, наблюдённых до
первого отказа — это **нижняя оценка**, потому что бакет делят другие клиенты.

Окно — 30 дней, обрезается при записи *(решение)*. Тогда «никогда не
удаляется» не значит «неограничен».

Ключи пишутся только отпечатком. Токен `public` — дословно: он опубликован, и
маскировка ничего не скрыла бы.

### 5.4 Лог

`app.log` — единственный журнал *(решение)*. Записи двух видов в одном потоке:
жизненный цикл демона и исход каждого запроса.

Ротация при 8 MiB, 3 поколения, **на старте** — единственный момент, когда
файл перемещается без гонки с собственным писателем. `data clear logs`
обрезает на месте, чтобы живой процесс писал в тот же inode.

### 5.5 Команда `data`

```
free-ai-router data                    что лежит на диске, с размерами
free-ai-router data clear logs         обрезать журнал
free-ai-router data clear stats        стереть счётчики
free-ai-router data clear all          стереть всё, кроме config.json
```

`all` **никогда** не трогает конфигурацию: удаление файла, описывающего
устройство демона, — не обслуживание, а потеря развёртывания.

---

## 6. CLI

```
free-ai-router up       [флаги…] [--detach]         запустить демон
free-ai-router status   [--listen ADDR]             строка: режим, оси, счётчики
free-ai-router show     [--listen ADDR]             полная панель
free-ai-router stop     [--listen ADDR]             корректная остановка
free-ai-router tui      [--tab status|keys|control] терминальный интерфейс (§7)

free-ai-router cred     [anonymous|pool]            источник ключей
free-ai-router rotate   [on|off]                    ротация ключей (только pool)
free-ai-router mode     [local|server]              форма развёртывания

free-ai-router keys     [list|add|delete]           ключи пула
free-ai-router models   [--free] [--json]           каталог (§1.6)
free-ai-router stats | logs [-f] [pattern] | raw <route>
free-ai-router axes                                 словари, как JSON
free-ai-router data     [clear logs|stats|all]      состояние на диске (§5.5)
free-ai-router config   [--print]                   эффективная конфигурация (§8.5)
```

`cred`, `rotate` и `mode` трогают **одну** настройку и переносят остальные,
поэтому ни одна не сбрасывает соседа. В частности `cred pool` не выставляет и
не сбрасывает `rotate`.

`rotate` без аргумента печатает значение. При `anonymous` команда объясняет,
что ротировать нечего, а не принимает молча бессмысленный флаг:

```
$ free-ai-router rotate on
rotate: n/a — credential is anonymous; there is exactly one token to send.
        Switch to `cred pool` first.
```

### `axes` — один источник словарей

```
$ free-ai-router axes
{"credential":["anonymous","pool"], "mode":["local","server"],
 "toggle":["on","off"], "family":["auto","v4","v6"], "scope":["logs","stats","all"]}
```

TUI строит пикеры отсюда. Копия списка расходится с бинарём молча.

### Доступ к удалённому демону

```sh
free-ai-router status --listen server.example:8787 --token "$TOKEN"
export ZEN_ROUTER_TOKEN=…
```

`--token` никогда не пишется на диск: это bearer на запрос.

### Отладка

Переопределение настроек — флагами:

```sh
free-ai-router up --credential pool --family v6 --listen 127.0.0.1:9000
free-ai-router status --listen 127.0.0.1:9000
```

Изолированное состояние — `ZEN_ROUTER_STATE` — один каталог.

---

## 7. TUI

```sh
free-ai-router tui
free-ai-router tui --tab status
```

TUI встроен в бинарь, на Bubble Tea. Вся функциональность проекта — на Go,
одним статическим бинарём без внешних программ.

### Экраны

Спокойный интерфейс: три экрана, без вложенных меню и анимаций.

| Экран | Показывает |
|---|---|
| **status** | демон, режим, listen, оси, счётчики, сводка по моделям |
| **keys** | ключи: отпечатки, счётчики, время отказа; добавление и удаление |
| **control** | строка на настройку — credential, key rotation, режим |

Строка **key rotation** активна только при `credential: pool`. При анонимном
источнике она видна, неактивна и несёт объяснение: пикер, который ничего не
переключает, читается как сломанный.

### Клавиши

| Клавиша | Действие |
|---|---|
| `Tab` / `Shift+Tab` | следующий / предыдущий экран |
| `↑` / `↓` | перемещение по строкам |
| `Enter` | открыть пикер / выполнить |
| `Esc` | закрыть пикер, отменить |
| `r` | перечитать с демона |
| `q` | выход |

Неназначенная клавиша не делает ничего: не перерисовывает, не мерцает.

### Свойства

- **Не переопределяет TTY.** `tea.WithAltScreen` не включается — TUI
  сосуществует с терминалом, вывод остаётся копируемым.
- **Не держит состояние.** Каждый экран читает демон; мутации идут через тот
  же код, что у CLI.
- **Ничего не требует сверх бинаря.**

---

## 8. Конфигурация

Три уровня, порядок старшинства — часть контракта:

```
флаги CLI  ────────────────▶  побеждают всё
переменные ZEN_ROUTER_* ───▶  перекрывают файл
config.json ───────────────▶  базовый слой
```

### 8.1 На NixOS

Конфигурацией владеет модуль Nix: он генерирует `config.json` при активации.
Пересборка = декларация.

```nix
services.free-ai-router = {
  enable = true;
  mode = "local";
  credential = "pool";
  keyRotation = true;
  family = "auto";
};
```

Ручная правка `config.json` на таком хосте переживёт запуск, но не пересборку —
и это ожидаемо: декларация побеждает при switch, между switch-ами состояние
принадлежит оператору.

### 8.2 На другой системе

NixOS не требуется. Бинарь статический (`CGO_ENABLED=0`), на любом
дистрибутиве работает сам. Здесь `config.json` — обычный файл, никто его не
перегенерирует, все три уровня доступны полностью.

### 8.3 Файл

```jsonc
{
  // --- mode -------------------------------------------------------------
  "mode": "local",                  // local | server
  "listen": "127.0.0.1:8787",
  "token": "",                      // обязателен при mode = server

  // --- адресация --------------------------------------------------------
  "family": "auto",                 // auto | v4 | v6

  // --- credential -------------------------------------------------------
  "credential": "anonymous",        // anonymous | pool
  "keyPoolFile": "",                // "" = <config-dir>/pool-config.json
  "keyRotation": true,              // только при pool; иначе игнорируется

  // --- апстрим и тайминги -----------------------------------------------
  "upstream": "https://opencode.ai",
  "firstEventTimeout": "30s",
  "idleTimeout": "120s",
  "responsesIdleTimeout": "300s"
}
```

### 8.4 Окружение

```
ZEN_ROUTER_MODE, ZEN_ROUTER_LISTEN, ZEN_ROUTER_TOKEN,
ZEN_ROUTER_CREDENTIAL, ZEN_ROUTER_ROTATE, ZEN_ROUTER_FAMILY,
ZEN_ROUTER_UPSTREAM, ZEN_ROUTER_KEY_POOL_FILE
```

`ZEN_ROUTER_STATE` переопределяет файл состояния.

### 8.5 Флаги и `--print-config`

У каждой настройки есть флаг — уровень для отладки. Он позволяет поднять демон
с непривычной настройкой, не трогая ни файл, ни окружение.

`--print-config` печатает **эффективную** конфигурацию с указанием уровня,
выигравшего каждое значение. Без этого отладка слияния — угадывание:

```
mode        local      (flag)
listen      127.0.0.1:9000  (flag)
credential  pool       (config.json)
family      auto       (default)
```

---

## 9. Control API

На том же слушателе, под `/_zenctl/`:

| Метод | Маршрут | Назначение |
|---|---|---|
| GET | `status` | режим, оси, uptime, счётчики |
| GET | `stats` | сводки по дням и моделям |
| GET | `catalog` | каталог моделей |
| GET | `keys` | отпечатки ключей |
| POST | `keys`, `keys/delete` | добавить / удалить ключ |
| GET/POST | `credential` | источник ключей |
| GET/POST | `rotate` | ротация |
| GET/POST | `mode` | форма развёртывания |
| POST | `stop` | завершение |

`credential` и `rotate` разделены: источник ключей и разрешение их тратить —
разные решения. Запрос, не упоминающий поле, **сохраняет** текущее значение, а
не сбрасывает в умолчание.

POST `rotate` при `anonymous` отвечает `200` с `"rotation": null` — `null`, а
не `false`, чтобы отличить «выключено» от «не применяется».

Всё остальное под этим префиксом и не под `/v1/` — `404`.

---

## 10. Раскладка

```
cmd/free-ai-router/    точка входа: диспетчер команд, разбор флагов,
                       сборка демона, команды чтения, TUI

internal/wire/         общий контракт, не привязанный к провайдеру:
                       идентификаторы сессии и сообщения, нейтральная форма

internal/provider/     ЧТО за сервис
  provider.go            интерфейс: модели, сборка запроса, разбор отказа
  opencode/              контракт OpenCode Zen (заголовки, инструменты,
                         промпт, каталог моделей)
  openai/                шаблон для OpenAI-совместимых сервисов

internal/capability/   КАК обходить
  capability.go          реестр механизмов
  keyrotation.go         ротация ключей

internal/transport/    КУДА уходит трафик
  transport.go           интерфейс и реестр
  direct.go              прямая маршрутизация с тёплым пулом

internal/catalog/      связка конфигурации и провайдеров
internal/gateway/      поверхность OpenAI, поток, передача отказа
internal/control/      поверхность управления
internal/state/        счётчики и журнал
internal/config/       XDG-пути, слияние трёх уровней, источники значений
internal/tui/          Bubble Tea: состояние / ключи / настройки
```

Разделение `provider` / `capability` / `transport` — не формальность:
оно позволяет добавлять сервисы и механизмы независимо друг от друга.

---

## 11. Открытые вопросы

Каждый требует живого замера. Метод: взять захваченный запрос с известным
`200`, менять **одно поле за раз**, всегда держать контроль, который обязан
вернуть `200`. Если отвергнут контроль — эксперимент недействителен.

1. **Принимает ли гейт записи `shell`/`read`** такими, как их шлёт клиент
   2.0.30. Гейт отвечает на записи целиком, значит имя, описание и схема —
   часть условия.
2. **Как гейт относится к `tool_choice`.** Клиент шлёт `"none"` при
   достижении лимита шагов *(источник:
   `packages/core/src/session/runner/llm.ts`)*.
3. **Что именно отвергается в промпте генератора заголовков** *(источник:
   `packages/core/src/plugin/agent.ts`)*.
4. **Расхождение `x-opencode-session` и `x-opencode-session-id`** — проверяет
   ли гейт их порознь или сверяет.
5. **Правило для `x-opencode-project`** — `global` вне чекаута, `prj_<hex>`
   внутри.
6. **Форма усечения IP у лимитера** — от неё зависит, является ли `family`
   переключателем бакета (§5.3).
7. **Форма `User-Agent`**, которую принимает гейт.

---

## 12. Сборка и тесты

Go 1.26. Внешняя зависимость ровно одна — Bubble Tea для TUI (§7). Всё
остальное без зависимостей: демон ретранслирует HTTP, и дерево зависимостей на
этом пути придётся патчить ради нескольких счётчиков.

```sh
CGO_ENABLED=0 go build ./...
CGO_ENABLED=0 go vet ./...
go test ./...
```

Тесты герметичны: подставные апстримы через `httptest`, только loopback, без
живой сети.

### Проверка контракта

1. **Записанные обмены** — ответы сервиса, сохранённые живым запросом и
   сверяемые по байтам. Лежат в `testdata/`, происхождение описано рядом
   с ними. Отвечают на вопрос, который нельзя вывести из кода: какова форма
   реального отказа. Тесты на них проверяют, что шлюз передаёт отказ клиенту
   дословно, и что признаки, по которым клиент принимает решение, доживают
   до него.
2. **Свойства** — проверяются без сети:
   - id по формуле §1.3 декодируется обратно в момент времени;
   - промпт §1.5 есть чистая функция от списка имён инструментов;
   - `x-opencode-session` и `x-opencode-session-id` расходятся тогда и только
     тогда, когда у сессии есть родитель;
   - заголовки родителя присутствуют тогда и только тогда, когда есть родитель;
   - тело содержит ровно тот gate-набор, что задан;
   - **отказ апстрима доходит до клиента побайтово** — статус, тело и
     `retry-after` совпадают с тем, что вернул подставной апстрим (§2.2).
3. **Живой A/B** — вручную, никогда в CI. Меняет одно поле за раз, всегда
   держит контроль и отказывается выдавать результат, когда отвергнут
   контроль.
