# Автоматическая приёмка исправлений браузера

Дата: 2026-10-24, Asia/Qyzylorda. Исходный план:
[BROWSER_ISSUES_REMEDIATION_PLAN.ru.md](BROWSER_ISSUES_REMEDIATION_PLAN.ru.md).

Этапы 1–8 реализованы. Проверен код до `bb08e30` включительно; итоговая поставка
помечает расширение версией **1.2.0**. Каждое изменение отправлено в `origin/main`
отдельным завершённым этапом. Это приёмка исходников и локальных тестовых сценариев;
она не утверждает, что рабочая установка Jinn уже обновлена.

## Исправления и коммиты

| Коммит | Результат |
| --- | --- |
| `6759623` | UTF-8 проверяется целиком в пределах лимита, включая read/edit/search/preview |
| `e38b385` | Различимые ошибки DOM/transport, безопасные причины и контекст в status/MCP |
| `a8327ab` | Select options, label/value, контекст повторяющихся полей, contenteditable |
| `9a4e3d9` | Scoped snapshot, причины усечения, metadata, ожидания UI/value/absence |
| `3f59dd7` | Bounded redacted diagnostics с request/operation correlation |
| `1292893` | Post-action verification, сохранность формы, восстановление без второго submit |
| `1f8bf46` | Независимые профили/IPC, discovery, локальные алиасы, Chrome/Edge selection |
| `bb08e30` | Full-page capture, JPEG metadata, payload/artifact availability и Gate delivery |

## Проверенное окружение

| Компонент | Версия / режим |
| --- | --- |
| Go | go1.25.6 windows/amd64 |
| Node.js | v24.11.1 |
| Chrome | 155.0.8059.40, отдельные временные headless профили |
| Edge | 155.0.4283.62, отдельные временные headless профили |
| Расширение | Service worker tests в Node VM; manifest поставки 1.2.0 |
| Linux | Cross-compile agent и Gate: linux/amd64, CGO_ENABLED=0 |

## Результаты

- `WEBCODEX_BROWSER_TEST=1 go test -json ./...` с Chrome: **142 pass, 0 fail**, включая
  subtests. Прошли agent и Gate, настоящий запуск локального агента, MCP round trip,
  personal IPC с отдельной тестовой авторизацией и новые локальные UI fixtures.
- Edge: **10 pass, 0 fail** — типизированные ошибки, forms/contenteditable,
  snapshot/wait/hydration, preview 500/timeout, recovery/dedup, full-page JPEG,
  managed E2E и personal IPC adapter.
- `node --test browser-extension/background.test.cjs`: **19 pass, 0 fail**.
- `go vet ./...`, syntax checks background/popup: успешно.
- `go test -race ./cmd/agent` для discovery, personal IPC, operation concurrency
  и safe journal/polling: успешно.
- `go test -c` agent/Gate для Linux: успешно. Это проверка сборки, не Linux runtime/GUI.
- `git diff --check`: успешно перед коммитами. Журналы и тестовые бинарники хранятся
  только в игнорируемом `tmp/` и не входят в поставку.

Важные проверки результата: неверный select не становится stale; Start Month
не меняет End Month; контрольные секреты отсутствуют в diagnostics/error journal;
preview 500/timeout сохраняет текст и допускает Back; потерянный ответ и status
не создают второй submit. JPEG декодируется после agent → Gate, его размеры
совпадают с metadata. Несколько профилей не выбираются молча, pause/reconnect
изолированы. Изменение contenteditable, существовавшее до начала работы,
включено только после собственной проверки.

## Что остаётся ручной приёмкой

- `TestInstalledPersonalBrowserLive` пропущен: рабочее расширение и реальный
  пользовательский аккаунт не использовались.
- Не получены версии реально запущенных компонентов Jinn; их нельзя отождествлять
  с проверенными исходниками. Агент/Gate не заменялись и не перезапускались.
- Рендер image block конкретным MCP-клиентом не проверен; подтверждена доставка
  корректного JPEG через Gate.
- Linux GUI и реальная установка расширения в Chrome/Edge профилях пользователя
  не проверены. Проверены Windows IPC и тестовый personal adapter.
- Реальные GitHub preview и LinkedIn Featured reorder не повторялись. Их внутренняя
  причина остаётся неизвестной; изменения улучшают диагностику и recovery.
- Профиль LinkedIn, публикации, навыки и Featured в этой работе не менялись.

## Применение к рабочей установке

1. Дождаться важных операций и сохранить несохранённые пользовательские формы.
   Использовать существующую процедуру резервного копирования/обновления Gate:
   [UPDATE_SERVER.ru.md](UPDATE_SERVER.ru.md). Профили и базу не удалять.
2. Обновить Gate, затем локальный agent из той же ревизии. `build.bat --no-pause`
   собирает Windows-компоненты и ZIP расширения; `build.sh` — Linux-компоненты.
   Сборку делать вне работающего пути бинарника, затем остановить старый agent
   и заменить его. Секреты и настройки не перезаписывать.
3. Обновить файлы расширения до 1.2.0 и перезагрузить его в нужных Chrome/Edge
   профилях. При переносе agent повторить `--install-browser-host`; сохранить
   прежний extension ID и постоянные профили. Native host должен указывать на
   обновлённый бинарник. При необходимости временно поставить расширение на паузу
   на время замены, затем продолжить локально.
4. Обновить metadata инструментов MCP-клиента. Для групп агентов обновить все
   задействованные агенты: Gate отклоняет несовпадающие schemas. Новые поля
   optional; `/mcp/v5` и состав инструментов сохранены.
5. В popup задать уникальные алиасы. Выполнить `browser_control(action=discover)`,
   проверить выбранный profile/product/tab и аккаунт по видимой странице.
6. Сначала пройти локальные read/form/screenshot сценарии. Затем проверить живые
   страницы и rendering клиента. Изменения реального профиля или публикации
   выполнять только в объёме отдельного поручения на такие действия.

Новые параметры и примеры:
[BROWSER_GUIDE.ru.md](BROWSER_GUIDE.ru.md). Обновление рабочих компонентов и
ручные пункты выше не отмечены выполненными.
