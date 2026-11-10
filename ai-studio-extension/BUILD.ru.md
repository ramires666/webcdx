# Сборка alpha из исходников

Эта инструкция нужна разработчику, у которого нет готовых файлов. Установка
готового комплекта описана в [README](README.ru.md); для неё Go не требуется.
Ни одна команда ниже не устанавливает alpha, не меняет работающий комплект 1.4.0
и не перезапускает процессы. Для установки нужно отдельно заменить и перезапустить
Gate и агент, загрузить новое расширение и обновить вкладку AI Studio. Старое
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

## Windows x64: Gate и агент

```powershell
New-Item -ItemType Directory -Path bin/ai-studio-bridge -Force
go build -ldflags '-X webcodex/internal/buildinfo.Version=1.5.0-ai-studio-alpha.1' -o bin/ai-studio-bridge/webcodex-gate.exe ./cmd/gate
go build -ldflags '-X webcodex/internal/buildinfo.Version=1.5.0-ai-studio-alpha.1' -o bin/ai-studio-bridge/webcodex-agent.exe ./cmd/agent
```

`New-Item` создаёт папку; `-ItemType Directory` выбирает тип «каталог», `-Path`
задаёт относительный путь `bin/ai-studio-bridge`, а `-Force` разрешает использовать
уже существующую папку. `go build` компилирует программу. `-ldflags` передаёт
заключённую в кавычки строку линковщику; `-X` устанавливает строковую переменную
`webcodex/internal/buildinfo.Version` в указанную версию `1.5.0-ai-studio-alpha.1`.
При выпуске другой версии замените это значение в обеих командах одинаково.
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
go build -ldflags '-X webcodex/internal/buildinfo.Version=1.5.0-ai-studio-alpha.1' -o bin/ai-studio-bridge/webcodex-gate-linux-amd64 ./cmd/gate
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
./ai-studio-extension/package.ps1
```

Этот путь запускает PowerShell-упаковщик без аргументов. Он создаёт
`bin/ai-studio-bridge/webcodex-ai-studio-bridge.zip` только с файлами расширения;
Go-бинарники, тесты и секреты в ZIP не добавляются. Само расширение не требует
компиляции. Перед установкой распакуйте архив в постоянную папку.
