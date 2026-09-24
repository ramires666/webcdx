# Как обновить сервер

Ниже предполагается, что проект уже клонирован на сервер и запускается через Docker Compose. Замените `USER` и `/opt/webcdx` на свои значения.

1. Отправьте изменения в Git:

   ```powershell
   git add -A
   git commit -m "MCP v4"
   git push
   ```

2. Подключитесь к серверу и откройте каталог проекта:

   ```bash
   ssh USER@85.92.121.220
   cd /opt/webcdx
   ```

3. Сделайте копию базы, получите изменения и пересоберите Gate:

   ```bash
   docker compose exec -T gate cp /data/webcodex.db /data/webcodex.db.bak
   git pull
   docker compose up -d --build gate
   ```

4. Проверьте запуск:

   ```bash
   docker compose ps
   curl -fsS https://codex.grom.world/.well-known/oauth-protected-resource/mcp/v4
   ```

   Последняя команда должна вернуть JSON с адресом, который заканчивается на `/mcp/v4`. Если Gate не запустился, посмотрите причину:

   ```bash
   docker compose logs --tail=50 gate
   ```

5. На рабочем компьютере перезапустите `start-agent-thunderfull.bat`. В ChatGPT удалите старый connector, создайте новый с адресом `https://codex.grom.world/mcp/v4` и начните новый чат.

После обновления замените показанный ранее agent token: создайте новый token в `/admin`, внесите его в локальный BAT-файл и удалите старый.
