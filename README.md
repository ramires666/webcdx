# WebCodex Local Workspace MCP

WebCodex exposes explicit file and process operations from local Windows/Linux machines through one public MCP gate. The gate keeps OAuth, SQLite, per-machine routing, and tool policy; each local agent makes only an outbound HTTP connection.

Russian documentation: [README.ru.md](README.ru.md) · [User guide](USER_GUIDE.ru.md)

```text
MCP client ── HTTPS + OAuth ──> Gate ── outbound NDJSON stream ──> Local agent
                                │                                  │
                                └─ SQLite, routing, policy          └─ files, processes, logs
```

## Tools

`/mcp/v4` exposes eleven file and command operations plus `list_allowed_roots`. `/mcp/v3` retains its seven original operations plus root discovery. Gate also provides `list_agents`.

`/mcp/v5` adds `list_commands`, safe Retry keys and, when enabled locally,
ten browser tools with clean/persistent/personal modes. See
[browser setup and verification](BROWSER_GUIDE.ru.md). v3/v4 remain compatible.

For your signed-in Chrome/Edge profile, enable **permanent access** once in the
WebCodex Local Browser extension. It reconnects automatically after browser or
agent restarts and makes website tabs available without per-tab approval.
Local pause persists; Disconnect disables the saved grant. Tab control attaches
on demand, and personal sessions can be reused across chats.

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

The agent console shows command/argv, cwd, session ID, PID, timeout, log paths,
and incoming stdout/stderr by default. Output chunks are quoted to escape
terminal control characters. Active processes emit `RUNNING` every 10 seconds;
completion emits `EXIT` with status and exit code, independently of polling.
Start failures, stop requests and disk log limits are also reported.
Console output continues after the disk log budget is exhausted.

Set `WEBCODEX_VERBOSE=0` to hide commands and output while retaining lifecycle
events. Environment values and stdin are not automatically logged, but commands
and program output can contain secrets. Restored processes report status and
elapsed time; use `poll_command` for their output. Their exit code may be unknown.

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

See [the development plan](EXECUTION_TRANSPARENCY.ru.md) for the minimal and
expanded versions.

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
