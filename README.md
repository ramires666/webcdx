# WebCodex Local Workspace MCP

WebCodex exposes explicit file and process operations from local Windows/Linux machines through one public MCP gate. The gate keeps OAuth, SQLite, per-machine routing, and tool policy; each local agent makes only an outbound HTTP connection.

Russian documentation: [README.ru.md](README.ru.md) · [User guide](USER_GUIDE.ru.md)

```text
MCP client ── HTTPS + OAuth ──> Gate ── outbound NDJSON stream ──> Local agent
                                │                                  │
                                └─ SQLite, routing, policy          └─ files, processes, logs
```

## Tools

`/mcp/v4` exposes exactly eleven tools. `/mcp/v3` remains available with the original seven-tool contract during migration.

| Tool | Operation |
| --- | --- |
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

The current connector endpoint is `/mcp/v4`; `/mcp/v3` is retained for rollback. The other important endpoints are `/oauth/authorize`, `/oauth/token`, `/agent/stream`, `/agent/result`, `/admin`, and `/healthz`. Put the gate behind HTTPS. OAuth requires PKCE S256 and a recognized ChatGPT callback URI. Access tokens expire after `WEBCODEX_ACCESS_TOKEN_TTL` (24 hours by default).

Create an agent in `/admin`; copy its one-time agent token and OAuth client secret immediately. Only their SHA-256 hashes are persisted.

## Local agent

Windows PowerShell example:

```powershell
$env:WEBCODEX_GATE_URL = "https://example.com"
$env:WEBCODEX_AGENT_TOKEN = "<agent token from /admin>"
$env:WEBCODEX_ALLOWED_ROOTS = "C:\projects;D:\work"
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

By default only the agent's startup directory is allowed. `WEBCODEX_ALLOWED_ROOTS=*` deliberately permits all local paths. Symlinks are resolved before access checks.

Each command session is stored under `<log-dir>/<session-id>/` with `metadata.json`, `stdout.log`, and `stderr.log`. Metadata excludes command text, stdin, and environment values. Completed sessions remain pollable after an agent restart and expire after 24 hours by default. Running sessions are restored only when the persisted PID identity still matches.

## Security

`exec_command` is remote code execution with the operating-system rights of the local agent. `WEBCODEX_ALLOWED_ROOTS` protects filesystem tools only; it cannot contain a shell process. Deny `exec_command` and `cancel_command` unless needed. New agents also deny `edit_file`, `move_path`, and `delete_path` until they are explicitly enabled.

Run the agent under a dedicated unprivileged account. On Windows, grant that account NTFS access only to the intended workspaces and log directory. On Linux, update `ReadWritePaths` in `deploy/webcodex-agent.service` to match the configured roots; the unit also enables `NoNewPrivileges`, `PrivateTmp`, and `ProtectSystem`. Restrict outbound network access at the OS or container layer when builds do not need downloads.

Back up SQLite, binaries, service files, and command logs before deployment. Deploy Gate first with v3/v4 enabled, then replace the agent binary. Create a fresh connector at `/mcp/v4` and start a new conversation so the eleven-tool schema is not read from cache. Roll back by reconnecting to `/mcp/v3` and restoring the prior agent binary.
