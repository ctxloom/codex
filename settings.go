// Package codex is ctxloom's OpenAI Codex CLI agent: the settings/hooks writer
// (agent.SettingsWriter) and the launch backend (agent.Backend), mirroring the
// claude and gemini agent modules. Codex reads project-scoped config from
// .codex/config.toml in a trusted project; ctxloom manages the [hooks] and
// [mcp_servers] tables there and preserves every other key the user set.
//
// ┌─────────────────────────────────────────────────────────────────────────┐
// │ WARNING — IMPLEMENTED AGAINST DOCS, ENTIRELY UNTESTED.                     │
// │                                                                           │
// │ Every codex-specific detail in this module — the config.toml [hooks] and  │
// │ [mcp_servers] format, the `codex exec` + --sandbox/--ask-for-approval     │
// │ flags, the global ~/.codex/prompts location and frontmatter, and the      │
// │ hook stdin fields used for session registration — is derived solely from  │
// │ the published OpenAI Codex CLI documentation. None of it has been run      │
// │ against a real codex binary: the maintainer's Linux development platform   │
// │ has no codex access. Unit tests cover the Go logic, NOT codex's actual     │
// │ acceptance of what we emit.                                               │
// │                                                                           │
// │ Treat every format/flag here as PROVISIONAL until smoke-tested on a       │
// │ machine with codex installed (write hooks/MCP, launch, confirm the hooks  │
// │ fire and the MCP server connects, run a oneshot/distill).                 │
// └─────────────────────────────────────────────────────────────────────────┘
package codex

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/afero"

	"github.com/ctxloom/shared/agent"
	"github.com/ctxloom/shared/wire"
)

// NewWriter constructs the Codex CLI settings writer.
func NewWriter(o agent.SettingsOptions) agent.SettingsWriter {
	return &CodexHookWriter{FS: o.FS}
}

// CodexHookWriter writes hooks and MCP servers to Codex's project-level
// .codex/config.toml. The file is TOML; ctxloom owns the [hooks] and
// [mcp_servers] tables and round-trips every other top-level key untouched.
type CodexHookWriter struct {
	// FS is the filesystem to use. If nil, the real OS filesystem is used.
	FS afero.Fs
}

func (w *CodexHookWriter) getFS() afero.Fs { return agent.GetFS(w.FS) }

// HooksPath returns the path to Codex's project-level config.toml.
func (w *CodexHookWriter) HooksPath(projectDir string) string {
	return filepath.Join(projectDir, ".codex", "config.toml")
}

// SettingsPath returns the path to Codex's config.toml.
func (w *CodexHookWriter) SettingsPath(projectDir string) string { return w.HooksPath(projectDir) }

// WriteSettings implements SettingsWriter for Codex CLI. Hooks and MCP servers
// are written to .codex/config.toml as the [hooks] and [mcp_servers] tables.
func (w *CodexHookWriter) WriteSettings(hooks *wire.HooksConfig, mcp *wire.MCPConfig, bundleMCP map[string]wire.MCPServer, projectDir string) error {
	if hooks == nil {
		hooks = &wire.HooksConfig{}
	}

	fs := w.getFS()
	settingsPath := w.SettingsPath(projectDir)

	if err := fs.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		return fmt.Errorf("failed to create .codex directory: %w", err)
	}

	cfg, err := w.load(settingsPath)
	if err != nil {
		return fmt.Errorf("failed to load existing config.toml: %w", err)
	}

	removeManagedHooks(cfg)
	removeManagedMCP(cfg)

	addUnifiedHooks(cfg, hooks.Unified)
	if backendHooks, ok := hooks.Plugins["codex"]; ok {
		addBackendHooks(cfg, backendHooks)
	}
	addMCPServers(cfg, mcp, bundleMCP)

	return w.save(settingsPath, cfg)
}

// WriteHooks implements HookWriter for Codex CLI (backwards compatible).
func (w *CodexHookWriter) WriteHooks(cfg *wire.HooksConfig, projectDir string) error {
	return w.WriteSettings(cfg, nil, nil, projectDir)
}

// load parses config.toml into a generic table, preserving every key. It is
// fault-tolerant: an unparseable file warns and yields an empty table so
// ctxloom never blocks startup on a schema change.
func (w *CodexHookWriter) load(path string) (map[string]any, error) {
	cfg := map[string]any{}
	data, err := afero.ReadFile(w.getFS(), path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		agent.Warn("failed to parse .codex/config.toml: %v - ctxloom settings will be added but existing config may not be preserved", err)
		return map[string]any{}, nil
	}
	return cfg, nil
}

