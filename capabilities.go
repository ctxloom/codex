package codex

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/afero"

	"github.com/ctxloom/shared/agent"
)

// CodexLifecycle implements LifecycleHandler for Codex using config.toml hooks.
// Embeds BaseLifecycle for the shared implementation.
type CodexLifecycle struct {
	*agent.BaseLifecycle
	backend *Codex
}

// NewCodexLifecycle creates a new Codex lifecycle handler.
func NewCodexLifecycle(backend *Codex) *CodexLifecycle {
	return &CodexLifecycle{
		BaseLifecycle: agent.NewBaseLifecycle("codex", backend.writeSettings),
		backend:       backend,
	}
}

// CodexMCPManager implements MCPManager for Codex CLI.
// Embeds BaseMCPManager for the shared implementation.
type CodexMCPManager struct {
	*agent.BaseMCPManager
	backend *Codex
}

// NewCodexMCPManager creates a new Codex MCP manager.
func NewCodexMCPManager(backend *Codex) *CodexMCPManager {
	return &CodexMCPManager{
		BaseMCPManager: agent.NewBaseMCPManager("codex", backend.writeSettings),
		backend:        backend,
	}
}

// CodexContext implements ContextProvider for Codex using file + hook.
// Embeds BaseContextProvider for the shared implementation.
type CodexContext struct {
	*agent.BaseContextProvider
	backend *Codex
}

// NewCodexContext creates a new Codex context provider.
func NewCodexContext(backend *Codex) *CodexContext {
	return &CodexContext{
		BaseContextProvider: agent.NewBaseContextProvider(),
		backend:             backend,
	}
}

// CodexSkills implements SkillRegistry for Codex CLI using custom prompts.
type CodexSkills struct {
	backend *Codex
}

// Register adds a skill as a Codex custom prompt.
func (s *CodexSkills) Register(workDir string, skill agent.Skill) error {
	return WriteCommandFiles(workDir, []agent.CommandExport{skillExport(skill)})
}

// RegisterAll adds multiple skills as Codex custom prompts.
func (s *CodexSkills) RegisterAll(workDir string, skills []agent.Skill) error {
	cmds := make([]agent.CommandExport, 0, len(skills))
	for _, skill := range skills {
		cmds = append(cmds, skillExport(skill))
	}
	return WriteCommandFiles(workDir, cmds)
}

// RegisterFromContent writes custom prompts from host-resolved command exports.
// The host maps bundle content (with codex enablement + metadata) to these
// agent-agnostic exports, so this stays config/bundle-free.
func (s *CodexSkills) RegisterFromContent(workDir string, cmds []agent.CommandExport) error {
	return WriteCommandFiles(workDir, cmds)
}

// skillExport maps a Skill to an enabled command export.
func skillExport(skill agent.Skill) agent.CommandExport {
	return agent.CommandExport{
		Name:        skill.Name,
		Content:     skill.Content,
		Enabled:     true,
		Description: skill.Description,
	}
}

// Clear removes all ctxloom-managed prompts using the manifest. workDir is
// unused — codex prompts live in the global $CODEX_HOME/prompts (see
// codexPromptsDir).
func (s *CodexSkills) Clear(workDir string) error {
	promptsDir := codexPromptsDir()
	manifestPath := filepath.Join(promptsDir, codexManifest)

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, name := range strings.Split(string(data), "\n") {
		if name = strings.TrimSpace(name); name != "" {
			_ = os.Remove(filepath.Join(promptsDir, name))
		}
	}
	return os.Remove(manifestPath)
}

// List returns registered prompt names from the manifest. workDir is unused —
// codex prompts live in the global $CODEX_HOME/prompts (see codexPromptsDir).
func (s *CodexSkills) List(workDir string) ([]string, error) {
	manifestPath := filepath.Join(codexPromptsDir(), codexManifest)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(string(data), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names = append(names, strings.TrimSuffix(name, ".md"))
		}
	}
	return names, nil
}

// CodexSessionHistory implements SessionHistory for Codex CLI. Reads from
// $CODEX_HOME/sessions/YYYY/MM/DD/rollout-*.jsonl (default ~/.codex). The fs and
// homeDir fields are afero injection points used by tests.
type CodexSessionHistory struct {
	backend *Codex
	fs      afero.Fs
	homeDir string // empty => fall back to os.UserHomeDir + $CODEX_HOME
}

// CodexSessionHistoryOption configures CodexSessionHistory.
type CodexSessionHistoryOption func(*CodexSessionHistory)

// WithCodexSessionFS sets a custom filesystem for testing.
func WithCodexSessionFS(fs afero.Fs) CodexSessionHistoryOption {
	return func(h *CodexSessionHistory) { h.fs = fs }
}

// WithCodexSessionHomeDir sets a custom home directory for testing. Overrides
// both os.UserHomeDir and the CODEX_HOME env var.
func WithCodexSessionHomeDir(dir string) CodexSessionHistoryOption {
	return func(h *CodexSessionHistory) { h.homeDir = dir }
}

