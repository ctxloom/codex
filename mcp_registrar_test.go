package codex

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/shared/wire"
)

func TestMCPRegistrar_Name(t *testing.T) {
	assert.Equal(t, "codex", MCPRegistrar{}.Name())
}

func TestMCPRegistrar_ConfigPath(t *testing.T) {
	p, err := (MCPRegistrar{}).ConfigPath("/proj", false)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/proj", ".codex", "config.toml"), p)

	g, err := (MCPRegistrar{}).ConfigPath("/proj", true)
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(g, filepath.Join(".codex", "config.toml")), g)
	assert.NotContains(t, g, "/proj", "global path is home-rooted")
}

func TestMCPRegistrar_InstallPreservesForeignTables(t *testing.T) {
	existing := `[hooks]
[[hooks.SessionStart]]
[[hooks.SessionStart.hooks]]
command = 'ctxloom hook session-bind'
type = 'command'

[mcp_servers]
[mcp_servers.ctxloom]
args = ['mcp']
command = 'ctxloom'
`
	r := MCPRegistrar{}
	out, err := r.Install([]byte(existing), "taskloom", wire.MCPServer{Command: "taskloom", Args: []string{"mcp"}})
	require.NoError(t, err)

	ok, err := r.Installed(out, "taskloom")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Contains(t, string(out), "session-bind", "foreign hooks table survives")

	// Idempotent.
	again, err := r.Install(out, "taskloom", wire.MCPServer{Command: "taskloom", Args: []string{"mcp"}})
	require.NoError(t, err)
	assert.Equal(t, string(out), string(again))

	removed, err := r.Uninstall(out, "taskloom")
	require.NoError(t, err)
	gone, err := r.Installed(removed, "taskloom")
	require.NoError(t, err)
	assert.False(t, gone)
	foreign, err := r.Installed(removed, "ctxloom")
	require.NoError(t, err)
	assert.True(t, foreign)
}
