# InstaBox job machine setup

`heavy-task` runs jobs and browser calls on an InstaBox machine. With `INSTABOX_JOB_USER=heavy` in
`~/.config/picoclaw/instabox.env` it runs them as a separate sudo-less user, so a job (which a group member may
instruct) cannot read the machine user's logins, keys or other projects' files.

One-time setup on the machine (as the machine user, which has sudo):

1. `sudo useradd -m -d /data/heavy -s /bin/bash heavy && sudo chmod 750 /data/heavy`
2. Install `heavy-sync` from this folder to `/usr/local/bin/heavy-sync` (root, 755).
3. `/data/heavy/.profile`:
   ```sh
   [ -r ~/.secrets.env ] && . ~/.secrets.env
   export PATH="$HOME/.local/bin:$PATH"
   export INSTABOX_HEADLESS=1 AGENT_BROWSER_PROFILE="$HOME/.config/chromium"
   ```
4. `sudo heavy-sync`, then as heavy (`sudo -iu heavy`): `npm config set prefix ~/.local`,
   `npm i -g @mariozechner/pi-coding-agent agent-browser`, `uv tool install yt-dlp`,
   `~/.config/yt-dlp/config` (`--cookies-from-browser chromium`, `--js-runtimes node`, `--remote-components ejs:github`),
   `~/.pi/agent/models.json` (anthropic provider, `apiKey: ANTHROPIC_API_KEY`) and `~/.pi/agent/AGENTS.md` (job rules).
5. Select the org Secrets for the machine (`PATCH /v1/machines/{id}/secrets {"secrets":"all"}`); they reach login
   shells only, which is why `heavy-task` runs everything through `bash -l`.

The owner signs in to sites in the machine's own Chromium (the desktop browser, `/data/home/.config/chromium`);
`heavy-sync` copies its cookies to the job user before each job.
