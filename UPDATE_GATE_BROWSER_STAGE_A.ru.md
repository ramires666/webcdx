# Обновление Gate через Git и Docker Compose

Для тестовой поставки агента `20c6fff` и расширения **1.3.0**.
Дата: 2026-10-25, Asia/Qyzylorda.

## Нужно ли обновлять Gate

Именно этап очередей и отмены **не менял код Gate**: между `7c1fd15` и `20c6fff`
файлы `cmd/gate`, `internal`, Dockerfile.gate и docker-compose.yml одинаковы.
Схемы инструментов Gate получает от агентов. Если сервер уже работает на предыдущей
поставке `7c1fd15`, для новых очередей достаточно обновить агент и расширение,
а затем обновить список MCP-инструментов. Агенты одного MCP-подключения должны
иметь совместимые схемы; старые и новые агенты могут дать `incompatible schemas`.

Если сервер давно не обновлялся или нужна единая актуальная поставка, выполните
инструкцию ниже. Docker соберёт Gate **из Git на сервере**; переносить Windows EXE
или локальный Linux-бинарник в контейнер не нужно.

## Что используется в этом репозитории

| Параметр | Значение |
| --- | --- |
| Репозиторий | https://github.com/ramires666/webcdx.git |
| Ветка | main |
| Compose-файл / сервис | docker-compose.yml / gate |
| Dockerfile | Dockerfile.gate |
| Контейнер | webcodex-gate |
| Порт / проверка | 38971 / http://127.0.0.1:38971/healthz |
| Данные | именованный volume webcodex_data, в контейнере /data |
| База | /data/webcodex.db |
| Настройки | существующий .env на сервере |

Команды предназначены для **уже установленного** сервера Linux с Docker Compose.
Выполняйте их через SSH от пользователя с доступом к Docker и папке установки.
Если Docker требует sudo, используйте административную сессию для всего блока.
Не клонируйте репозиторий в новую папку: другой Compose project может создать
пустой volume вместо подключения прежней базы.

## 1. Найти существующую установку

На сервере выполните:

```bash
docker inspect webcodex-gate --format '{{index .Config.Labels "com.docker.compose.project.working_dir"}}'
docker inspect webcodex-gate --format '{{index .Config.Labels "com.docker.compose.project"}}'
docker inspect webcodex-gate --format '{{index .Config.Labels "com.docker.compose.project.config_files"}}'
```

Первая строка — существующая папка установки, часто `/opt/webcdx`.
Третья должна указывать на её `docker-compose.yml`. Если у вас несколько Compose
файлов или нестандартные overrides, основной блок ниже не запускайте без адаптации
к этим файлам. Содержимое `.env` выводить или отправлять в чат не нужно.

Дождитесь завершения важных операций агентов. Во время замены Gate связь
прервётся; агенты автоматически переподключатся после запуска.

## 2. Получить код, собрать образ и заменить Gate

Скопируйте **весь блок целиком** в серверную Bash-сессию. Он сам определяет прежнюю
папку и Compose project, сохраняет старый образ и конфигурацию, обновляет `main`,
собирает новый образ и только затем останавливает Gate для копирования SQLite.
Путь к резервной копии будет напечатан; сохраните его для возможного отката.

