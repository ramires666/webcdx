# WebCodex Agentic Coding Development Plan

## 1. Goal

Extend the local MCP server from a basic file-and-command executor into a safe,
reliable coding-agent workspace without restoring prompt parsing, a second AI
engine, or a large IDE abstraction layer.

The target contract contains the existing seven tools plus four new tools:

1. `read_file`
2. `write_file`
3. `list_directory`
4. `search_files`
5. `exec_command`
6. `poll_command`
7. `cancel_command`
8. `edit_file`
9. `find_files`
10. `move_path`
11. `delete_path`

The implementation must remain deterministic: every filesystem mutation is
explicit, every path is absolute, and arbitrary prose is never interpreted as
a command or filename.

## 2. Success criteria

The work is complete when:

- an agent can discover, read, edit, create, move, and delete source files
  without requiring shell access;
- normal edits do not rewrite an entire file and cannot silently overwrite a
  newer version;
- long-running commands remain inspectable after an agent reconnect or restart;
- direct commands can use an argument vector, environment overrides, and stdin;
- stdout and stderr are available separately;
- all eleven tools publish exact input and output schemas;
- command execution is documented and deployed as an operating-system security
  boundary, not falsely protected by `WEBCODEX_ALLOWED_ROOTS`;
- Gate policy can independently allow or deny every destructive tool;
- unit, security, reconnect, and real Gate-to-agent E2E tests pass;
- a fresh connector and new chat see the `/mcp/v4` schema.

## 3. Non-goals

Do not add:

- another local AI/model runtime;
- natural-language command or patch parsing;
- custom Git tools while Git works through `exec_command`;
- a custom LSP client, compiler protocol, terminal emulator, or package manager;
- recursive deletion in the first release;
- a unified-diff parser unless exact replacement proves insufficient in real
  usage.

## 4. Shared filesystem contract

All filesystem tools must:

- accept absolute paths only;
- enforce `WEBCODEX_ALLOWED_ROOTS` after cleaning paths and resolving existing
  symlink prefixes;
- reject traversal and symlink escapes;
- use UTF-8 text unless a tool explicitly documents binary support;
- return structured errors without file contents or secrets in action logs;
- respect request deadlines and response-size limits;
- publish `additionalProperties: false` input schemas;
- publish exact output schemas instead of a generic object;
- use lowercase SHA-256 hex strings for optimistic concurrency checks;
- return normalized absolute paths in results.

Every read of a regular file returns:

```json
{
  "path": "C:\\workspace\\main.go",
  "content": "...",
  "sha256": "...",
  "size": 1234,
  "offset": 1,
  "line_count": 40,
  "next_offset": 41,
  "truncated": false
}
```

The hash always covers the complete file, even when only a line range is
returned.

## 5. New tool: `edit_file`

### Purpose

Apply small deterministic text replacements without sending or rewriting an
entire source file. Detect concurrent edits before writing.

### Input

```json
{
  "path": "C:\\workspace\\main.go",
  "expected_sha256": "current complete-file hash",
  "edits": [
    {
      "old_text": "old exact text",
      "new_text": "new exact text",
      "replace_all": false
    }
  ]
}
```

Rules:

- `path`, `expected_sha256`, and a non-empty `edits` array are required;
- edits are applied in array order to an in-memory copy;
- `replace_all` defaults to `false`;
- without `replace_all`, `old_text` must occur exactly once at that step;
- with `replace_all`, `old_text` must occur at least once;
- empty `old_text` is rejected;
- empty `new_text` is valid;
- the current complete-file hash must match `expected_sha256`;
- if any edit fails, nothing is written;
- the final write uses a temporary file in the same directory, sync, close, and
  atomic rename, matching `write_file` durability;
- the normal request/body/file-size limits apply.

### Output

```json
{
  "path": "C:\\workspace\\main.go",
  "old_sha256": "...",
  "new_sha256": "...",
  "replacements": 2,
  "bytes_written": 1300
}
```

Annotations: `readOnlyHint=false`, `destructiveHint=true`,
`openWorldHint=false`.

## 6. New tool: `find_files`

### Purpose

Discover files by name without searching their contents or depending on shell
access.

### Input

```json
{
  "path": "C:\\workspace",
  "name_glob": "*.go",
  "kind": "file",
  "max_results": 1000
}
```

Rules:

- `path` is a required directory;
- `name_glob` defaults to `*` and matches the basename using Go's standard glob
  syntax;
- `kind` is one of `file`, `directory`, or `any`, defaulting to `file`;
- traversal is recursive and deterministic;
- results are sorted case-insensitively by normalized relative path;
- configured service/log/data directories are skipped consistently with
  `search_files`;
