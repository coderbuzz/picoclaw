# App MCP: how the bot asks other apps for data

One contract for every app the bot talks to (Sirkel first, then Dalang, Nalar, ...). The app exposes an
[MCP](https://modelcontextprotocol.io) server over Streamable HTTP; PicoClaw is the MCP client. Because it is plain MCP,
the same endpoint also works from Claude Code, Claude Desktop or any other MCP client that holds a token.

Existing channels stay as they are: `dm_apps.d` / `group_apps.d` (keyword messages to an app) and `msg_hub` (app pushes
to WhatsApp). App MCP is the third direction: **the agent pulls answers from the app** when a person asks.

```
member in group / owner in DM ──> agent ──tools/call──> https://<app>/api/mcp
                                        _meta caller (set by bot code)
                                        Authorization: Bearer <audience token>
```

## App side (the contract)

1. **Endpoint**: `POST https://<app-host>/api/mcp`, MCP Streamable HTTP, protocol `2025-06-18` or later. A stateless server
   that answers each POST with `application/json` is enough (no SSE, no session store needed; `Mcp-Session-Id` optional).
2. **Auth**: `Authorization: Bearer <token>`. One token per **audience**, stored hashed by the app, revocable:
   - `owner`: the bot owner's personal agent. The app maps it to one app account and answers as that account.
   - `group`: shared by all group agents. On its own it grants nothing: every answer needs a linked group (see 4).
   Wrong or missing token: HTTP 401, nothing else.
3. **Caller facts**: every `tools/call` carries `params._meta["id.qumatic/caller"]`:
   ```json
   {"channel": "whatsapp", "chat_type": "group", "chat_id": "<group jid>", "sender_id": "<sender id>", "agent_id": "<agent>"}
   ```
   Keys are omitted when unknown. The bot fills them from the inbound message, never from model output, so the app trusts
   them exactly as far as it trusts the token. `sender_id` may be a phone JID or a WhatsApp LID: use it for audit, not
   for authorization, unless the app already stores that exact ID.
4. **Authorization is the app's job** and comes only from token + caller, **never from tool arguments**:
   - `group` token: `chat_id` must be a group already linked to an app space (for Sirkel: `SIRKEL HUBUNGKAN`, the same
     link `msg_hub` checks). Unlinked group: `isError` with a short how-to-link message. Answers cover only the linked
     space, at "safe for everyone in this group" level: counts, totals, names already visible to members; no phone
     numbers, emails, addresses, per-person money unless the space's own setting already shows it to members.
   - `owner` token: whatever that account can see in the app UI, no more.
   - An argument like `space_id` may only pick among spaces the caller is already allowed; otherwise refuse.
5. **Tools**: read-only in v1. `snake_case` names, descriptions in Bahasa Indonesia that say what the tool answers and
   when to use it (the model reads them). Every app has `konteks` first: who the caller is to the app, which space(s) are in
   scope, and which other tools make sense. Keep each result under ~4 KB: plain text or small JSON, always with the
   as-of time (`per 17 Nov 2026 10.26 WIB`) and, when useful, a link to the page in the app.
   Write tools come later and need their own review (confirmation, idempotency key, audit).
6. **Errors**: tool-level problems (`not linked`, `no access`, `not found`) are `isError: true` results with one sentence
   the agent can repeat to people. HTTP errors are for transport/auth only.
7. **Limits and audit**: rate-limit per token + `chat_id` (suggested 30 calls/min) and log tool, audience, chat, sender,
   time and result size. No raw personal data in logs.

## Bot side (PicoClaw config)

Tokens live in `~/.picoclaw/secrets/` (600). Header values accept `file://` (relative to the config dir) or `enc://`; the
file holds the whole header value, e.g. `Bearer abc...`.

```json
{
  "tools": {
    "mcp": {
      "enabled": true,
      "servers": {
        "sirkel":      {"enabled": true, "type": "http", "url": "https://<app-host>/api/mcp",
                        "headers": {"Authorization": "file://secrets/sirkel-mcp-owner"}},
        "sirkel_grup": {"enabled": true, "type": "http", "url": "https://<app-host>/api/mcp",
                        "headers": {"Authorization": "file://secrets/sirkel-mcp-group"}}
      }
    }
  }
}
```

Which agent may use which server is set in its `AGENT.md` frontmatter. An agent without `mcpServers` sees **every**
server, so each agent must declare it (an empty list for none):

```yaml
mcpServers: [sirkel]            # owner's main agent
mcpServers: [sirkel_grup]       # group work/member agents that may ask the app
mcpServers: []                  # everyone else
```

Agents with a `tools:` allowlist must also list the tool names, which are `mcp_<server>_<tool>`
(e.g. `mcp_sirkel_grup_konteks`). Add a line to the agent's instructions: numbers from the app are the source of truth,
quote the as-of time, and say so plainly when the app refuses.

## Adding another app

1. App: implement `/api/mcp` per the contract above, start with `konteks` + 2-3 read tools, mint the two tokens.
2. Bot: token files, one or two `servers` entries, `mcpServers` + `tools:` in the agents that need them, restart.
3. Check with `picoclaw mcp test <server>` (lists tools), then one real question from DM and one from a linked group.
