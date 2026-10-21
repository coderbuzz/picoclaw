# Tool `google` (Sheets, Docs, Slides, Drive)

Built-in tool that lets an agent read and edit Google files with the **service account attached to the GCE VM**. No key file:
the access token comes from the metadata server (`169.254.169.254`), is cached in memory until about a minute before expiry, and is never
written to tool results, errors or logs. Agents that run `exec` in the network-less sandbox use this tool instead, because it runs in the bot process.

## Setup
1. Attach a service account to the VM with scopes `spreadsheets`, `documents`, `presentations`, `drive` (stop the VM to change scopes).
2. Share each Sheet/Doc/Slide with the service account's email (`bot-sa@example-project.iam.gserviceaccount.com`) as Editor (or Viewer for read-only use).
   The bot cannot create files: create them yourself, then share.
3. Enable the Sheets, Docs, Slides and Drive APIs in the service account's project.
4. Give the agent access in `config.json` and list `google` in its `AGENT.md` `tools:` frontmatter. Without `agents.list[].google` the tool is not registered for that agent.

```json
{
  "agents": {
    "list": [
      {
        "id": "club-admin",
        "google": {
          "files": ["1AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-aBcDe"],
          "write": true
        }
      }
    ]
  }
}
```

- `files`: allowed file IDs (the part after `/d/` in the URL), or `["*"]` for anything the service account can see. Other IDs are refused.
- `write`: `false` (default) refuses `sheets_write`, `sheets_append`, `docs_append`, `docs_replace`, `slides_replace`.

## Actions
`sheets_info`, `sheets_read` (`range`), `sheets_write` (`range`, `values`), `sheets_append` (`range` or tab, `values`), `docs_read`, `docs_append` (`text`),
`docs_replace` (`find`, `replace`), `slides_read`, `slides_replace` (`find`, `replace`), `drive_list`.
`file` accepts an ID or a `docs.google.com` URL. Output is truncated at 20000 bytes with a note.