// save marshals the table back to config.toml via an atomic write+backup.
func (w *CodexHookWriter) save(path string, cfg map[string]any) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return fmt.Errorf("failed to marshal config.toml: %w", err)
	}
	return agent.AtomicWriteFile(w.getFS(), path, buf.Bytes(), "config.toml")
}

// RemoveSettings implements SettingsWriter for Codex CLI: it strips ctxloom
// hooks and MCP servers from config.toml, leaving an absent file absent.
func (w *CodexHookWriter) RemoveSettings(projectDir string) error {
	fs := w.getFS()
	settingsPath := w.SettingsPath(projectDir)
	if exists, _ := afero.Exists(fs, settingsPath); !exists {
		return nil
	}
	cfg, err := w.load(settingsPath)
	if err != nil {
		return fmt.Errorf("failed to load existing config.toml: %w", err)
	}
	removeManagedHooks(cfg)
	removeManagedMCP(cfg)
	return w.save(settingsPath, cfg)
}

// Status implements SettingsWriter for Codex CLI.
func (w *CodexHookWriter) Status(projectDir string) (agent.SettingsStatus, error) {
	fs := w.getFS()
	var status agent.SettingsStatus
	settingsPath := w.SettingsPath(projectDir)
	if exists, _ := afero.Exists(fs, settingsPath); !exists {
		return status, nil
	}
	status.SettingsExists = true
	cfg, err := w.load(settingsPath)
	if err != nil {
		return status, fmt.Errorf("failed to load existing config.toml: %w", err)
	}
	status.HooksPresent = hasManagedHook(cfg)
	if servers := asMap(cfg["mcp_servers"]); servers != nil {
		for name, s := range servers {
			if name == agent.MCPServerName || isManagedServer(asMap(s)) {
				status.MCPPresent = true
				break
			}
		}
	}
	return status, nil
}

// --- generic TOML table helpers -------------------------------------------

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }
func asSlice(v any) []any        { s, _ := v.([]any); return s }

// isManagedServer reports whether an mcp_servers entry was installed by ctxloom,
// recognized by its command's executable token (matches bare/path/quoted forms).
func isManagedServer(server map[string]any) bool {
	cmd, _ := server["command"].(string)
	return agent.IsManaged(cmd, "ctxloom")
}

// --- hook removal / addition ----------------------------------------------

// removeManagedHooks drops ctxloom-managed entries from every [hooks.EVENT]
// group, descending Codex's array-of-tables shape (event -> groups -> hooks[]).
// A hook entry is ctxloom-managed when its command's executable token is
// ctxloom. Groups left empty are dropped; events left empty are removed.
func removeManagedHooks(cfg map[string]any) {
	hooks := asMap(cfg["hooks"])
	if hooks == nil {
		return
	}
	for event, groupsRaw := range hooks {
		var keptGroups []any
		for _, g := range asSlice(groupsRaw) {
			gm := asMap(g)
			if gm == nil {
				keptGroups = append(keptGroups, g)
				continue
			}
			var keptEntries []any
			for _, e := range asSlice(gm["hooks"]) {
				em := asMap(e)
				cmd, _ := em["command"].(string)
				if agent.IsManaged(cmd, "ctxloom") {
					continue // ctxloom-managed — drop
				}
				keptEntries = append(keptEntries, e)
			}
			if len(keptEntries) > 0 {
				gm["hooks"] = keptEntries
				keptGroups = append(keptGroups, gm)
			}
		}
		if len(keptGroups) > 0 {
			hooks[event] = keptGroups
		} else {
			delete(hooks, event)
		}
	}
	if len(hooks) == 0 {
		delete(cfg, "hooks")
	}
}

// hasManagedHook reports whether any configured hook is ctxloom-managed.
func hasManagedHook(cfg map[string]any) bool {
	for _, groupsRaw := range asMap(cfg["hooks"]) {
		for _, g := range asSlice(groupsRaw) {
			for _, e := range asSlice(asMap(g)["hooks"]) {
				cmd, _ := asMap(e)["command"].(string)
				if agent.IsManaged(cmd, "ctxloom") {
					return true
				}
			}
		}
	}
	return false
}