```bash
bash <<'BASH'
set -euo pipefail

install_dir=$(docker inspect webcodex-gate --format '{{index .Config.Labels "com.docker.compose.project.working_dir"}}')
project=$(docker inspect webcodex-gate --format '{{index .Config.Labels "com.docker.compose.project"}}')
config_files=$(docker inspect webcodex-gate --format '{{index .Config.Labels "com.docker.compose.project.config_files"}}')
test -n "$install_dir" && test "$install_dir" != '<no value>'
test -n "$project" && test "$project" != '<no value>'
cd "$install_dir"
test "$config_files" = "$install_dir/docker-compose.yml"
test -s .env
test "$(git branch --show-current)" = main
git diff --quiet
git diff --cached --quiet

export COMPOSE_PROJECT_NAME="$project"
docker compose version
docker compose -f docker-compose.yml config --quiet
test "$(docker compose -f docker-compose.yml ps -a -q gate)" = "$(docker inspect webcodex-gate --format '{{.Id}}')"

umask 077
stamp=$(date -u +%Y%m%d-%H%M%S)
backup="$install_dir/backups/gate-$stamp"
mkdir -p "$backup"
chmod 700 "$backup"
rollback_image="webcodex-gate:rollback-$stamp"
old_image=$(docker inspect webcodex-gate --format '{{.Image}}')
docker image tag "$old_image" "$rollback_image"
git rev-parse HEAD > "$backup/old-revision.txt"
printf '%s\n' "$project" > "$backup/project.txt"
printf '%s\n' "$rollback_image" > "$backup/rollback-image.txt"
cp .env "$backup/.env"
cp docker-compose.yml "$backup/docker-compose.yml"
printf 'Резервная копия: %s\n' "$backup"

git pull --ff-only origin main
git merge-base --is-ancestor 20c6fff7fcbdf0668f6365e2bd62afc9bcc8d728 HEAD
git rev-parse HEAD > "$backup/new-revision.txt"
docker compose -f docker-compose.yml config --quiet
docker compose -f docker-compose.yml build gate

docker compose -f docker-compose.yml stop gate
if ! docker cp webcodex-gate:/data "$backup/data"; then
  docker compose -f docker-compose.yml start gate
  printf 'Копирование данных не удалось; прежний контейнер запущен.\n' >&2
  exit 1
fi
if ! test -s "$backup/data/webcodex.db"; then
  docker compose -f docker-compose.yml start gate
  printf 'Копия базы отсутствует или пуста; прежний контейнер запущен.\n' >&2
  exit 1
fi

docker compose -f docker-compose.yml up -d --no-deps gate
healthy=false
for attempt in $(seq 1 60); do
  health=$(docker inspect webcodex-gate --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}')
  if test "$health" = healthy; then healthy=true; break; fi
  if test "$health" = unhealthy || test "$health" = exited; then break; fi
  sleep 2
done
if test "$healthy" != true; then
  docker compose -f docker-compose.yml logs --tail=80 gate
  printf 'Gate не стал healthy. Копия для отката: %s\n' "$backup" >&2
  exit 1
fi

docker compose -f docker-compose.yml ps gate
docker exec webcodex-gate wget -q --spider http://127.0.0.1:38971/healthz
printf 'Gate healthy. Ревизия: %s\n' "$(cat "$backup/new-revision.txt")"
printf 'Копия для отката: %s\n' "$backup"
BASH
```

Ожидаемый результат — контейнер `webcodex-gate` в состоянии **healthy**.
`/healthz` возвращает **HTTP 204**, без тела; он проверяет доступность SQLite.
Дополнительно с сервера, если установлен curl:

```bash
curl -sS -o /dev/null -w 'HTTP %{http_code}\n' http://127.0.0.1:38971/healthz
```

В браузере откройте прежний адрес `/admin`, например
`https://codex.grom.world/admin`: должны сохраниться пароль, агенты и MCP-подключения.
Если домен у вас другой, используйте его. Новый образ использует прежний named volume;
`.env` и секреты сохраняются. Блок не переустанавливает Nginx и сертификаты.

Если git обнаружил локальные изменения/расхождение веток или сборка не удалась,
блок остановится **до остановки прежнего Gate**. Не применяйте reset --hard или
force pull для обхода этой ошибки. После начала замены `docker compose start`
уже не является откатом: старый контейнер мог быть пересоздан.

## 3. Если требуется откат

Не удаляйте старый образ и резервную копию до завершения проверки. Если новый
Gate уже запускался, база могла мигрировать: для прежнего образа используйте
сохранённую копию данных. Изменения после резервного копирования в ней отсутствуют.

В блоке ниже замените **только путь backup** на напечатанный при обновлении.
Откат создаёт отдельный volume из копии; текущий volume сохраняется для разбора.