- symlinked directories are reported when requested but never followed;
- traversal stops at `max_results` or the response byte limit and reports
  `truncated=true`;
- per-path access errors are returned in `errors` rather than aborting the entire
  walk.

### Output

```json
{
  "path": "C:\\workspace",
  "entries": [
    {
      "path": "C:\\workspace\\cmd\\agent\\main.go",
      "relative_path": "cmd/agent/main.go",
      "type": "file",
      "size": 1234,
      "modified_at": "2026-01-01T00:00:00Z"
    }
  ],
  "errors": [],
  "truncated": false
}
```

Annotations: `readOnlyHint=true`, `destructiveHint=false`,
`openWorldHint=false`.

## 7. New tool: `move_path`

### Purpose

Rename a file or directory inside allowed roots without shell access.

### Input

```json
{
  "source": "C:\\workspace\\old.go",
  "destination": "C:\\workspace\\new.go",
  "expected_sha256": "hash for a regular file"
}
```

Rules:

- both paths are required and independently checked against allowed roots;
- the destination must not already exist;
- a regular-file source requires `expected_sha256`;
- an empty directory may be moved without a hash;
- symlinks are not accepted as sources in the first release;
- use `os.Rename`; do not silently fall back to copy-and-delete across volumes;
- create no destination parent directories implicitly;
- source and destination must be different after normalization.

### Output

```json
{
  "source": "C:\\workspace\\old.go",
  "destination": "C:\\workspace\\new.go",
  "type": "file"
}
```

Annotations: `readOnlyHint=false`, `destructiveHint=true`,
`openWorldHint=false`.

## 8. New tool: `delete_path`

### Purpose

Delete a known file or an empty directory without granting general shell
execution.

### Input

```json
{
  "path": "C:\\workspace\\obsolete.go",
  "expected_sha256": "hash for a regular file"
}
```

Rules:

- regular files require a matching `expected_sha256`;
- empty directories may be deleted without a hash;
- non-empty directories are rejected;
- symlinks are rejected in the first release;
- missing paths return a clear error rather than pretending success;
- recursive deletion is intentionally unsupported;
- the action log records only the normalized path and result, never contents.

### Output

```json
{
  "path": "C:\\workspace\\obsolete.go",
  "deleted": true,
  "type": "file"
}
```

Annotations: `readOnlyHint=false`, `destructiveHint=true`,
`openWorldHint=false`.

## 9. Existing filesystem tool upgrades

### `read_file`

- add complete-file `sha256` and `size` to every successful response;
- retain line-range behavior and UTF-8-safe response truncation;
- distinguish `not_found`, `not_file`, `binary_file`, `outside_allowed_roots`,
  and `too_large` error codes.

### `write_file`

Add two mutually exclusive preconditions:

- `if_absent: true` for creating a new file;
- `expected_sha256` for replacing an existing file.

On `/mcp/v4`, one precondition is required. This prevents an agent from silently
overwriting a file it has not read. Empty content remains valid.

### `list_directory` and `search_files`

- publish exact result schemas;
- return stable error codes;
- share directory-skip and symlink-handling helpers with `find_files`;
- do not add overlapping filename-discovery modes to `search_files`.

## 10. Command tool upgrades

### `exec_command`

Accept exactly one execution form:

```json
{
  "argv": ["go", "test", "./..."],
  "cwd": "C:\\workspace",
  "env": {"GOFLAGS": "-count=1"},
  "stdin": "",
  "timeout_seconds": 1200,
  "yield_time_ms": 10000
}
```

or the existing shell form:

```json
{
  "command": "go test ./...",
  "cwd": "C:\\workspace",
  "timeout_seconds": 1200,
  "yield_time_ms": 10000
}
```

Rules:

- `argv` bypasses shell parsing and is the preferred portable form;
- `command` retains PowerShell on Windows and `/bin/sh` on Unix;
- `env` overlays the agent process environment; invalid names and NUL bytes are
  rejected;
- `stdin` is size-limited, written once, and then closed;
- command strings, stdin, environment values, and output are never copied into
  action logs;
- stdout and stderr write continuously to separate capped log files;
- the combined size of both logs is limited by `WEBCODEX_MAX_LOG_BYTES`;
- the result reports truncation independently for stdout and stderr.

### `poll_command`

Replace the single stream offset on `/mcp/v4` with:

```json
{
  "session_id": "...",
  "stdout_offset": 0,
  "stderr_offset": 0,
  "max_bytes": 65536
}
```

Return `stdout`, `stderr`, their next offsets, status, exit code, timestamps,
and log paths. Split the response byte allowance fairly when both streams have
new data.

