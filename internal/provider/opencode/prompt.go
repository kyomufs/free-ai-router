package opencode

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"free-ai-router/internal/provider"
)

// defaultSystemPrompt is the chat system prompt of the reference client.
//
// SOURCE: opencode v1.19.3, packages/opencode/src/session/prompt/default.txt.
//
// MEASURED on 2026-10-25 against the free lane, with the mandatory tools, a
// canonical session id and "stream": true held constant:
//
//	this text + the environment block below -> 200
//	this text alone                          -> 429 FreeUsageLimitError
//	the prompt this package used to render   -> 429 FreeUsageLimitError
//	this text + the block + client rules     -> 200
//
// The lane therefore recognises the client by the canonical prompt AND by the
// environment block that follows it. The block's VALUES are not checked: a
// different working directory and date kept the 200. The block's shape is.
//
//go:embed prompt/default.txt
var defaultSystemPrompt string

// systemPrompt assembles the system message for a request.
//
// The client's own system part is kept, but AFTER the canonical text and the
// environment block. Dropping it would lose the harness's rules; putting it
// before the block was admitted too, but the block is part of the client's own
// text, so the canonical form stays intact and the client's rules follow it.
func (p *Provider) systemPrompt(model provider.Model, client string) string {
	var b strings.Builder
	b.WriteString(defaultSystemPrompt)

	fmt.Fprintf(&b, "\nYou are powered by the model named %s. The exact model ID is %s/%s\n", model.ID, p.id, model.ID)
	b.WriteString("Here is some useful information about the environment you are running in:\n<env>\n")

	dir, err := os.Getwd()
	if err != nil || dir == "" {
		dir = "/"
	}
	fmt.Fprintf(&b, "  Working directory: %s\n", dir)
	fmt.Fprintf(&b, "  Workspace root folder: %s\n", dir)
	fmt.Fprintf(&b, "  Is directory a git repo: %s\n", yesNo(isGitRepo(dir)))
	fmt.Fprintf(&b, "  Platform: %s\n", runtime.GOOS)
	fmt.Fprintf(&b, "  Today's date: %s\n", time.Now().Format("Mon Jan 02 2006"))
	b.WriteString("</env>")

	if client != "" {
		b.WriteString("\n\n")
		b.WriteString(client)
	}
	return b.String()
}

// isGitRepo reports whether dir is the root of a git worktree.
//
// Only the value in the environment block is affected, and the lane does not
// check it. The answer is still measured rather than invented, because the
// model reads the same block.
func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
