# WebCodex: локальный MCP для файлов и команд

WebCodex публикует явные операции с файлами и процессами локальных Windows/Linux-компьютеров через единый MCP Gate. Gate сохраняет OAuth, SQLite, маршрутизацию по агентам и политику инструментов; локальный agent устанавливает только исходящее соединение.

English: [README.md](README.md) · Пошаговый запуск: [USER_GUIDE.ru.md](USER_GUIDE.ru.md)

```text
MCP-клиент ── HTTPS + OAuth ──> Gate ── исходящий NDJSON ──> Local agent
                                │                            │
                                └─ SQLite, policy            └─ файлы, процессы, логи
```

## Контракт инструментов

`tools/list` содержит ровно семь инструментов:

| Инструмент | Операция |
| --- | --- |
| `read_file` | Чтение UTF-8 файла или диапазона строк. |
| `write_file` | Атомарная замена файла точным содержимым. |
| `list_directory` | Нерекурсивный листинг каталога. |
| `search_files` | Поиск подстроки или регулярного выражения по текстовым файлам. |
| `exec_command` | Запуск неинтерактивной команды с потоковой записью ограниченного лога. |
| `poll_command` | Чтение новых байт лога и статуса процесса. |
| `cancel_command` | Завершение дерева процессов. |

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

Основные endpoint: `/mcp`, `/oauth/authorize`, `/oauth/token`, `/agent/stream`, `/agent/result`, `/admin`, `/healthz`. Gate должен работать за HTTPS. OAuth требует PKCE S256 и известный callback ChatGPT. Срок access token задаёт `WEBCODEX_ACCESS_TOKEN_TTL`, по умолчанию 24 часа.

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

Вывод команды с первой секунды пишется в `<UTC timestamp>-<session_id>.log`, а MCP-ответ содержит ограниченный хвост. Завершённые сессии и логи по умолчанию удаляются через 24 часа. При остановке agent активные деревья процессов отменяются.

## Безопасность и обновление

`exec_command` — удалённое выполнение команд с правами пользователя локального agent. Запретите его в `DeniedTools`, если shell не нужен, и не запускайте agent от Administrator/root. Ограничения файловых корней не ограничивают shell.

Перед обновлением сделайте резервную копию SQLite и бинарников. Gate и agent можно откатывать независимо: схема БД не менялась. После изменения tool schema переподключите MCP, используйте `/mcp/v3` и начните новый чат, чтобы не использовать закешированный список инструментов.
