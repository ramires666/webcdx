# Как обновить WebCodex

Сначала сервер (Gate), затем локальный агент. Дождитесь завершения важных команд агента.

## Сервер — Linux, Docker Compose

Выполняйте в **существующей папке установки** (обычно `/opt/webcdx`):

```bash
cd /opt/webcdx
git pull --ff-only
docker compose build gate
docker compose stop gate
backup="backups/$(date +%Y%m%d-%H%M%S)"
mkdir -p "$backup"
chmod 700 "$backup"
docker cp webcodex-gate:/data "$backup/data"
cp .env "$backup/.env"
docker compose up -d --no-deps gate
docker compose ps
curl -fsS http://127.0.0.1:38971/healthz
```

Выполняйте команды по очереди; при ошибке остановитесь. Если резервная копия не создалась,
запустите прежний контейнер: `docker compose start gate`.
База, агенты, токены и пароль сохраняются в прежнем Docker volume и `.env`.
Не удаляйте volume и не используйте `docker compose down -v`.

## Локально — Windows

1. Остановите агент через **Ctrl+C** в его окне.
2. В PowerShell выполните:

```powershell
cd W:\_python\webcdx
git pull --ff-only
.\build.bat
.\start-agent-jinn.bat
```

При ошибке сборки не запускайте агент. Если ваш запускной файл называется иначе,
запустите свой `start-agent-*.bat`. Для сборки нужен Go 1.25 или новее.
Готовый агент: `webcodex-agent.exe`; Gate для Windows: `bin/webcodex-gate.exe`;
расширение: `bin/webcodex-browser-extension.zip`. Сохраните запускные файлы с токенами и `logs`.

Локальный Linux: остановите агент, выполните `git pull --ff-only` и `bash build.sh`,
затем запустите новый `./webcodex-agent` с прежними настройками.

## Подключение и браузер

Проверьте статус агента в `https://codex.grom.world/admin`.
Для новых функций используйте адрес `https://codex.grom.world/mcp/v5` и начните новый чат.
Если ваш домен другой, подставьте его. Существующие OAuth client ID и secret сохраняются.

Для браузерных инструментов добавьте перед запуском агента в свой BAT:
`set "WEBCODEX_BROWSER_ENABLED=1"`.
Для личных вкладок также добавьте `set "WEBCODEX_BROWSER_PERSONAL_ENABLED=1"`,
выполните `.\webcodex-agent.exe --install-browser-host` и загрузите папку
`browser-extension` в Chrome/Edge через режим разработчика.
Уже установленное расширение обновите на странице расширений; подключите вкладку заново.
Подробнее: [настройка браузера](BROWSER_GUIDE.ru.md).
