# WebCodex Local Workspace MCP

WebCodex connects MCP clients such as ChatGPT and Gemini to files, commands, and browsers on your Windows and Linux computers through a single self-hosted gateway. Each local agent connects outbound to the gateway, so it does not need an incoming connection.

Main features:

- **File management:** read, write, search, edit, move, and delete files within configured roots.
- **Command execution:** run shell commands, track background processes, inspect logs, and cancel tasks.
- **Browser tasks:** send bounded tasks by default to the existing uebagent MCP and its Docker Camoufox executor. The built-in WebCodex browser remains available as an explicit native backend.
- **Multiple computers:** discover available agents and route each operation to the selected machine.
- **Access management:** configure OAuth connections, permitted computers, and tool permissions through the web admin interface.

## AI Studio Bridge (alpha 0.3.10)

Version `0.3.10` lets `write_file` send its contents in a separate raw text block.
The model emits a small JSON call with `content_source` and a matching delimited
`helper-file` block; the extension supplies `arguments.content` and validates the
complete tool schema. This avoids invalid JSON caused by unescaped quotes in
generated HTML or JavaScript. File text is preserved exactly, without a size
ceiling. Existing JSON calls remain supported; malformed ones are not guessed or
silently repaired. Update the extension and reload a saved AI Studio tab. The
new format needs no agent or Gate replacement or restart; the previously compiled
alpha.5 agent is still available at `bin/ai-studio-bridge/webcodex-agent.exe`,
with no local build required. The alpha.5 update is still needed to remove the
older agent/Gate size limits described below.

Version `0.3.9` no longer restages instructions or consumes the next call when
AI Studio rerenders or virtualizes history. Instructions and their preset use
**Helper**; legacy blocks migrate in place. **Execute last call** lets the user
continue from a complete selected call without generating it again. Update only
the extension and reload a saved AI Studio tab for this fix; agent and Gate need
no replacement or restart.

Since extension `0.3.8` and agent/Gate `1.5.0-ai-studio-alpha.5`, fixed file, request
and result size ceilings have been removed throughout the local and Gate paths. Large session state
uses extension-local storage with unlimitedStorage. **Save file from reply** recovers
the last complete selected `write_file` without generating it again.
The extension waits for idle generation controls and one second of unchanged
answer text, then re-reads the DOM before dispatch. Malformed JSON gets eight
seconds of unchanged idle text before it is reported as an error. Resumed
generation cancels the pending deadline. This fixes premature parsing of automatic
followups after tool results. One JSON scanner handles the whole model answer and execution
validation. Code widget names, language labels, nested wrappers and syntax
highlighting do not control call recognition. Strings and command arguments remain
exact; ambiguous, duplicate-key and malformed calls show errors. Selected-tool
schemas, generation checks and protection against replaying history still apply.
Update the extension in `chrome://extensions`, replace and restart the agent with
alpha.5 using the existing Jinn launcher, then reload a saved AI Studio tab.
Save unsaved conversations first. Gate needs no update for direct local access;
replace and restart Gate with alpha.5 to remove the limits on the Gate route.
The agent EXE is already compiled at `bin/ai-studio-bridge/webcodex-agent.exe`;
no local build is required.

The Chrome extension sends commands from an ordinary AI Studio chat directly to
the first available local agent through `127.0.0.1`. The agent runs them locally and returns
results; this mode needs no host registration, Gate, OAuth, or model API key. Sessions stay in one
active tab without binding to a chat ID or URL. Do not use that tab for an
unrelated conversation. Selected tools and consent controls remain in effect.
An uncertain command is not automatically replayed. The live AI Studio page has
not been verified; its DOM selectors remain alpha.

The ready-to-use kit is in
[`bin/ai-studio-bridge`](bin/ai-studio-bridge) in this working copy: extension ZIP version `0.3.10`
and compiled `webcodex-agent.exe` version `1.5.0-ai-studio-alpha.5`. No local
build is required. Direct local mode does not need Gate. Replace the agent and extension, reload it in
`chrome://extensions`, and reload an already saved AI Studio tab. Do not reload
a tab with an unsaved conversation. Restart the agent with the existing Jinn launcher;
preserve its settings. Update and restart Gate only to apply these fixes to its route.
Each new session tries the local agent first, then uses the saved Gate
fallback if needed. While local mode is active, **Configure Gate fallback** sets
the Gate connection and target for future sessions; it does not switch the
current session. A session already using Gate stays pinned there until stopped
and restarted. A saved remote target may be another computer; sessions never
silently switch to an arbitrary agent.