```bash
bash <<'BASH'
set -euo pipefail
backup='/opt/webcdx/backups/gate-ЗАМЕНИТЕ-НА-ПЕЧАТАВШИЙСЯ-ПУТЬ'
test -s "$backup/data/webcodex.db"
test -s "$backup/.env"
project=$(cat "$backup/project.txt")
rollback_image=$(cat "$backup/rollback-image.txt")
docker image inspect "$rollback_image" >/dev/null
recovery_volume="webcodex-gate-recovery-$(date -u +%Y%m%d-%H%M%S)"
docker volume create "$recovery_volume" >/dev/null
docker run --rm --user 0 \
  --mount "type=volume,src=$recovery_volume,dst=/restore" \
  --mount "type=bind,src=$backup/data,dst=/backup,readonly" \
  --entrypoint sh "$rollback_image" \
  -c 'cp -a /backup/. /restore/ && chown -R webcodex:webcodex /restore'

cat > "$backup/rollback.override.yml" <<YAML
services:
  gate:
    image: "$rollback_image"
    pull_policy: never
    volumes:
      - gate_recovery_data:/data
volumes:
  gate_recovery_data:
    external: true
    name: "$recovery_volume"
YAML

docker compose -p "$project" --env-file "$backup/.env" \
  -f "$backup/docker-compose.yml" -f "$backup/rollback.override.yml" \
  up -d --no-deps --no-build gate
healthy=false
for attempt in $(seq 1 60); do
  health=$(docker inspect webcodex-gate --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}')
  if test "$health" = healthy; then healthy=true; break; fi
  if test "$health" = unhealthy || test "$health" = exited; then break; fi
  sleep 2
done
test "$healthy" = true
docker inspect webcodex-gate --format '{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{end}}'
docker exec webcodex-gate wget -q --spider http://127.0.0.1:38971/healthz
printf 'Откат использует volume: %s\n' "$recovery_volume"
BASH
```

Блок ждёт healthy до двух минут. Для дальнейшего управления после отката используйте те же
`-p`, `--env-file` и два `-f`: обычный `compose up` из папки установки может
снова подключить исходный volume и новый образ. Не запускайте `down -v` и не
удаляйте volumes. Если прежний образ использовал другой Dockerfile/пользователя
или иные mounts, этот шаблон отката требует адаптации к той установке.

## 4. После сервера — Windows-агент и расширение

Готовый комплект: `W:\_python\webcdx\bin\webcodex-test-20c6fff.zip`.
Распакованная папка: `W:\_python\webcdx\bin\test-20c6fff`.

1. Остановите прежний агент через Ctrl+C и замените EXE из комплекта, сохранив
   свой запускной BAT с прежними настройками. Запустите его этим BAT.
2. При изменении пути EXE выполните из окончательной папки агента
   `.\webcodex-agent.exe --install-browser-host`.
3. Обновите **всю** папку расширения 1.3.0, включая `command_queue.js`, затем
   перезагрузите расширение в Chrome/Edge. Проверьте локальный доступ в popup.
4. Обновите metadata MCP-инструментов. Все выбранные в одном MCP-подключении
   агенты должны иметь совместимые схемы.
5. Проверьте чтение страницы, ввод и pause/resume на тестовой форме. Сначала
   подтвердите правильный профиль/вкладку; затем переходите к рабочим задачам.

На сервере в рамках подготовки этой инструкции команды не выполнялись.
Проверены конфигурация репозитория и синтаксис команд; установка проверяется
выводом healthy, /healthz и вашей проверкой /admin после обновления.

Справка: [общая инструкция](UPDATE_SERVER.ru.md),
[browser guide](BROWSER_GUIDE.ru.md),
[Docker Compose build](https://docs.docker.com/reference/cli/docker/compose/build/),
[Compose up и сохранение mounted volumes](https://docs.docker.com/reference/cli/docker/compose/up/),
[docker cp для остановленного контейнера](https://docs.docker.com/reference/cli/docker/container/cp/).
