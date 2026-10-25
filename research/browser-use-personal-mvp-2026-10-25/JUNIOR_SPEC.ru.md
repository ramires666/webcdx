# ТЗ для junior-инженера: Browser-use + действующий WebCodex Personal

Дата: 2026-10-25. Цель: MVP без VM/Docker, управление уже открытым авторизованным Chrome. Это спецификация, а не реализация.

## 1. Основная рекомендация

НЕ заменять существующий WebCodex personal bridge прямым CDP только ради «антибота». Текущий WebCodex уже использует обычный Chrome, авторизованные вкладки, расширение MV3, Native Messaging и CDP debugger; browser-use не гарантирует меньшей обнаружимости. Добавить локальную Vision-LLM и AI-планировщик; оставить WebCodex ответственным за реальные браузерные действия. Можно провести отдельный эксперимент прямого CDP, но не подменять личный профиль.

Chrome 136+ ограничивает remote debugging стандартного профиля: https://developer.chrome.com/blog/remote-debugging-port . browser-use поддерживает cdp_url, но не умеет автоматически управлять через существующий WebCodex Native Messaging. Штатный browser-use Agent требует поддерживаемую BrowserSession. Junior обязан проверить публичную точку расширения в конкретной закреплённой версии browser-use; если её нет — реализовать собственный компактный LLM planner + WebCodex executor и честно обозначить решение, а не monkey-patch приватные методы.

## 2. Что уже есть

Читать: AGENTS.md, BROWSER_GUIDE.ru.md, BROWSER_STAGE_A_ACCEPTANCE.ru.md, browser-extension/background.js и cmd/agent/browser*.go. В проекте Go/chromedp и команды browser_open, browser_snapshot, browser_interact, browser_tabs, browser_screenshot, browser_files, browser_wait, browser_control, browser_diagnostics. Уже реализованы alias/profile_id, reuse_only/exact_url, binding_revision, focus_policy=preserve, native input, journaling/request_key и проверка downloads. Не переписывать работающие функции без необходимости.

## 3. Обязательное сравнение рисков

| Параметр | Текущий WebCodex personal | Чистый browser-use CDP | Гибрид |
| --- | --- | --- | --- |
| Прежняя авторизованная вкладка | Да, через расширение | Только при доступном CDP | Да |
| Отдельный профиль | Не нужен | Иногда необходим из-за Chrome 136+ | Не нужен |
| CDP/debugger и машинные действия | Есть | Есть | Есть |
| Доказанная меньшая вероятность блокировки | Нет | Нет | Нет |
| Контроль повторов | Имеется в WebCodex | Нужен дополнительный | Сохраняется |
| AI/Vision | Требует дополнения | Есть | Добавляется |

Нельзя гарантировать «незаметную» автоматизацию. «Антибот-фишки» MVP означают соблюдение правил сайтов, безопасность учётной записи, адекватную частоту операций, уважение HTTP 429, прекращение действий при CAPTCHA/MFA/403/challenge, отсутствие дублированных submit. Не реализовывать spoofing browser fingerprint, обход CAPTCHA, proxy rotation для ухода от санкций или скрытие факта автоматизации. Если правила Discord/LinkedIn запрещают конкретную автоматизацию, перейти на разрешённые API или ручной сценарий.

## 4. Архитектура

Python Browser AI Worker (локальная LLM через настраиваемый OpenAI-compatible API или адаптер) -> строгий JSON/Pydantic planner -> WebCodexPersonalExecutor -> существующие browser_* функции Go -> расширение в личном Chrome. Python Worker общается с агентом через локальный аутентифицированный IPC, предпочтительно named pipe на Windows; без публикации CDP-порта наружу. WebCodex agent остаётся владельцем browser session.

Схемы:
- BrowserTarget: agent_id, profile_id, tab_id, binding_revision, exact_url, approved_origins.
- Action: read_snapshot, screenshot, click, fill, scroll, wait, download, stop; action_id, snapshot_id/element_id/frame_id, request_key. Нет произвольного JS/shell, неконтролируемой навигации, прямого чтения cookies.
- Outcome: ok, failed, cancelled, uncertain, waiting_user, blocked, verification; URL, tab ID, elapsed_ms и безопасная диагностика.