Launch the local agent from PowerShell at the repository root:

```powershell
.\bin\ai-studio-bridge\webcodex-agent.exe --local
```

`--local` enables local mode and ignores Gate connection variables. The path
after `.\` selects the compiled EXE relative to the current directory. Permitted
roots come from the semicolon-separated absolute paths in `WEBCODEX_ALLOWED_ROOTS`,
previous local settings, or the startup directory. Discovery probes ports
`127.0.0.1:47831–47840`; an active session remains pinned to its selected agent.
The EXE is already compiled at the shown path in this working copy; binaries and
ZIPs are not stored in Git. Source-only checkouts use the separate
[build guide](ai-studio-extension/BUILD.ru.md). See the
[Russian setup and update guide](ai-studio-extension/README.ru.md).

Russian documentation: [README.ru.md](README.ru.md) · [User guide](USER_GUIDE.ru.md)

For the default uebagent browser integration, see
[the integration guide](docs/uebagent-integration.ru.md). The separate
[native browser guide](BROWSER_GUIDE.ru.md) applies only when
`WEBCODEX_BROWSER_BACKEND=native` is selected.

```text
MCP client ── HTTPS + OAuth ──> Gate ── outbound NDJSON stream ──> Local agent
                                │                                  │
                                └─ SQLite, routing, policy          └─ files, processes, logs
```

## Tools

`/mcp/v4` exposes eleven file and command operations plus `list_allowed_roots`. `/mcp/v3` retains its seven original operations plus root discovery. Gate also provides `list_agents`.

`/mcp/v5` adds `list_commands`, safe Retry keys and, when the configured local
uebagent endpoint is available, seven `browser_task_*` tools. The native
clean/persistent/personal browser remains an explicit opt-in. See
[uebagent setup](docs/uebagent-integration.ru.md). v3/v4 remain compatible.

For the native WebCodex backend only (`WEBCODEX_BROWSER_BACKEND=native`), a
signed-in Chrome/Edge profile can use the extension's **permanent access**
setting. See [the native browser guide](BROWSER_GUIDE.ru.md) before enabling
it. The default uebagent backend uses its separate Docker Camoufox profile.

Browser history no longer blocks new actions when full: the agent automatically
evicts completed results from its 4,096-entry memory cache. Mutation results and
Retry keys remain on disk for 24 hours, with no fixed entry count, and are loaded
on demand. Expired records are deleted at startup and every minute, including
while idle. Up to 128 operations may run concurrently; active operations are
never evicted. Update and restart the local agent to apply these limits.

| Tool | Operation |
| --- | --- |
| `list_agents` | Permitted computers and online status (Gate). |
| `list_allowed_roots` | Effective file roots on the selected computer. |
| `read_file` | Read a UTF-8 file or line range. |
| `write_file` | Atomically create or replace a file with a required precondition. |
| `list_directory` | List one directory without recursion. |
| `search_files` | Search text files by substring or optional regular expression. |
| `exec_command` | Start a shell command or argument vector with env/stdin and separate bounded logs. |
| `poll_command` | Read new stdout/stderr bytes and process status. |
| `cancel_command` | Terminate the process tree. |
| `edit_file` | Apply exact replacements guarded by a complete-file SHA-256. |
| `find_files` | Find files and directories recursively by basename glob. |
| `move_path` | Rename a regular file or empty directory without overwriting. |
| `delete_path` | Delete a hash-checked file or empty directory. |

File paths and command working directories must be absolute. There is no natural-language task parser, conversation state, interactive terminal, or tool alias.

## Build and test

Go 1.25 or newer is required.

```powershell
go test -count=1 ./...
go vet ./...
go build -o webcodex-agent.exe ./cmd/agent
go build -o bin/webcodex-gate.exe ./cmd/gate
```

`build.bat` and `build.sh` build both binaries.

## Gate

Copy `.env.example` to `.env`, set a strong admin password, then start the public gate:

```bash
docker compose up -d --build
```

The current connector endpoint is `/mcp/v5`; v3/v4 are retained for rollback. The other important endpoints are `/oauth/authorize`, `/oauth/token`, `/agent/stream`, `/agent/result`, `/admin`, and `/healthz`. Put the gate behind HTTPS. OAuth requires PKCE S256 and a recognized ChatGPT or Gemini callback URI (`https://oauth-redirect.googleusercontent.com/r/user_bound_custom-mcp-<id>`). The same MCP OAuth credentials can connect both clients; their access tokens remain valid independently. Access tokens expire after `WEBCODEX_ACCESS_TOKEN_TTL` (24 hours by default).

