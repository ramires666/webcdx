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

### Необязательная проверка собранных Gate и агента на Windows

Готовые `webcodex-gate.exe` версии `1.5.0-ai-studio-alpha.2` и
`webcodex-agent.exe` версии `1.5.0-ai-studio-alpha.1` находятся в
`bin/ai-studio-bridge` этой рабочей копии. Gate пересобран для исправления входа,
агент не меняется. В Git готовых EXE нет: если вы скачали
только исходники, сначала выполните Windows-сборку из следующего раздела.
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
go build -ldflags '-X webcodex/internal/buildinfo.Version=1.5.0-ai-studio-alpha.2' -o bin/ai-studio-bridge/webcodex-gate.exe ./cmd/gate
go build -ldflags '-X webcodex/internal/buildinfo.Version=1.5.0-ai-studio-alpha.1' -o bin/ai-studio-bridge/webcodex-agent.exe ./cmd/agent
```

`New-Item` создаёт папку; `-ItemType Directory` выбирает тип «каталог», `-Path`
задаёт относительный путь `bin/ai-studio-bridge`, а `-Force` разрешает использовать
уже существующую папку. `go build` компилирует программу. `-ldflags` передаёт
заключённую в кавычки строку линковщику; `-X` устанавливает строковую переменную
`webcodex/internal/buildinfo.Version` в указанную версию: Gate —
`1.5.0-ai-studio-alpha.2`, агент — `1.5.0-ai-studio-alpha.1`.
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
go build -ldflags '-X webcodex/internal/buildinfo.Version=1.5.0-ai-studio-alpha.2' -o bin/ai-studio-bridge/webcodex-gate-linux-amd64 ./cmd/gate
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
