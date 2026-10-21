# Tool `group_trust`

Built-in tool for a group's owner agent (`<id>-admin`) to grant, revoke and list trusted members. Since agent exec runs in a bubblewrap
sandbox (S7), `group-agents` and the bot config are invisible there; this tool runs in the bot process and starts the host's
`group-agents` binary directly.

## Config
```json
{"id": "club-admin", "group_trust": {"profile": "club", "command": "/home/bot/.local/bin/group-agents"}}
```
- `profile`: group profile the agent manages. Set in config only; the agent cannot choose another.
- `command`: absolute path of `group-agents` (optional, default: looked up in the bot's `PATH`).
- No `group_trust` field = tool not registered. List `group_trust` in the agent's `AGENT.md` `tools:` frontmatter.
- `group-agents new <id>` writes the field for `<id>-admin` only (never `-staff`), fills it in for older profiles and never overwrites an existing value.

## Tool
Parameters: `action` (`add`, `remove`, `list`), `member` (phone number or numeric ID), optional `name` (for `add`).
`member` is stripped of `+`, spaces, `@lid`, `@s.whatsapp.net` and must match `^[0-9]{6,20}$`.

It runs `group-agents trust <profile> <action> [member [name]]` with no shell, working directory = the agent workspace
(so the script's `authorize()` accepts it), a 30 s timeout, the bot's environment, and returns the output plus the exit code (output truncated).
The script restarts the bot 20 s after a change.

## Deploy to an existing profile
1. Add `"group_trust": {"profile": "<id>"}` to `<id>-admin` (or run the new `group-agents new <id>`).
2. Put `group_trust` in `tools:` of `ws-<id>-admin/AGENT.md` and replace the `exec group-agents trust ...` commands in "Izin anggota" with tool calls.
3. The bot's service must be able to run `systemd-run --user` (the script's delayed restart); otherwise `add`/`remove` fail after saving.