Register agents and MCP connections separately in `/admin`. Save the Agent Token when creating a computer, and the OAuth Client ID and Secret when creating an MCP connection. Secrets are shown once; only SHA-256 hashes are persisted.

### Multiple computers through one MCP URL

1. Register each computer under Agents and run it with its own
   `WEBCODEX_AGENT_TOKEN` and the shared `WEBCODEX_GATE_URL`.
2. Register an independent MCP connection, save its OAuth Client ID and Secret,
   then select the permitted computers in its card.
3. Connect Gemini or ChatGPT once to `https://example.com/mcp/v5` using those OAuth
   credentials. Worker tokens remain separate and are never tool arguments.

Call `list_agents`, then pass `agent_id` to file, command and browser tools,
including subsequent session operations. For example:
`list_allowed_roots({"agent_id":"home-pc"})` or
`read_file({"agent_id":"home-pc","path":"C:\\projects\\app\\README.md"})`.
Selecting a computer is required with multiple assigned agents; it is optional
with one for compatibility with existing connectors. Unknown, disabled and offline
computers produce errors without fallback. Tool policy applies to the target agent.

Grants are checked on each request and affect all active sessions of that MCP
connection. MCP registrations and agents have independent enable/disable, deletion
and secret rotation. Deleting a worker removes its assignment without revoking the
MCP registration or other computers. Disabling an MCP registration does not stop
its workers. On upgrade, legacy OAuth credentials and access tokens are migrated
into independent MCP registrations with their original computer assignments.

Discovery merges tools from connected permitted agents. `list_agents` works even
if all computers are offline or no agents are assigned. Refresh tools or reconnect
after changing grants or bringing new computers online. Update grouped agents
together: tools with the same name must have identical input and output schemas.

## Local agent

Windows PowerShell example:

```powershell
$env:WEBCODEX_GATE_URL = "https://example.com"
$env:WEBCODEX_AGENT_TOKEN = "<agent token from /admin>"
$env:WEBCODEX_ALLOWED_ROOTS = "C:\projects;W:\"
$env:WEBCODEX_BROWSER_ENABLED = "1"
$env:WEBCODEX_LOG_DIR = "$PSScriptRoot\logs"
./webcodex-agent.exe
```

Linux environment file example:

```env
WEBCODEX_GATE_URL=https://example.com
WEBCODEX_AGENT_TOKEN=<agent token from /admin>
WEBCODEX_ALLOWED_ROOTS=/srv/projects:/opt/work
WEBCODEX_LOG_DIR=/var/log/webcodex-agent
WEBCODEX_PROCESS_TTL=24h
WEBCODEX_MAX_LOG_BYTES=52428800
```

