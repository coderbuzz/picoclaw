# Group activity: per-member message counts

Lets members of a WhatsApp group ask "how many messages did each person write?". Related pieces: silent dispatch
rules and hidden MCP tools.

## Config

```jsonc
// channel_list.<whatsapp>.settings
"group_activity": {
  "groups": ["<id>@g.us"],          // only these groups are counted
  "retention_days": 90,             // default 90
  "names": {"server": "<mcp server>", "tool": "<tool>"}   // optional, see below
}

// agents.list[]: register the tool for this agent (also name it in the AGENT.md `tools:` list)
{"id": "<profile>-admin", "group_activity": true}

// agents.dispatch.rules[]: "-" drops matching messages (no agent, no reply, no typing, no read receipt)
{"name": "group members", "agent": "-", "when": {"channel": "whatsapp", "chat": "group:<id>@g.us"}}

// tools.mcp.servers.<name>: never offered to any agent, still callable by code
"hidden_tools": ["<tool>"]
```

Rules are matched in order, so keep the owner/trusted rules above the catch-all `-` rule.

## What is stored

`group_activity.db` next to the WhatsApp session store: one row per (group, sender, day WIB) with a counter and the
sender's latest push name. **No message text.** Rows older than `retention_days` are deleted at start and daily.
Counted: text, image, video, sticker, document, audio. Not counted: reactions, edits, revokes, protocol messages,
the bot's own messages, direct messages, groups not listed. Counting starts when the feature is switched on and runs
before every filter, so members the bot ignores are counted too.

## Tool `group_activity`

Arguments: `period` (`today`, `week` = last 7 days, `month` = last 30 days, `since` + `date` YYYY-MM-DD) and `limit`
(1..100, default 30). The group is the chat of the current turn, never an argument; any other chat gets "Penghitung
belum dinyalakan di grup ini". The result lists `name: count` (most first), total messages, number of people and the
date counting started. Phone numbers and JIDs never appear.

Names: with `names` set, the bot itself calls that MCP tool (not through an agent) with `{"nomor": ["62...", ...]}`
(max 100 per call, caller `_meta` from the turn). The last JSON line of the answer must be
`[{"i":0,"nama":"...","peran":"..."}]` (`i` = index in `nomor`). `nama: null` shows the push name plus
"(belum terdaftar)". LIDs are mapped to numbers through the whatsmeow store; if that or the MCP call fails, only push
names are shown. Mark the lookup tool in `hidden_tools` so nobody can ask it through chat.

## Privacy

Counts only, no content, retention-limited, only groups listed by the owner, numbers never shown. Announce the
counter in the group when switching it on.
