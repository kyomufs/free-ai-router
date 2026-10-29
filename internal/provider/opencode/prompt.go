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
// SOURCE: opencode 2.x, packages/core/src/session/runner/prompt/system.txt.
//
// The text used to be v1.19.3's default.txt. Measured on 2026-10-29 by running
// the installed v2.1.2 client through a TLS-terminating proxy: every request it
// sends is assembled from this text, a model block, the client's tool guidance
// and the environment block. The v1 text was still admitted on that date, but
// it is a v1 marker, and the lane routes what it reads as v1 traffic into the
// exhausted legacy bucket — the exact refusal this emulation exists to avoid.
//
// The lane recognises the client by the canonical prompt AND by the
// environment block (measured on 2026-10-25: canonical text alone -> 429
// FreeUsageLimitError, canonical text plus the block -> 200). The values in the
// block are not checked; its shape is.
//
//go:embed prompt/default.txt
var defaultSystemPrompt string

// systemPrompt assembles the system message for a request.
//
// The ORDER is the v2 client's (captured, not guessed): canonical text, the
// model block, the client's own rules (the analogue of the client's tool
// guidance), then the date and the environment block last. Dropping the
// client's part would lose the harness's rules; keeping it before the
// environment block preserves the block as the tail of the message, which is
// where the reference client puts it.
func (p *Provider) systemPrompt(model provider.Model, client string) string {
	var b strings.Builder
	b.WriteString(defaultSystemPrompt)

	name := model.Name
	if name == "" {
		name = model.ID
	}
	b.WriteString("\n# Your Model\n")
	fmt.Fprintf(&b, "- Name: %s\n", name)
	fmt.Fprintf(&b, "- Provider ID: %s\n", p.id)
	fmt.Fprintf(&b, "- Model ID: %s\n", model.ID)

	if client != "" {
		b.WriteString("\n")
		b.WriteString(client)
	}

	dir, err := os.Getwd()
	if err != nil || dir == "" {
		dir = "/"
	}
	fmt.Fprintf(&b, "\n\nToday's date: %s\n", time.Now().Format("Mon Jan 02 2006"))
	b.WriteString("\nHere is some useful information about the environment you are running in:\n<env>\n")
	fmt.Fprintf(&b, "  Working directory: %s\n", dir)
	fmt.Fprintf(&b, "  Workspace root folder: %s\n", dir)
	fmt.Fprintf(&b, "  Is directory a git repo: %s\n", yesNo(isGitRepo(dir)))
	fmt.Fprintf(&b, "  Platform: %s\n", runtime.GOOS)
	b.WriteString("</env>")
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
