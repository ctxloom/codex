# github.com/ctxloom/codex

ctxloom's OpenAI Codex CLI agent module — the settings/hooks writer
(`agent.SettingsWriter`) and the launch backend (`agent.Backend`), mirroring the
`claude` and `gemini` agent modules.

## ⚠️ WARNING: implemented against docs, entirely untested

**Every codex-specific behavior in this module is derived solely from the
published OpenAI Codex CLI documentation and has never been run against a real
codex binary** — the maintainer's Linux development platform has no codex access.

That includes:

- the `.codex/config.toml` `[[hooks.EVENT]]` and `[mcp_servers.NAME]` format
- the `codex exec` non-interactive invocation and `--sandbox` /
  `--ask-for-approval` / `--model` flags (the previous `--quiet` flag is not in
  the current CLI reference)
- the global `~/.codex/prompts` location and `description`/`argument-hint`
  frontmatter for custom prompts
- the `session_id` / `transcript_path` hook stdin fields used for session
  registration

The Go unit tests verify our *logic* (what we write, how we parse), **not
whether codex accepts what we emit**. Until someone smoke-tests this on a machine
with codex installed — write hooks/MCP, launch codex, confirm the SessionStart
hook fires and the MCP server connects, run a oneshot/distill — treat every
format and flag here as **provisional**.

Sources: `developers.openai.com/codex` (config-reference, hooks, mcp,
custom-prompts, cli/reference).