// NewCodexSessionHistory creates a new Codex session history handler.
func NewCodexSessionHistory(backend *Codex, opts ...CodexSessionHistoryOption) *CodexSessionHistory {
	h := &CodexSessionHistory{
		backend: backend,
		fs:      afero.NewOsFs(),
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// GetCurrentSession returns the current/most recent session transcript.
func (h *CodexSessionHistory) GetCurrentSession(workDir string) (*agent.Session, error) {
	sessions, err := h.ListSessions(workDir)
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, fmt.Errorf("no sessions found")
	}
	return h.GetSession(workDir, sessions[0].ID)
}

// ListSessions returns available session metadata.
func (h *CodexSessionHistory) ListSessions(workDir string) ([]agent.SessionMeta, error) {
	sessionsDir, err := h.getSessionsDir()
	if err != nil {
		return nil, err
	}

	var sessions []agent.SessionMeta
	err = afero.Walk(h.fs, sessionsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors, continue walking
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasPrefix(info.Name(), "rollout-") || !strings.HasSuffix(info.Name(), ".jsonl") {
			return nil
		}
		relPath, _ := filepath.Rel(sessionsDir, path)
		sessions = append(sessions, agent.SessionMeta{
			ID:        relPath,
			StartTime: info.ModTime(),
			Path:      path,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].StartTime.After(sessions[j].StartTime)
	})
	return sessions, nil
}

// GetSession returns a specific session by ID.
func (h *CodexSessionHistory) GetSession(workDir string, sessionID string) (*agent.Session, error) {
	sessionsDir, err := h.getSessionsDir()
	if err != nil {
		return nil, err
	}
	return h.parseSessionFile(filepath.Join(sessionsDir, sessionID))
}

// GetSessionByPath returns a session by its full file path.
func (h *CodexSessionHistory) GetSessionByPath(path string) (*agent.Session, error) {
	return h.parseSessionFile(path)
}

// getSessionsDir returns the Codex sessions directory. Resolution order:
// explicit homeDir override (test-only) -> $CODEX_HOME -> os.UserHomeDir()/.codex.
// The chosen dir is suffixed with /sessions and stat-checked through the injected
// fs so tests with an empty MemMapFs see "not found" without touching the OS.
func (h *CodexSessionHistory) getSessionsDir() (string, error) {
	var codexHome string
	switch {
	case h.homeDir != "":
		codexHome = filepath.Join(h.homeDir, ".codex")
	case os.Getenv("CODEX_HOME") != "":
		codexHome = os.Getenv("CODEX_HOME")
	default:
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to get home directory: %w", err)
		}
		codexHome = filepath.Join(homeDir, ".codex")
	}

	sessionsDir := filepath.Join(codexHome, "sessions")
	if _, err := h.fs.Stat(sessionsDir); err != nil {
		return "", fmt.Errorf("sessions directory not found: %s", sessionsDir)
	}
	return sessionsDir, nil
}

// parseSessionFile reads and parses a Codex session JSONL file.
func (h *CodexSessionHistory) parseSessionFile(path string) (*agent.Session, error) {
	file, err := h.fs.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}
	defer func() { _ = file.Close() }()

	session := &agent.Session{
		ID:      filepath.Base(path),
		Entries: []agent.SessionEntry{},
	}

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		entry, err := h.parseEntry(line)
		if err != nil {
			continue // Skip malformed entries
		}
		if entry != nil {
			session.Entries = append(session.Entries, *entry)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan session file: %w", err)
	}

	if len(session.Entries) > 0 {
		session.StartTime = session.Entries[0].Timestamp
		session.EndTime = session.Entries[len(session.Entries)-1].Timestamp
	}
	return session, nil
}

// codexEntry represents a raw entry from Codex's rollout JSONL.
type codexEntry struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Role      string          `json:"role"`
	Content   string          `json:"content"`
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	Output    string          `json:"output"`
	IsError   bool            `json:"is_error"`
}

// parseEntry converts a Codex JSONL entry to a normalized SessionEntry.
func (h *CodexSessionHistory) parseEntry(line []byte) (*agent.SessionEntry, error) {
	var raw codexEntry
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, err
	}

	entry := &agent.SessionEntry{}
	if raw.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339, raw.Timestamp); err == nil {
			entry.Timestamp = t
		}
	}

	switch raw.Type {
	case "message":
		switch raw.Role {
		case "user":
			entry.Type = agent.EntryTypeUser
			entry.Content = raw.Content
		case "assistant":
			entry.Type = agent.EntryTypeAssistant
			entry.Content = raw.Content
		default:
			return nil, nil
		}
	case "tool_use", "codex.tool_decision":
		entry.Type = agent.EntryTypeToolUse
		entry.ToolName = raw.ToolName
		entry.ToolInput = raw.ToolInput
	case "tool_result", "codex.tool_result":
		entry.Type = agent.EntryTypeToolResult
		entry.ToolName = raw.ToolName
		entry.ToolOutput = raw.Output
		entry.IsError = raw.IsError
	default:
		return nil, nil // Skip unknown types
	}
	return entry, nil
}

// TranscriptPathFromHook returns the transcript path codex provides directly on
// every hook's stdin (the shared `transcript_path` field), enabling /clear
// recovery and session-bind. Empty when codex reports none (transcript_path may
// be null); the path points at the session's rollout-*.jsonl, which
// GetSessionByPath parses.
func (h *CodexSessionHistory) TranscriptPathFromHook(workDir, sessionID, transcriptPath string) string {
	return transcriptPath
}
