# Запуск WebCodex за три шага

## 1. Создайте локальный agent

Откройте `https://<ваш-домен>/admin`, создайте agent и сохраните выданные agent token, OAuth Client ID и OAuth Client Secret. Секреты повторно не показываются; при потере выполните ротацию.

В карточке agent настройте `AllowedTools`/`DeniedTools`. Если выполнение команд не требуется, добавьте `exec_command` в `DeniedTools`.

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
| Server URL | `https://<ваш-домен>/mcp/v3` |
| Authorization URL | `https://<ваш-домен>/oauth/authorize` |
| Token URL | `https://<ваш-домен>/oauth/token` |
| Client ID | значение из `/admin` |
| Client Secret | значение из `/admin` |
| OAuth | Authorization Code + PKCE S256 |

Это MCP endpoint, а не OpenAPI schema. После переподключения начните новый чат и убедитесь, что доступны семь инструментов: `read_file`, `write_file`, `list_directory`, `search_files`, `exec_command`, `poll_command`, `cancel_command`.

## Диагностика

- `OFFLINE`: проверьте `WEBCODEX_GATE_URL`, agent token, HTTPS и доступ к `/agent/stream`.
- `path is outside WEBCODEX_ALLOWED_ROOTS`: добавьте нужный абсолютный корень и перезапустите agent.
- Долгая команда: используйте `session_id` с `poll_command`; полный вывод остаётся в файле из `log_path`.
- Потерян секрет: выполните ротацию в `/admin`; старые токены перестанут работать.
- После смены инструментов всё ещё виден старый список: подключитесь к `/mcp/v3` заново и начните новый чат.
