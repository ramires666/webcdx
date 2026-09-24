# Как обновить сервер

Старая версия содержала другой исходник, поэтому на сервере проще сделать чистый clone. База Docker и `.env` при этом сохраняются.

1. Узнайте настоящий каталог старой установки:

   ```bash
   docker inspect webcodex-gate --format '{{ index .Config.Labels "com.docker.compose.project.working_dir" }}'
   ```

   Скопируйте выведенный путь в следующую команду вместо `/путь/из/команды`:

   ```bash
   OLD_DIR="/путь/из/команды"
   BACKUP="${OLD_DIR}.old-$(date +%Y%m%d-%H%M%S)"
   ```

2. Сохраните базу и остановите старый Gate:

   ```bash
   cd "$OLD_DIR"
   docker compose exec -T gate cp /data/webcodex.db /data/webcodex.db.bak
   docker compose down
   ```

   Не добавляйте `-v`: без него Docker оставит volume с базой.

3. Уберите старую папку в резерв и клонируйте свежий проект на её место:

   ```bash
   mv "$OLD_DIR" "$BACKUP"
   git clone https://github.com/ramires666/webcdx.git "$OLD_DIR"
   cp "$BACKUP/.env" "$OLD_DIR/.env"
   ```

4. Запустите новый Gate:

   ```bash
   cd "$OLD_DIR"
   docker compose up -d --build gate
   docker compose ps
   curl -fsS https://codex.grom.world/.well-known/oauth-protected-resource/mcp/v4
   ```

   Последняя команда должна вернуть JSON с адресом, который заканчивается на `/mcp/v4`. При ошибке покажите журнал:

   ```bash
   docker compose logs --tail=50 gate
   ```

5. На рабочем компьютере перезапустите `start-agent-thunderfull.bat`. В ChatGPT удалите старый connector, создайте новый с адресом `https://codex.grom.world/mcp/v4` и начните новый чат.

После проверки новую папку можно оставить, а `$BACKUP` удалить. Также замените ранее показанный agent token: создайте новый token в `/admin`, внесите его в локальный BAT-файл и удалите старый.