Нельзя выдавать LLM cookies, пароли, OAuth токены, signed URLs или ключи доступа. Ограничить объём DOM/изображения, хранение логов и screenshots. Нужен domain allowlist, явный выбор profile и tab, mutex на изменяющие действия, timeout и аварийная остановка. Для публикаций, платежей, удаления или внешних сообщений требуется подтверждение пользователя.

## 5. Локальная Vision-LLM

Модель пользователя уже запущена, но точные MODEL_BASE_URL, MODEL_NAME, протокол и способность соблюдать structured output неизвестны. Не выдумывать значения. Добавить настройки через локальный env (не коммитить секреты), валидировать JSON schema и ответы модели, ограничить токены/время. Smoke-тесты: правильно выбрать кнопку, не нажимать соседнюю, обработать неизвестный элемент, невалидный JSON, отсутствие изображения, 429 и CAPTCHA. Не считать vision capabilities доказательством умения надёжно планировать действия.

## 6. Поэтапная разработка

1. Spike: изучить закреплённый релиз browser-use и подтвердить реальную возможность вставить кастомный executor через публичный API. На локальном HTML-стенде показать мини-сценарий. Зафиксировать версию и решение.
2. Добавить BrowserExecutor с WebCodexPersonalExecutor. Привязка профиля по alias/profile_id, existing tab, exact_url, binding_revision и проверка target перед каждой мутацией. Не открывать новый Chrome автоматически.
3. Добавить локальный worker/planner с vision, структурированными действиями и валидатором. LLM никогда не выполняет напрямую Python, shell или произвольный JS.
4. Добавить idempotency для mutations, checkpoint и resume, запрет повтора при uncertain outcome; не обходить существующий журнал WebCodex.
5. Добавить user handoff (MFA/CAPTCHA, блокировка, смена авторизации) и глобальный emergency stop.
6. Интеграционные тесты на локальном сайте: SPA rerender, iframe, виртуализированный список, download + SHA-256, переключение вкладки, отрыв debugger, duplicate submit, 403/429, simulated CAPTCHA, падение модели.
7. Проверить E2E в разрешённой тестовой вкладке личного Chrome без вмешательства в другие вкладки, курсор Windows и клавиатуру. Не испытывать агрессивные сценарии на реальных сайтах.
8. Benchmark: p50/p95 latency, количество browser calls/LLM calls, правильность действий, false clicks, остановка на challenge; не выдавать эти метрики за реальную вероятность блокировки.
9. Добавить инструкции установки, конфигурации, обновления agent/Gate/extension и восстановления; соблюсти AGENTS.md (обязательный вывод версий agent/Gate в следующем релизе).
10. Выполнить go test ./..., go vet ./..., Node extension tests, pytest и локальные E2E; проверить отсутствие токенов в Git; коммитить и push каждый законченный этап.

## 7. Критерии приемки

- Подключение к уже запущенному и явно выбранному Chrome personal profile, без копирования стандартного User Data и закрытия браузера.
- Подтверждено использование локальной vision модели; ошибочный structured output не вызывает действий.
- Только разрешённая вкладка получает действия; focus_policy preserve; физическая мышь/клавиатура хоста остаются за пользователем.
- При challenge, 403/429, CAPTCHA/MFA и неопределённом результате мутации работа корректно останавливается.
- Повтор после сбоя не создаёт дубликат отправки, публикации, сохранения.
- Конкретные версии зависимостей, инструкции, тесты и честное описание ограничений. Без заявлений о гарантированном обходе антибота.

## 8. Источники

- Внутренние BROWSER_GUIDE.ru.md, AGENTS.md, BROWSER_STAGE_A_ACCEPTANCE.ru.md.
- https://github.com/browser-use/browser-use/blob/main/skills/open-source/references/browser.md
- https://github.com/browser-use/browser-use/blob/main/AGENTS.md
- https://developer.chrome.com/blog/remote-debugging-port
- https://github.com/chromium/chromium/blob/main/docs/user_data_dir.md
