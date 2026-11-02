# Браузер через uebagent MCP

WebCodex по умолчанию передаёт задачи браузера в существующий локальный uebagent:

```text
ChatGPT → Gate WebCodex → агент WebCodex → MCP localhost → агент и Camoufox в Docker
```

Агент WebCodex использует Streamable HTTP endpoint uebagent `http://localhost:8090/mcp` и Bearer-токен из локального файла. Самостоятельная модель uebagent и его executor выполняют задачу в Camoufox; WebCodex не запускает и не подключает Chrome для этого режима.

Перед запуском нужны: работающий uebagent MCP/browser сервис на локальном порту 8090, запущенный Windows `llama-server` на настроенном OpenAI-compatible endpoint, и установленный Docker Desktop с Linux/WSL2 engine. Если модель использует изображение страницы, ей нужен совместимый vision-модельный файл и projector. Если репозитории находятся рядом в `W:\_python\{webcdx,uebagent}`, команды запуска и проверки смотрите в [README uebagent](../../uebagent/README.md); если они лежат в других каталогах, скорректируйте относительный путь ссылки. Сеть Docker для сайтов должна быть настроена в соответствии с политикой uebagent.

## Настройка

Добавьте переменные в используемый локальный start-agent скрипт до запуска агента. Блок ниже — синтаксис PowerShell для `.ps1`:

```powershell
$env:WEBCODEX_BROWSER_BACKEND = "mcp"
$env:WEBCODEX_BROWSER_MCP_URL = "http://localhost:8090/mcp"
$env:WEBCODEX_BROWSER_MCP_TOKEN_FILE = "W:\_python\uebagent\runtime\access\mcp-token.txt"
$env:WEBCODEX_BROWSER_MCP_TIMEOUT = "30s"
```

`WEBCODEX_BROWSER_BACKEND` выбирает `mcp` (значение по умолчанию), `native` или `disabled`. `WEBCODEX_BROWSER_MCP_URL` указывает локальный MCP endpoint; если переменную не задать, используется показанный адрес. `WEBCODEX_BROWSER_MCP_TOKEN_FILE` указывает файл с Bearer-токеном uebagent; токен должен быть в файле одной строкой. Не вставляйте его в скрипт и не выводите в журнал. `WEBCODEX_BROWSER_MCP_TIMEOUT` задаёт тайм-аут одного MCP запроса; `30s` — значение по умолчанию.

Строки PowerShell `$env:...` задают переменные только для текущего процесса и его дочерних процессов. Поместите их в используемый start-agent `.ps1`. Для start-agent `.bat` используйте соответствующий синтаксис:

```bat
set "WEBCODEX_BROWSER_BACKEND=mcp"
set "WEBCODEX_BROWSER_MCP_URL=http://localhost:8090/mcp"
set "WEBCODEX_BROWSER_MCP_TOKEN_FILE=W:\_python\uebagent\runtime\access\mcp-token.txt"
set "WEBCODEX_BROWSER_MCP_TIMEOUT=30s"
```

Файл `.env` сам по себе переменные агента не загружает. Перезапустите агент после изменения endpoint, пути токена или выбранного backend.

`WEBCODEX_BROWSER_ENABLED=0` отключает браузерные инструменты целиком. Переменная со значением `1` сама по себе не включает локальный Chrome: чтобы явно выбрать старый встроенный браузер WebCodex, установите `WEBCODEX_BROWSER_BACKEND=native`. Вариант `disabled` оставляет только файловые и командные инструменты.

## Доступные инструменты и сбои подключения

Через Gate MCP v5 агент публикует ровно `browser_task_start`, `browser_task_status`, `browser_task_events`, `browser_task_result`, `browser_task_pause`, `browser_task_resume` и `browser_task_cancel`. Это инструменты жизненного цикла одной удалённой задачи; они не имитируют низкоуровневые `browser_open`/`browser_interact`.

При начале задачи `sync_cookies` по умолчанию выключен. Если оператор явно передаст `sync_cookies=true`, uebagent сможет применить соответствующую настройку синхронизации. Ограничения и разрешения на синхронизацию задаются самим uebagent.

При `sync_cookies=false` вход в сайты выполняется отдельно в профиле Camoufox. Откройте [локальный просмотрщик](http://localhost:8090/desktop), нажмите «Забрать управление», войдите вручную в нужный сайт и нажмите «Вернуть управление». Перед задачей проверьте, что выбран правильный аккаунт. Расширение Chrome WebCodex и personal-browser подключение для этого режима не нужны. Подробнее: [панель и расширение uebagent](../../uebagent/README.md#панель-и-расширение-chrome). Если репозитории расположены не рядом, замените относительный путь на соответствующий путь к README.

Если в карточке агента Gate настроен allowlist, добавьте в `AllowedTools` точные имена семи `browser_task_*` инструментов выше. Проверьте также `DeniedTools`: deny-list имеет приоритет и не должен содержать эти имена. Gate фильтрует инструменты по точному имени. После запуска uebagent обновите список инструментов MCP-клиента или переподключите MCP, чтобы браузерные инструменты появились. Если uebagent недоступен при запросе `tools/list`, WebCodex оставляет файловые и командные инструменты доступными, а браузерные инструменты временно не показывает и повторяет обнаружение при следующем запросе списка. Если соединение пропало во время вызова, агент возвращает ошибку и не переключается на локальный Chrome.

Безопасный пример задачи для MCP-клиента: «Вызови `browser_task_start` с `goal`: открой `https://example.org` и прочитай заголовок страницы; не нажимай кнопки и не отправляй формы. Передай `allowed_origins: ["https://example.org"]`, `sync_cookies: false` и новый уникальный `idempotency_key`, например `read-example-org-20261102-001`. Сохрани выданный `task_id`, проверяй выполнение через `browser_task_status` (при необходимости читай события через `browser_task_events`), затем запроси `browser_task_result`. Не заменяй origin на домен с другого сайта.»

## Обновление компонентов

Для этой интеграции нужны новые бинарники агента и Gate. Их кандидаты собираются в `staging\artifacts\webcodex-agent.exe` и `staging\artifacts\webcodex-gate.exe` с версией `uebagent-integration.ea106c8`; готовый комплект не входит в исходники и должен пройти проверку до установки. Для установки проверенного комплекта замените Agent и Gate и перезапустите оба. Gate обновляется также из-за обязательного вывода версий Gate и агента при старте и после подключения. Расширение браузера обновлять не нужно. Если готовые проверенные EXE предоставлены, самостоятельная сборка не нужна. Для сборки из исходников выполните команды ниже из корня репозитория; они собирают именно эти два бинарника с версией кандидата.

Сборка из исходников в PowerShell:

```powershell
go test -count=1 ./... # запускает все Go-тесты один раз без переиспользования кэша
go vet ./...          # запускает статические проверки Go-кода
New-Item -ItemType Directory -Force staging/artifacts # создаёт каталог для файлов сборки, если его ещё нет
go build -ldflags="-X webcodex/internal/buildinfo.Version=uebagent-integration.ea106c8" -o staging/artifacts/webcodex-agent.exe ./cmd/agent # собирает агент с версией кандидата
go build -ldflags="-X webcodex/internal/buildinfo.Version=uebagent-integration.ea106c8" -o staging/artifacts/webcodex-gate.exe ./cmd/gate # собирает Gate с той же версией кандидата
```

Эти команды нужны только при сборке из исходников. Не запускайте собранные бинарники поверх установленного комплекта до завершения проверок и выкладки.