// addUnifiedHooks translates unified hooks to Codex event names and adds them.
// Codex lacks a SessionEnd event, so unified SessionEnd hooks are not emitted.
func addUnifiedHooks(cfg map[string]any, u wire.UnifiedHooks) {
	for _, h := range u.SessionStart {
		addHook(cfg, "SessionStart", h)
	}
	for _, h := range u.PreTool {
		addHook(cfg, "PreToolUse", h)
	}
	for _, h := range u.PostTool {
		addHook(cfg, "PostToolUse", h)
	}
	for _, h := range u.PreShell {
		hook := h
		if hook.Matcher == "" {
			hook.Matcher = "Bash"
		}
		addHook(cfg, "PreToolUse", hook)
	}
	for _, h := range u.PostFileEdit {
		hook := h
		if hook.Matcher == "" {
			hook.Matcher = "Edit|Write"
		}
		addHook(cfg, "PostToolUse", hook)
	}
}

// addBackendHooks adds backend-specific passthrough hooks (already keyed by
// Codex-native event names).
func addBackendHooks(cfg map[string]any, backendHooks wire.BackendHooks) {
	for eventName, hooks := range backendHooks {
		for _, h := range hooks {
			addHook(cfg, eventName, h)
		}
	}
}

// addHook appends one command hook to the named event as its own matcher group,
// emitting Codex's [[hooks.EVENT]] / [[hooks.EVENT.hooks]] shape. Timeout is
// carried in seconds (Codex's unit); zero is omitted so Codex applies its default.
func addHook(cfg map[string]any, eventName string, h wire.Hook) {
	hooks := asMap(cfg["hooks"])
	if hooks == nil {
		hooks = map[string]any{}
		cfg["hooks"] = hooks
	}

	entry := map[string]any{"type": "command", "command": h.Command}
	if h.Timeout > 0 {
		entry["timeout"] = h.Timeout
	}

	group := map[string]any{"hooks": []any{entry}}
	if h.Matcher != "" {
		group["matcher"] = h.Matcher
	}
	hooks[eventName] = append(asSlice(hooks[eventName]), group)
}

// --- MCP removal / addition -----------------------------------------------

// removeManagedMCP drops ctxloom-managed servers from [mcp_servers]: the
// well-known "ctxloom" auto-server and any entry whose command resolves to
// ctxloom. Bundle/unified servers are re-added by name (the map overwrites).
func removeManagedMCP(cfg map[string]any) {
	servers := asMap(cfg["mcp_servers"])
	if servers == nil {
		return
	}
	for name, s := range servers {
		if name == agent.MCPServerName || isManagedServer(asMap(s)) {
			delete(servers, name)
		}
	}
	if len(servers) == 0 {
		delete(cfg, "mcp_servers")
	}
}

// addMCPServers adds MCP servers from config and bundles to [mcp_servers].
func addMCPServers(cfg map[string]any, mcp *wire.MCPConfig, bundleMCP map[string]wire.MCPServer) {
	servers := asMap(cfg["mcp_servers"])
	if servers == nil {
		servers = map[string]any{}
	}

	// Auto-register ctxloom's own MCP server unless disabled.
	if mcp == nil || mcp.ShouldAutoRegisterCtxloom() {
		servers[agent.MCPServerName] = mcpEntry(agent.CtxloomBinary, agent.CtxloomMCPArgs)
	}

	// Profile-bundle servers (loaded first, can be overridden).
	for name, server := range bundleMCP {
		servers[name] = mcpEntry(server.Command, server.Args)
	}

	if mcp != nil {
		for name, server := range mcp.Servers {
			servers[name] = mcpEntry(server.Command, server.Args)
		}
		for name, server := range mcp.Plugins["codex"] {
			servers[name] = mcpEntry(server.Command, server.Args)
		}
	}

	if len(servers) > 0 {
		cfg["mcp_servers"] = servers
	}
}

// mcpEntry builds a Codex [mcp_servers.NAME] table value.
func mcpEntry(command string, args []string) map[string]any {
	entry := map[string]any{"command": command}
	if len(args) > 0 {
		anyArgs := make([]any, len(args))
		for i, a := range args {
			anyArgs[i] = a
		}
		entry["args"] = anyArgs
	}
	return entry
}