### `cancel_command`

- retain idempotent process-tree termination;
- allow cancellation after agent restart only when the persisted PID/process
  identity still matches;
- return `already_finished=true` for completed sessions.

## 11. Durable command sessions

Store each session under the configured log directory:

```text
<log-dir>/<session-id>/
  metadata.json
  stdout.log
  stderr.log
```

`metadata.json` contains no command text, stdin, environment values, or secrets.
It records:

- session ID;
- cwd;
- PID and process-group/job identity;
- status;
- start and finish timestamps;
- exit code;
- timeout deadline;
- log truncation flags.

Write metadata through temp-file plus atomic rename whenever state changes. On
startup:

1. scan session directories newer than the TTL;
2. restore completed sessions for polling;
3. verify the identity of processes marked running;
4. restore a verified process as `running`;
5. mark an unverifiable process `orphaned` while keeping its logs readable;
6. never kill a reused PID whose identity does not match.

TTL cleanup removes completed/orphaned session directories only after the
configured retention period. Active verified processes are never removed.

## 12. Command security boundary

`WEBCODEX_ALLOWED_ROOTS` protects filesystem tools; it is not a sandbox for a
shell process. Documentation and admin warnings must say this explicitly.

Minimum production boundary:

- run the local agent under a dedicated unprivileged OS account;
- grant that account access only to intended workspaces and the command log
  directory;
- keep Gate policy deny-by-default for `exec_command` and `cancel_command`;
- expose separate policy switches for `edit_file`, `move_path`, and
  `delete_path`;
- on Linux, strengthen the service with `NoNewPrivileges`, `PrivateTmp`,
  `ProtectSystem`, and explicit `ReadWritePaths`;
- on Windows, document a dedicated local account with matching NTFS ACLs and
  continue using Job Objects/process-tree termination;
- add optional deployment-level network restrictions when builds do not need
  downloads.

Do not claim that application path validation can contain arbitrary shell
commands.

## 13. Exact MCP schemas

Replace the shared generic output schema with one schema per tool. Each schema
must declare:

- required fields;
- property types and bounds;
- status/error enums where applicable;
- `additionalProperties: false` for stable result objects;
- annotations matching actual read/destructive behavior.

Keep the textual MCP content and `structuredContent` representations derived
from the same result object so they cannot disagree.

Add contract tests that serialize `tools/list` and compare all eleven names,
input schemas, output schemas, and annotations.

## 14. Gate and policy changes

- add the four tools to the Gate policy model and admin UI;
- default `find_files` to allowed wherever read tools are allowed;
- default `edit_file`, `move_path`, and `delete_path` to denied until explicitly
  enabled for an agent;
- continue filtering denied tools out of `tools/list`;
- reject denied calls before they enter the agent queue;
- preserve deadlines and body limits;
- ensure action logs contain tool name, normalized path where safe, duration,
  status, and byte counts only;
- never log replacement text, command strings, stdin, environment values, or
  command output.

SQLite schema changes are unnecessary if policy is stored as the existing
tool-name set. If migrations become necessary, they must be additive and
backward-compatible.

## 15. Endpoint and compatibility strategy

Publish the expanded and stricter contract at `/mcp/v4` because connector tool
schemas may be cached.

- keep `/mcp/v3` during rollout and rollback;
- `/mcp/v3` continues exposing the original seven-tool schemas;
- `/mcp/v4` exposes all eleven tools and the safer write/poll contracts;
- agent internals may support both schemas through thin request adapters;
- do not keep two executor implementations;
- remove `/mcp/v3` only after all connectors have migrated and a documented
  deprecation window has passed.

## 16. Tests

### 16.1 Contract tests

- `/mcp/v4` lists exactly eleven tools;
- every input and output schema rejects unknown properties;
- all destructive/read-only annotations are correct;
- forbidden AI/model/prompt terminology remains absent from MCP-visible JSON;
- `/mcp/v3` retains its seven-tool compatibility contract.

### 16.2 `edit_file`

- one exact replacement;
- multiple ordered replacements;
- deletion through empty `new_text`;
- `replace_all` count;
- duplicate match rejected without `replace_all`;
- missing match leaves the file unchanged;
- stale hash leaves the file unchanged;
- Unicode path and content;
- atomicity when a later edit fails;
- root and symlink escape rejection.

### 16.3 Discovery and path mutations

- deterministic recursive `find_files` results;
- basename glob and kind filtering;
- truncation and access errors;
- symlink directories are not followed;
- file move with matching hash;
- existing destination rejected;
- cross-volume move rejected without copy/delete;
- file deletion with matching hash;
- stale hash rejected;
- empty directory deletion succeeds;
- recursive/non-empty directory deletion is rejected.