Agent cards default to `C:\projects` and `W:\`. Without local `WEBCODEX_ALLOWED_ROOTS`, the agent loads roots from `/agent/config` at startup. A local .bat/.ps1 or environment value fully overrides Gate defaults. Restart after changing roots; downloaded scripts contain a local copy, so remove that assignment to inherit Gate settings. Roots unavailable at startup grant no access; restart after mounting them. `list_allowed_roots` reports effective roots, availability and unrestricted access. Downloaded .bat/.ps1 scripts enable browsing with `WEBCODEX_BROWSER_ENABLED=1`; set it to `0` to disable. For Linux, configure its absolute paths in admin or environment. `WEBCODEX_ALLOWED_ROOTS=*` deliberately permits all local paths. Symlinks are resolved before access checks.

Each command session is stored under `<log-dir>/<session-id>/` with `metadata.json`, `stdout.log`, and `stderr.log`. Metadata excludes command text, stdin, and environment values. Completed sessions remain pollable after an agent restart and expire after 24 hours by default. Running sessions are restored only when the persisted PID identity still matches.

## Execution visibility

The agent console defaults to short Russian action summaries: files being read,
folders being inspected, file creation/edits and browser actions. File contents,
protocol chatter, repeated command polling and internal IDs are hidden.
Successful `write_file`/`edit_file` operations show only changed lines in two
columns, before and after, with line numbers. Previews show at most 40 rows and
shorten long lines; files over 20,000 total lines or more than 256 inserted/deleted
lines receive a short notice instead. Shell changes are described by the command,
without automatic file comparison.

Commands retain disk stdout/stderr logs. The console reports running status every
10 seconds, completion, nonzero exit codes, start/stop failures and disk log limits.
Failures include a bounded stderr excerpt when available. File and browser errors
remain visible as short action-specific warnings.

Set `WEBCODEX_DEBUG=1` for request/session/operation IDs, PID, timing, log paths and
escaped stdout/stderr chunks. `WEBCODEX_VERBOSE=1` remains a fallback; an explicit
`WEBCODEX_DEBUG=0` overrides it. Terminal control characters are escaped. Environment
values and stdin are not automatically logged; commands, changed text and stderr
may contain secrets. Restored processes may have unknown exit codes.

`exec_command` now yields after 1 second by default (at most 3 seconds even if
a longer wait is requested); the process continues in
the background. Client disconnects do not cancel it. Resume with `poll_command`
using its session ID, rather than rerunning the command. Set `timeout_seconds`
for the expected computation time (default 1200, maximum 86400).

Gate instructions recommend explaining commands before execution, yielding
after 1–3 seconds, polling running sessions with returned offsets and reporting
meaningful progress about every 10–20 seconds. Chat updates depend on the client
following these instructions; no messages are pushed into a completed chat.
A successful tool response does not mean the process has finished successfully.

## Security

Open **Monitoring** on the agent's admin page (`/admin/agents/<id>/commands`)
to filter recent sessions, tail stdout/stderr, download logs and stop processes.
The page polls every two seconds; admin authentication, tool policy and CSRF
apply. It displays the last 256 KiB per stream; downloads stop at EOF or 100 MiB.
Logs stay on the agent. Offline agents show an update error and reconnect.

Connect to `/mcp/v5` for `list_commands` and idempotent `exec_command`.
Supply a unique `request_key`; reuse the same key and arguments after Retry.
Conflicting arguments are rejected; ambiguous launches return `outcome_unknown`
without relaunching. Keys expire with their session (`WEBCODEX_PROCESS_TTL`);
do not retry expired launches. Existing v3/v4 operations remain available.
Update Gate and agent together. After the database migration, an older Gate
requires the pre-upgrade database backup for rollback.

`exec_command` is remote code execution with the operating-system rights of the local agent. `WEBCODEX_ALLOWED_ROOTS` protects filesystem tools only; it cannot contain a shell process. Deny `exec_command` and `cancel_command` unless needed. New agents also deny `edit_file`, `move_path`, and `delete_path` until they are explicitly enabled.

Run the agent under a dedicated unprivileged account. On Windows, grant that account NTFS access only to the intended workspaces and log directory. On Linux, update `ReadWritePaths` in `deploy/webcodex-agent.service` to match the configured roots; the unit also enables `NoNewPrivileges`, `PrivateTmp`, and `ProtectSystem`. Restrict outbound network access at the OS or container layer when builds do not need downloads.

Back up SQLite, binaries, service files, and command logs before deployment. Deploy Gate first, then replace the agent binary and install the browser extension if needed. Create a fresh connector at `/mcp/v5` and start a new conversation. v5 adds root discovery and computer selection to its file, command and browser tools; policy may filter the list. Roll back by disabling browser locally, reconnecting to `/mcp/v4` and restoring the prior agent binary; preserve personal/persistent profiles.
