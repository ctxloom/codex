package codex

import (
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/shared/agent"
	"github.com/ctxloom/shared/wire"
)

func readConfig(t *testing.T, fs afero.Fs, path string) map[string]any {
	t.Helper()
	data, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, toml.Unmarshal(data, &cfg))
	return cfg
}

// TestWriteSettings_CompanionHookIdempotent pins re-apply behavior for hooks
// whose executable is a companion binary (no ctxloom token, no durable
// marker): the exact command must be deduplicated, while a user variant of the
// same binary with different arguments survives.
func TestWriteSettings_CompanionHookIdempotent(t *testing.T) {
	fs := afero.NewMemMapFs()
	// User's own ltk registration (different args) predates ctxloom's.
	existing := "[hooks]\n[[hooks.PreToolUse]]\nmatcher = 'Bash'\n[[hooks.PreToolUse.hooks]]\ncommand = 'ltk evaluate --config .ltk/config.yaml'\ntype = 'command'\n"
	require.NoError(t, afero.WriteFile(fs, "/proj/.codex/config.toml", []byte(existing), 0644))

	w := NewWriter(agent.SettingsOptions{FS: fs})
	hooks := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			PreTool: []wire.Hook{{Command: "ltk evaluate", Matcher: "Bash", SCM: "bundle:builtin:ltk"}},
		},
	}

	countLtk := func() (exact, variant int) {
		cfg := readConfig(t, fs, "/proj/.codex/config.toml")
		for _, g := range asSlice(asMap(cfg["hooks"])["PreToolUse"]) {
			for _, e := range asSlice(asMap(g)["hooks"]) {
				switch asMap(e)["command"] {
				case "ltk evaluate":
					exact++
				case "ltk evaluate --config .ltk/config.yaml":
					variant++
				}
			}
		}
		return
	}

	require.NoError(t, w.WriteSettings(hooks, nil, nil, "/proj"))
	require.NoError(t, w.WriteSettings(hooks, nil, nil, "/proj"))

	exact, variant := countLtk()
	assert.Equal(t, 1, exact, "companion hook must not duplicate across re-applies")
	assert.Equal(t, 1, variant, "user's own variant of the same binary must survive")
}

// TestWriteSettings_HooksAndMCP verifies the writer emits codex's
// [[hooks.EVENT]] groups and [mcp_servers] table, auto-registers ctxloom, and
// preserves unrelated user keys.
func TestWriteSettings_HooksAndMCP(t *testing.T) {
	fs := afero.NewMemMapFs()
	// Pre-existing user config the writer must preserve.
	require.NoError(t, afero.WriteFile(fs, "/proj/.codex/config.toml", []byte("model = \"o3\"\n"), 0644))

	w := NewWriter(agent.SettingsOptions{FS: fs})
	hooks := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: "ctxloom hook inject-context"}},
			PreTool:      []wire.Hook{{Command: "ctxloom hook stamp", Matcher: "Bash"}},
		},
	}
	mcp := &wire.MCPConfig{Servers: map[string]wire.MCPServer{
		"context7": {Command: "npx", Args: []string{"-y", "@upstash/context7-mcp"}},
	}}

	require.NoError(t, w.WriteSettings(hooks, mcp, nil, "/proj"))

	cfg := readConfig(t, fs, "/proj/.codex/config.toml")
	assert.Equal(t, "o3", cfg["model"], "user key preserved")

	hookTbl := asMap(cfg["hooks"])
	require.NotNil(t, hookTbl)
	assert.NotEmpty(t, asSlice(hookTbl["SessionStart"]), "SessionStart group written")
	assert.NotEmpty(t, asSlice(hookTbl["PreToolUse"]), "PreTool maps to PreToolUse")

	servers := asMap(cfg["mcp_servers"])
	require.NotNil(t, servers)
	assert.Contains(t, servers, "context7", "config MCP server written")
	assert.Contains(t, servers, agent.MCPServerName, "ctxloom server auto-registered")

	status, err := w.Status("/proj")
	require.NoError(t, err)
	assert.True(t, status.HooksPresent)
	assert.True(t, status.MCPPresent)
}

// TestRemoveSettings strips ctxloom-managed hooks + MCP but keeps user content.
func TestRemoveSettings(t *testing.T) {
	fs := afero.NewMemMapFs()
	w := NewWriter(agent.SettingsOptions{FS: fs})

	hooks := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		SessionStart: []wire.Hook{{Command: "ctxloom hook inject-context"}},
	}}
	require.NoError(t, w.WriteSettings(hooks, nil, nil, "/proj"))

	// A user-authored hook the writer must not touch.
	cfg := readConfig(t, fs, "/proj/.codex/config.toml")
	hookTbl := asMap(cfg["hooks"])
	hookTbl["Stop"] = []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "/usr/bin/mine"}}}}
	var buf []byte
	buf, err := toml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, afero.WriteFile(fs, "/proj/.codex/config.toml", buf, 0644))

	require.NoError(t, w.RemoveSettings("/proj"))

	got := readConfig(t, fs, "/proj/.codex/config.toml")
	gotHooks := asMap(got["hooks"])
	assert.NotContains(t, gotHooks, "SessionStart", "ctxloom hook removed")
	assert.Contains(t, gotHooks, "Stop", "user hook preserved")
}