### 16.4 Commands and persistence

- argv execution preserves arguments containing spaces and shell characters;
- shell execution remains compatible;
- environment overlay and stdin work without appearing in action logs;
- stdout and stderr are separated and incrementally polled;
- either stream can exceed the cap without unbounded RAM growth;
- nonzero exit, timeout, cancel, and process-tree termination;
- agent restart restores completed-session polling;
- agent restart restores or safely marks a running session orphaned;
- a reused PID is never cancelled;
- TTL cleanup preserves active sessions and removes expired completed sessions.

### 16.5 Security and E2E

The real Gate-to-agent test must cover:

1. OAuth token and `/agent/stream` connection.
2. `/mcp/v4` initialize and exact `tools/list`.
3. `write_file` with `if_absent`.
4. `read_file` and captured hash.
5. `edit_file` with that hash.
6. stale-hash edit rejection.
7. `find_files` discovery.
8. `move_path` and `delete_path`.
9. argv command with distinct stdout/stderr.
10. long command, polling, reconnect, and final logs.
11. agent restart and continued session inspection.
12. policy denial proving a destructive call never reaches the workstation.

All long checks write stdout/stderr to files under `tmp/`.

## 17. Implementation phases

### Phase 1: lock the contract

- add `/mcp/v4` routing;
- add failing exact-eleven-tool schema tests;
- add precise result-schema types;
- keep runtime behavior unchanged.

Exit criterion: failures identify only the missing v4 contract.

### Phase 2: safe editing and concurrency

- add complete-file hashing to `read_file`;
- add v4 preconditions to `write_file`;
- implement `edit_file` with atomic replacement;
- add focused unit tests.

Exit criterion: stale reads cannot overwrite newer file content.

### Phase 3: shell-independent workspace management

- implement `find_files`;
- implement `move_path`;
- implement non-recursive `delete_path`;
- update Gate policy filtering and admin controls.

Exit criterion: a policy with `exec_command` denied can still perform a normal
source-file refactor.

### Phase 4: command contract and durable sessions

- add argv, env overlay, and stdin;
- split stdout/stderr logs and polling offsets;
- persist session metadata;
- restore sessions safely at startup;
- retain caps, deadlines, timeout, and process-tree cancellation.

Exit criterion: logs remain readable after restart and verified running
processes remain controllable.

### Phase 5: deployment security

- harden the Linux service;
- add Windows account/ACL guidance;
- update admin RCE warnings;
- add security regression tests and secret-log checks.

Exit criterion: deployment documentation identifies the OS account as the
command security boundary.

### Phase 6: E2E, documentation, and rollout

- extend the real E2E test to the full v4 workflow;
- update README files, environment examples, and recovery instructions;
- deploy Gate and agent as versioned binaries;
- connect a fresh `/mcp/v4` connector and test in a new chat;
- retain v3 binaries/configuration for rollback.

Exit criterion: the fresh connector exposes exactly eleven tools and completes
the full coding workflow.

## 18. Required verification

```powershell
go test -count=1 ./... *> tmp/agentic-go-test.log
go vet ./... *> tmp/agentic-go-vet.log
go build -o webcodex-agent.exe ./cmd/agent *> tmp/agentic-build-agent.log
go build -o bin/webcodex-gate.exe ./cmd/gate *> tmp/agentic-build-gate.log
```

Run the race detector in CI/Linux or a Windows environment with working CGO.
Preserve E2E and deployment smoke-test logs.

## 19. Rollout and rollback

1. Back up SQLite, current binaries, service files, and command logs.
2. Deploy Gate with `/mcp/v3` and `/mcp/v4` enabled.
3. Deploy the new agent under the same agent identity.
4. Verify health, OAuth metadata, ONLINE status, and the eleven-tool contract.
5. Create a new connector pointing to `/mcp/v4`.
6. Start a new chat and execute the E2E workflow.
7. Keep `/mcp/v3` available during the migration window.

Rollback by reconnecting the connector to `/mcp/v3` and restoring the previous
agent binary. Do not restore prompt parsing or regex fallbacks.

## 20. Suggested commits

1. `test(mcp): define v4 agentic coding contract`
2. `feat(agent): add hashed atomic text edits`
3. `feat(agent): add native find move and delete tools`
4. `feat(agent): add structured command execution and durable sessions`
5. `fix(gate): enforce expanded tool policy and exact schemas`
6. `test(e2e): cover v4 coding workflow and reconnects`
7. `docs: add agent sandbox deployment and v4 migration`

