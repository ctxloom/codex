package codex

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/shared/agent"
)

// codexManifest tracks which prompt files ctxloom wrote, so cleanup removes only
// ctxloom-managed prompts and leaves the user's own untouched (the prompts dir
// is shared).
const codexManifest = ".ctxloom-manifest"

// codexPromptsDir resolves Codex's custom-prompts directory. NOTE: unlike
// claude/gemini (project-scoped command dirs), Codex only discovers prompts from
// the GLOBAL, top-level $CODEX_HOME/prompts (default ~/.codex/prompts) — there is
// no project-level prompts dir. So codex slash commands are global: a session's
// Setup rewrites them, and the ctxloom manifest scopes cleanup to ctxloom's own
// files. Resolution mirrors the session-history dir: $CODEX_HOME, else ~/.codex.
func codexPromptsDir() string {
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return filepath.Join(home, "prompts")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".codex", "prompts")
	}
	return filepath.Join(".codex", "prompts") // last-resort relative
}

// WriteCommandFiles generates Codex custom-prompt files from exported prompts.
// Files are written flat to the global $CODEX_HOME/prompts (e.g. save.md ->
// /save), since Codex scans only top-level markdown there. workDir is unused
// (codex prompts are global, not project-scoped — see codexPromptsDir). ctxloom
// tracks the files it manages via a manifest so it can clean up stale prompts
// without touching the user's own. Only exports with Enabled == true are written.
func WriteCommandFiles(workDir string, cmds []agent.CommandExport, opts ...agent.CommandFileOption) error {
	fs := agent.ResolveCommandFS(opts...)

	promptsDir := codexPromptsDir()
	manifestPath := filepath.Join(promptsDir, codexManifest)

	cleanupTrackedPrompts(fs, promptsDir, manifestPath)

	if !hasExportableCommands(cmds) {
		_ = fs.Remove(manifestPath)
		return nil
	}

	if err := fs.MkdirAll(promptsDir, 0755); err != nil {
		return fmt.Errorf("create prompts dir: %w", err)
	}

	manifest, err := writeCodexPrompts(fs, promptsDir, cmds)
	if err != nil {
		return err
	}

	if err := afero.WriteFile(fs, manifestPath, []byte(strings.Join(manifest, "\n")), 0644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}

// cleanupTrackedPrompts removes every prompt file recorded in the previous
// run's manifest.
func cleanupTrackedPrompts(fs afero.Fs, promptsDir, manifestPath string) {
	data, err := afero.ReadFile(fs, manifestPath)
	if err != nil {
		return
	}
	for _, name := range strings.Split(string(data), "\n") {
		if name = strings.TrimSpace(name); name != "" {
			_ = fs.Remove(filepath.Join(promptsDir, name))
		}
	}
}

// hasExportableCommands reports whether any export is enabled.
func hasExportableCommands(cmds []agent.CommandExport) bool {
	for _, c := range cmds {
		if c.Enabled {
			return true
		}
	}
	return false
}

// writeCodexPrompts writes each enabled export as a Codex prompt file, returning
// the manifest of written filenames.
func writeCodexPrompts(fs afero.Fs, promptsDir string, cmds []agent.CommandExport) ([]string, error) {
	var manifest []string
	for _, c := range cmds {
		if !c.Enabled {
			continue
		}
		md := TransformToCodexPrompt(c)
		filename := strings.ReplaceAll(c.Name, "/", "-") + ".md"
		if err := afero.WriteFile(fs, filepath.Join(promptsDir, filename), []byte(md), 0644); err != nil {
			return nil, fmt.Errorf("write prompt %s: %w", c.Name, err)
		}
		manifest = append(manifest, filename)
	}
	return manifest, nil
}

// TransformToCodexPrompt converts a command export to a Codex prompt: optional
// YAML frontmatter (description + argument-hint, the keys Codex supports) plus a
// markdown body with {{var}} transformed to positional $N arguments (Codex's
// prompt argument syntax). Codex does not support allowed-tools/model frontmatter,
// so those export fields are dropped.
func TransformToCodexPrompt(c agent.CommandExport) string {
	var buf bytes.Buffer

	if c.Description != "" || c.ArgumentHint != "" {
		buf.WriteString("---\n")
		if c.Description != "" {
			buf.WriteString("description: ")
			buf.WriteString(escapeYAMLString(c.Description))
			buf.WriteString("\n")
		}
		if c.ArgumentHint != "" {
			buf.WriteString("argument-hint: ")
			buf.WriteString(c.ArgumentHint)
			buf.WriteString("\n")
		}
		buf.WriteString("---\n\n")
	}

	buf.WriteString(transformMustacheToPositional(c.Content))
	return buf.String()
}

// escapeYAMLString quotes a string for safe inclusion in YAML frontmatter when it
// contains special characters.
func escapeYAMLString(s string) string {
	needsQuotes := strings.ContainsAny(s, ":#{}[]&*!|>'\"%@`") ||
		strings.HasPrefix(s, " ") ||
		strings.HasSuffix(s, " ") ||
		strings.Contains(s, "\n")
	if needsQuotes {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

// transformMustacheToPositional replaces {{variable}} patterns with $1, $2, etc.
// Variables are assigned positions by first occurrence order.
func transformMustacheToPositional(content string) string {
	varNum := 1
	seen := make(map[string]int)
	re := regexp.MustCompile(`\{\{(\w+)\}\}`)

	return re.ReplaceAllStringFunc(content, func(match string) string {
		varName := re.FindStringSubmatch(match)[1]
		if num, exists := seen[varName]; exists {
			return fmt.Sprintf("$%d", num)
		}
		seen[varName] = varNum
		num := varNum
		varNum++
		return fmt.Sprintf("$%d", num)
	})
}
