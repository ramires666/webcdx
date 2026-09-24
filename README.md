# WebCodex Local Workspace MCP

WebCodex exposes explicit file and process operations from local Windows/Linux machines through one public MCP gate. The gate keeps OAuth, SQLite, per-machine routing, and tool policy; each local agent makes only an outbound HTTP connection.

Russian documentation: [README.ru.md](README.ru.md) · [User guide](USER_GUIDE.ru.md)

```text
MCP client ── HTTPS + OAuth ──> Gate ── outbound NDJSON stream ──> Local agent
                                │                                  │
                                └─ SQLite, routing, policy          └─ files, processes, logs
```

## Tools

`tools/list` contains exactly:

| Tool | Operation |
| --- | --- |
| `read_file` | Read a UTF-8 file or line range. |
| `write_file` | Atomically replace a file with exact content. |
| `list_directory` | List one directory without recursion. |
| `search_files` | Search text files by substring or optional regular expression. |
| `exec_command` | Start a non-interactive shell command and stream output to a bounded log. |
| `poll_command` | Read new log bytes and process status. |
| `cancel_command` | Terminate the process tree. |

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

The important endpoints are `/mcp`, `/oauth/authorize`, `/oauth/token`, `/agent/stream`, `/agent/result`, `/admin`, and `/healthz`. Put the gate behind HTTPS. OAuth requires PKCE S256 and a recognized ChatGPT callback URI. Access tokens expire after `WEBCODEX_ACCESS_TOKEN_TTL` (24 hours by default).

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

Command output is written from process start to `<UTC timestamp>-<session_id>.log`; a call returns only a bounded tail. Finished sessions and logs expire after 24 hours by default. The agent cancels active process trees during shutdown.

## Security

`exec_command` is remote code execution with the operating-system rights of the local agent. Deny it per agent unless it is needed, and do not run the agent as Administrator/root. File root restrictions do not constrain shell commands.

Back up the SQLite database before deployment. Gate and agent binaries can be rolled back independently because this release does not change the database schema. After changing the tool schema, reconnect the MCP integration or use `/mcp/v3` and start a new conversation to avoid a cached tool list.
