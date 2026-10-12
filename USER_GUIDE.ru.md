# Запуск WebCodex за три шага

## 1. Создайте локальный agent

Откройте `https://<ваш-домен>/admin`, создайте agent и сохраните выданные agent token, OAuth Client ID и OAuth Client Secret. Секреты повторно не показываются; при потере выполните ротацию.

В карточке agent настройте `AllowedTools`/`DeniedTools`. Если выполнение команд не требуется, добавьте `exec_command,cancel_command` в `DeniedTools`. У новых агентов `edit_file`, `move_path` и `delete_path` уже запрещены; удаляйте нужные имена из `DeniedTools` явно.

## 2. Запустите бинарник на рабочем компьютере

Windows PowerShell:

```powershell
$env:WEBCODEX_GATE_URL = "https://<ваш-домен>"
$env:WEBCODEX_AGENT_TOKEN = "<agent token>"
$env:WEBCODEX_ALLOWED_ROOTS = "C:\projects;D:\work"
$env:WEBCODEX_LOG_DIR = "$PSScriptRoot\logs"
./webcodex-agent.exe
```

Linux:

```bash
export WEBCODEX_GATE_URL="https://<ваш-домен>"
export WEBCODEX_AGENT_TOKEN="<agent token>"
export WEBCODEX_ALLOWED_ROOTS="/srv/projects:/opt/work"
export WEBCODEX_LOG_DIR="/var/log/webcodex-agent"
./webcodex-agent
```

Не запускайте agent от администратора/root. В `/admin` статус должен стать `ONLINE`.

## 3. Подключите MCP

В настройке MCP/Connected App укажите:

| Поле | Значение |
| --- | --- |
| Server URL | `https://<ваш-домен>/mcp/v5` |
| Authorization URL | `https://<ваш-домен>/oauth/authorize` |
| Token URL | `https://<ваш-домен>/oauth/token` |
| Client ID | значение из `/admin` |
| Client Secret | значение из `/admin` |
| OAuth | Authorization Code + PKCE S256 |

Это MCP endpoint, а не OpenAPI schema. После подключения начните новый чат:
v5 предоставляет двенадцать инструментов (одиннадцать операций v4 плюс
`list_commands`), а при `WEBCODEX_BROWSER_ENABLED=1` — ещё десять браузерных.
Tool policy может скрывать часть списка. `exec_command` v5 требует `request_key`.
Для личных вкладок и установки расширения следуйте [инструкции браузера](BROWSER_GUIDE.ru.md).
Для отката остаются `/mcp/v4` и `/mcp/v3`.

## Диагностика

Для независимого наблюдения откройте `/admin`, выберите агент и нажмите
«Мониторинг команд и журналы». Можно искать сессии, читать stdout/stderr,
события и скачивать логи. Остановка требует права `cancel_command`.
Логи могут содержать секреты, которые напечатала программа.

Для надёжного Retry используйте `/mcp/v5` и уникальный `request_key` в
`exec_command`. После потери ответа повторите тот же запрос с тем же ключом
или найдите сессию через `list_commands`, затем читайте `poll_command`.
`outcome_unknown` означает, что результат запуска неизвестен: автоматически
повторять действие нельзя. Защита действует пока хранится сессия (24 часа
по умолчанию); она не гарантирует бесконечный ответ самого ChatGPT.

- `OFFLINE`: проверьте `WEBCODEX_GATE_URL`, agent token, HTTPS и доступ к `/agent/stream`.
- `path is outside WEBCODEX_ALLOWED_ROOTS`: добавьте нужный абсолютный корень и перезапустите agent.
- Долгая команда: используйте `session_id` и раздельные offsets с `poll_command`; полный вывод остаётся в `stdout_log_path` и `stderr_log_path`.
- Потерян секрет: выполните ротацию в `/admin`; старые токены перестанут работать.
- После смены инструментов всё ещё виден старый список: создайте новый connector на `/mcp/v5` и начните новый чат.
