package opencode

import (
	"strings"
	"time"

	"free-ai-router/internal/provider"
)

// jsonSchemaDialect — диалект схемы, который клиент объявляет в описаниях
// инструментов. Часть проверки, поэтому закреплён.
const jsonSchemaDialect = "https://json-schema.org/draft/2020-12/schema"

// DefaultUserAgent — строка User-Agent.
//
// Клиент строит её как `opencode/${channel}/${version}/${name}`
// (packages/core/src/app.ts). Значения по умолчанию для частей — "opencode",
// "unknown", "unknown", поэтому у произвольной сборки получается
// `opencode/unknown/unknown/opencode`.
const DefaultUserAgent = "opencode/unknown/unknown/opencode"

// Бюджеты потоков. Числа относятся к контракту, а не к настройкам: клиент
// держит собственные тайм-ауты, и несовпадение привело бы к тому, что шлюз
// рвал бы соединение раньше клиента.
const (
	// firstEventTimeout — бюджет до первых заголовков ответа.
	firstEventTimeout = 30 * time.Second
	// chatIdleTimeout — бюджет молчания обычного потока.
	chatIdleTimeout = 120 * time.Second
	// reasoningIdleTimeout — бюджет молчания для моделей, умеющих
	// рассуждать: они держат паузы заметно дольше.
	reasoningIdleTimeout = 300 * time.Second
)

// idleTimeoutFor выбирает бюджет молчания по возможностям модели.
func idleTimeoutFor(m provider.Model) time.Duration {
	if m.Reasoning {
		return reasoningIdleTimeout
	}
	return chatIdleTimeout
}

// Описания полей инструментов. Вынесены в константы, потому что они часть
// проверки: сервис сверяет текст, а не намерение.
const (
	workdirDescription = "Working directory to execute the command in. " +
		"Defaults to the current working directory. When possible, avoid changing " +
		"directories in the command and set the working directory here instead."

	timeoutDescription = "Timeout in milliseconds. Set to 0 to disable the timeout. " +
		"Defaults to 120000 for foreground commands. Background commands have no timeout by default."

	backgroundDescription = "Run the command in the background and return immediately " +
		"(useful for dev servers and long-running builds). You do not need to use '&' at the end " +
		"of the command when using this parameter. You will be notified when it completes. " +
		"DO NOT poll for completion."

	readDescription = "Read the contents of a file or directory. Supports text files, images, " +
		"and PDFs. Images and PDFs are presented directly to the model. Each text line is prefixed " +
		"by its 1-based line number as <line>: <content>. The prefix is for reference and is not part " +
		"of the file content. Directory entries are returned one per line. Use offset and limit to read " +
		"large files or directories in sections. Prefer one larger read over many small slices, and use " +
		"grep to find specific content in large files."
)

// shellDescriptionTemplate — описание инструмента shell с подстановкой системы
// и оболочки. %s — название системы, %s — имя оболочки.
const shellDescriptionTemplate = "Execute a shell command and return its output. " +
	"Commands run on %s using %s. " +
	"Quote file paths containing spaces or special characters. " +
	"Prefer dedicated tools over shell commands when possible. " +
	"When output is large, the full result is saved to a file and a truncated preview is returned. " +
	"Rely on automatic truncation unless filtering the output is more useful. " +
	"Commands accept an optional timeout, background commands have no timeout by default. " +
	"Background commands return immediately, and you will be notified when they complete."

// systemPromptTemplate — шаблон системного промпта с подстановкой указаний по
// инструментам.
//
// Промпт РЕНДЕРИТСЯ, а не берётся готовым: клиент собирает его из имён активных
// инструментов (packages/core/src/session/system-prompt.ts), и вставка зависит
// от того, что доступно. Проброс клиентского промпта как есть дал бы другую
// форму, потому что у клиента другой набор инструментов.
const systemPromptTemplate = "You are an AI agent running in OpenCode, a coding agent harness. " +
	"Help the user accomplish their goals using the tools you have available.\n" +
	"\n" +
	"# Harness\n" +
	"- Responses are rendered as GitHub-flavored Markdown.\n" +
	"- `<system-reminder>` blocks are harness instructions, not user-authored content. Read and follow them.\n" +
	"- Prefer parallelizing independent tool calls.\n" +
	"${OPENCODE_TOOL_GUIDANCE}\n" +
	"\n" +
	"# Communication\n" +
	"- Use clear file paths when referring to files.\n" +
	"- Keep responses clear and concise, and avoid unnecessary technical jargon.\n" +
	"\n" +
	"# Working in codebases\n" +
	"- Keep changes consistent with the structure, naming, style, and patterns of the surrounding code.\n" +
	"- Treat unfamiliar files or changes as potential user work and investigate before deleting or overwriting them.\n"

// guidanceShell — указания, добавляемые когда доступен инструмент shell.
const guidanceShell = "- Prefer dedicated tools over shell commands; fall back to the shell when a tool cannot do what you need.\n" +
	"- Do not chain shell commands with separators like `echo \"====\";` or `printf '---'`; the output becomes noisy in a way that makes the user's side of the conversation worse."

// guidanceWrite — указания для инструмента write.
const guidanceWrite = "- Use the write tool to create files or completely replace their content. " +
	"Prefer using the edit tool for targeted changes."

// guidanceEdit — указания для инструмента edit.
const guidanceEdit = "- Use the edit tool for targeted changes to existing text files. " +
	"It replaces the exact text in `oldString` with `newString`, and the values must differ. " +
	"By default, `oldString` must occur exactly once. If it occurs multiple times, include more " +
	"surrounding context to make it unique or set `replaceAll` to true to replace every occurrence."

// RenderSystemPrompt собирает системный промпт из имён активных инструментов.
//
// Функция экспортирована, потому что её результат — проверяемое свойство:
// промпт есть чистая функция от списка имён, и это тестируется без сети.
//
// Указания добавляются по тем же трём инструментам, что и у клиента: shell,
// write, edit. Порядок вставки совпадает с порядком проверок в
// system-prompt.ts.
func RenderSystemPrompt(toolNames []string) string {
	has := make(map[string]bool, len(toolNames))
	for _, name := range toolNames {
		has[name] = true
	}

	var guidance []string
	if has["shell"] {
		guidance = append(guidance, guidanceShell)
	}
	if has["write"] {
		guidance = append(guidance, guidanceWrite)
	}
	if has["edit"] {
		guidance = append(guidance, guidanceEdit)
	}

	return strings.Replace(systemPromptTemplate, "${OPENCODE_TOOL_GUIDANCE}", strings.Join(guidance, "\n"), 1)
}
