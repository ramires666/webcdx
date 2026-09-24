# Чистый запуск сервера

Эти команды удалят старый контейнер и его базу с учётками:

```bash
OLD_VOLUME=$(docker inspect webcodex-gate --format '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}' 2>/dev/null)
docker rm -f webcodex-gate 2>/dev/null || true
[ -z "$OLD_VOLUME" ] || docker volume rm "$OLD_VOLUME"

git clone https://github.com/ramires666/webcdx.git /opt/webcdx
cd /opt/webcdx
cp .env.example .env

ADMIN_PASSWORD=$(openssl rand -hex 24)
sed -i "s/CHANGE_ME_TO_A_VERY_LONG_RANDOM_PASSWORD/$ADMIN_PASSWORD/" .env

docker compose up -d --build gate
docker compose ps
echo "Пароль admin: $ADMIN_PASSWORD"
```

Проверьте MCP v4:

```bash
curl -fsS https://codex.grom.world/.well-known/oauth-protected-resource/mcp/v4
```

Затем откройте `https://codex.grom.world/admin`, войдите как `admin` с показанным паролем и создайте нового agent. Внесите его token в `start-agent-thunderfull.bat`, перезапустите agent и создайте в ChatGPT connector с адресом `https://codex.grom.world/mcp/v4`.
