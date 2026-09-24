# WebCodex: локальный MCP для файлов и команд

WebCodex публикует явные операции с файлами и процессами локальных Windows/Linux-компьютеров через единый MCP Gate. Gate сохраняет OAuth, SQLite, маршрутизацию по агентам и политику инструментов; локальный agent устанавливает только исходящее соединение.

English: [README.md](README.md) · Пошаговый запуск: [USER_GUIDE.ru.md](USER_GUIDE.ru.md) · [Как обновить сервер](UPDATE_SERVER.ru.md)

```text
MCP-клиент ── HTTPS + OAuth ──> Gate ── исходящий NDJSON ──> Local agent
                                │                            │
                                └─ SQLite, policy            └─ файлы, процессы, логи
```

## Контракт инструментов

`/mcp/v4` публикует ровно одиннадцать инструментов. На время миграции `/mcp/v3` сохраняет прежний контракт из семи инструментов.

| Инструмент | Операция |
| --- | --- |
| `read_file` | Чтение UTF-8 файла или диапазона строк. |
| `write_file` | Атомарное создание или замена файла с обязательным предусловием. |
| `list_directory` | Нерекурсивный листинг каталога. |
| `search_files` | Поиск подстроки или регулярного выражения по текстовым файлам. |
| `exec_command` | Запуск shell-команды или argv с env/stdin и раздельными ограниченными логами. |
| `poll_command` | Чтение новых байт stdout/stderr и статуса процесса. |
| `cancel_command` | Завершение дерева процессов. |
| `edit_file` | Точные замены с проверкой SHA-256 полного файла. |
| `find_files` | Рекурсивный поиск файлов и каталогов по glob имени. |
| `move_path` | Переименование обычного файла или пустого каталога без перезаписи. |
| `delete_path` | Удаление файла с проверкой хеша или пустого каталога. |

Пути и рабочий каталог команды должны быть абсолютными. Нет разбора задач на естественном языке, истории диалога, интерактивного терминала и алиасов инструментов.

## Сборка и проверка

Нужен Go 1.25 или новее.

```powershell
go test -count=1 ./...
go vet ./...
go build -o webcodex-agent.exe ./cmd/agent
go build -o bin/webcodex-gate.exe ./cmd/gate
```

Скрипты `build.bat` и `build.sh` собирают оба бинарника.

## Gate

Скопируйте `.env.example` в `.env`, задайте сложный пароль администратора и запустите:

```bash
docker compose up -d --build
```

Текущий endpoint коннектора — `/mcp/v4`; `/mcp/v3` сохранён для отката. Остальные основные endpoint: `/oauth/authorize`, `/oauth/token`, `/agent/stream`, `/agent/result`, `/admin`, `/healthz`. Gate должен работать за HTTPS. OAuth требует PKCE S256 и известный callback ChatGPT. Срок access token задаёт `WEBCODEX_ACCESS_TOKEN_TTL`, по умолчанию 24 часа.

Создайте agent в `/admin` и сразу сохраните одноразово показанные agent token и OAuth secret. В SQLite сохраняются только SHA-256 хеши.

## Локальный agent

Пример для Windows PowerShell:

```powershell
$env:WEBCODEX_GATE_URL = "https://example.com"
$env:WEBCODEX_AGENT_TOKEN = "<токен из /admin>"
$env:WEBCODEX_ALLOWED_ROOTS = "C:\projects;D:\work"
$env:WEBCODEX_LOG_DIR = "$PSScriptRoot\logs"
./webcodex-agent.exe
```

Пример Linux environment file:

```env
WEBCODEX_GATE_URL=https://example.com
WEBCODEX_AGENT_TOKEN=<токен из /admin>
WEBCODEX_ALLOWED_ROOTS=/srv/projects:/opt/work
WEBCODEX_LOG_DIR=/var/log/webcodex-agent
WEBCODEX_PROCESS_TTL=24h
WEBCODEX_MAX_LOG_BYTES=52428800
```

По умолчанию разрешена только директория запуска agent. Значение `WEBCODEX_ALLOWED_ROOTS=*` сознательно открывает все пути. Перед проверкой разрешённых корней symlink/reparse path разрешается до фактического пути.

Каждая command-сессия хранится в `<log-dir>/<session-id>/`: `metadata.json`, `stdout.log`, `stderr.log`. В metadata нет текста команды, stdin и значений environment. Завершённые сессии доступны после перезапуска agent и удаляются через 24 часа. Активная сессия восстанавливается только при совпадении сохранённой идентичности PID.

## Безопасность и обновление

`exec_command` — удалённое выполнение команд с правами пользователя локального agent. `WEBCODEX_ALLOWED_ROOTS` защищает только файловые инструменты и не ограничивает shell. Запрещайте `exec_command` и `cancel_command`, если они не нужны. У новых агентов `edit_file`, `move_path` и `delete_path` запрещены до явного включения.

Запускайте agent под отдельной непривилегированной учётной записью. В Windows выдайте ей NTFS-права только на рабочие каталоги и каталог логов. В Linux настройте `ReadWritePaths` в `deploy/webcodex-agent.service`; unit уже включает `NoNewPrivileges`, `PrivateTmp` и `ProtectSystem`. Если сборкам не нужны загрузки, ограничьте исходящую сеть средствами ОС или контейнера.

Перед обновлением сохраните SQLite, бинарники, service-файлы и command-логи. Сначала разверните Gate с v3/v4, затем замените agent. Создайте новый connector на `/mcp/v4` и начните новый чат. Для отката переключите connector на `/mcp/v3` и верните предыдущий agent binary.
