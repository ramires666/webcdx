# Сборка alpha из исходников

Эта инструкция нужна разработчику, у которого нет готовых файлов. Установка
готового комплекта описана в [README](README.ru.md); для неё Go не требуется.
Команды ниже не устанавливают alpha, не меняют работающий комплект 1.4.0
и не перезапускают рабочие процессы. Для локального режима замените и перезапустите
агент, обновите расширение и сохранённую вкладку AI Studio; Gate не требуется,
обновлять или перезапускать его не нужно. Старое
расширение WebCodex Local Browser обновлять не нужно.

Нужны Go 1.25 или новее и PowerShell; Node.js нужен только для тестов расширения.
Команды выполняются из корня этого репозитория.

## Проверка исходников

```powershell
go test ./...
go vet ./...
node --test ai-studio-extension/*.test.cjs
```

`go test` запускает Go-тесты, `go vet` проверяет подозрительные конструкции.
Аргумент `./...` выбирает все Go-пакеты текущего репозитория.
`node` запускает Node.js, `--test` включает его встроенный тестовый режим;
`ai-studio-extension/*.test.cjs` выбирает тестовые файлы расширения. Живой тест
в вашем аккаунте AI Studio эти проверки не заменяют.

### Необязательная проверка локального агента на Windows

Сначала соберите агент командой из раздела Windows ниже. Для локального режима
нужен только EXE агента версии `1.5.0-ai-studio-alpha.4`; Gate собирать, обновлять
или запускать не нужно. Выполните:

```powershell
$env:WEBCODEX_LOCAL_REAL_TEST = '1'
node --test ai-studio-extension/real-local.test.cjs
Remove-Item Env:WEBCODEX_LOCAL_REAL_TEST
```

Первая строка устанавливает переменную окружения `WEBCODEX_LOCAL_REAL_TEST`:
значение `1` включает тест настоящего EXE. `node` запускает Node.js, `--test`
включает тестовый режим, следующий аргумент — путь тестового файла относительно
корня проекта. Тест запускает агент с `--local`, находит его, записывает и читает
временный файл, перезапускает агент и проверяет сохранённый результат без повторной
записи. Ни реестр, ни рабочие настройки агента не меняются. `Remove-Item` удаляет
переменную из текущего окна; `Env:WEBCODEX_LOCAL_REAL_TEST` — её имя в хранилище
окружения PowerShell. Тест удаляет свои процессы и временные файлы.

### Необязательная проверка собранных Gate и агента на Windows

Для этого отдельного теста нужны `webcodex-gate.exe` версии `1.5.0-ai-studio-alpha.4`
и `webcodex-agent.exe` версии `1.5.0-ai-studio-alpha.4` в `bin/ai-studio-bridge`.
В Git готовых EXE нет: если они отсутствуют, сначала выполните Windows-сборку из
следующего раздела. Для текущего локального исправления собран только агент;
Gate не обновлялся.
Следующие команды запускают локальный тест
с временной базой Gate и без AI Studio или ключа модели. Выполните её из корня
репозитория в PowerShell на Windows:

```powershell
$env:WEBCODEX_BRIDGE_REAL_TEST = '1'
node --test ai-studio-extension/real-gate.test.cjs
Remove-Item Env:WEBCODEX_BRIDGE_REAL_TEST
```

Первая строка задаёт для текущего окна переменную окружения
`WEBCODEX_BRIDGE_REAL_TEST`; значение `1` включает этот необязательный тест.
`node` запускает Node.js, `--test` включает тестовый режим, а
`ai-studio-extension/real-gate.test.cjs` — путь тестового файла от корня репозитория;
тест использует уже собранные EXE, проверяет чтение и запись временных файлов,
перезапуск Gate и повторное подключение агента. Успех отображается как один
пройденный тест. `Remove-Item` удаляет переменную из текущего окна PowerShell;
аргумент `Env:WEBCODEX_BRIDGE_REAL_TEST` обозначает её в хранилище окружения.
Тест сам удаляет свои временные данные. На других ОС он пропускается.

## Windows x64: Gate и агент

```powershell
New-Item -ItemType Directory -Path bin/ai-studio-bridge -Force
go build -ldflags '-X webcodex/internal/buildinfo.Version=1.5.0-ai-studio-alpha.4' -o bin/ai-studio-bridge/webcodex-gate.exe ./cmd/gate
go build -ldflags '-X webcodex/internal/buildinfo.Version=1.5.0-ai-studio-alpha.4' -o bin/ai-studio-bridge/webcodex-agent.exe ./cmd/agent
```

`New-Item` создаёт папку; `-ItemType Directory` выбирает тип «каталог», `-Path`
задаёт относительный путь `bin/ai-studio-bridge`, а `-Force` разрешает использовать
уже существующую папку. `go build` компилирует программу. `-ldflags` передаёт
заключённую в кавычки строку линковщику; `-X` устанавливает строковую переменную
`webcodex/internal/buildinfo.Version` в указанную версию: Gate —
`1.5.0-ai-studio-alpha.4`, агент — `1.5.0-ai-studio-alpha.4`.
`-o` задаёт путь готового EXE: первая команда создаёт Gate, вторая — агент.
`./cmd/gate` и `./cmd/agent` выбирают соответствующий пакет исходников.
Команды рассчитаны на Go для Windows x64 без переопределённых GOOS/GOARCH.

После успешного выполнения агент скомпилирован и лежит в
`bin/ai-studio-bridge/webcodex-agent.exe`; повторная самостоятельная сборка для
его установки не нужна. Версия `dev` из обычной сборки без `-ldflags` не является
номером этой alpha-сборки.

## Linux x64: Gate, сборка из PowerShell

В отдельном окне PowerShell из корня репозитория:

```powershell
$env:GOOS = 'linux'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'
go build -ldflags '-X webcodex/internal/buildinfo.Version=1.5.0-ai-studio-alpha.4' -o bin/ai-studio-bridge/webcodex-gate-linux-amd64 ./cmd/gate
```

Префикс `$env:` задаёт переменную окружения для этого окна и запускаемых им
процессов. `GOOS=linux` выбирает Linux, `GOARCH=amd64` — архитектуру x64,
`CGO_ENABLED=0` выключает использование C-компилятора. `-ldflags` и `-X`
внедряют ту же версию, `-o` задаёт имя готового файла, `./cmd/gate` выбирает Gate.
Закройте это отдельное окно после сборки, чтобы следующие Windows-сборки не
унаследовали Linux-настройки. Установка на Linux и права запуска выполняются
отдельно по серверной инструкции.

## Архив Chrome-расширения

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File ./ai-studio-extension/package.ps1
```

`powershell` запускает отдельный PowerShell. `-NoProfile` отключает загрузку
пользовательского профиля, `-ExecutionPolicy Bypass` разрешает выполнение скрипта
только в этом процессе, не меняя системную политику. `-File` задаёт путь скрипта
относительно корня проекта. Упаковщик без дополнительных аргументов создаёт
`bin/ai-studio-bridge/webcodex-ai-studio-bridge.zip` только с файлами расширения;
Go-бинарники, тесты и секреты в ZIP не добавляются. Само расширение не требует
компиляции. Перед установкой распакуйте архив в постоянную папку.
