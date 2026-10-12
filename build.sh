#!/usr/bin/env bash
set -e

echo "========================================================"
echo "  WebCodex: Сборка компонентов (Agent + Gate)"
echo "========================================================"
echo ""

# Проверка наличия Go
if ! command -v go &> /dev/null; then
    echo "[ОШИБКА] Go не найден в системе!"
    echo "Установите Go:"
    echo "  Ubuntu/Debian: sudo apt update && sudo apt install -y golang"
    echo "  macOS:         brew install go"
    echo "  Официальный:   https://go.dev/dl/"
    exit 1
fi

echo "[OK] Найден Go: $(go version)"
echo ""

# Сборка агента
echo "--------------------------------------------------------"
echo "[1/2] Компиляция webcodex-agent ..."
go build -ldflags="-s -w" -o webcodex-agent ./cmd/agent
echo "[УСПЕХ] Создан webcodex-agent"

# Сборка шлюза
echo ""
echo "--------------------------------------------------------"
echo "[2/2] Компиляция bin/webcodex-gate ..."
mkdir -p bin
go build -ldflags="-s -w" -o bin/webcodex-gate ./cmd/gate
echo "[УСПЕХ] Создан bin/webcodex-gate"

mkdir -p bin/browser-extension
cp browser-extension/manifest.json browser-extension/background.js browser-extension/popup.html browser-extension/popup.js bin/browser-extension/
echo "[УСПЕХ] Расширение подготовлено в bin/browser-extension"

echo ""
echo "========================================================"
echo "  Сборка успешно завершена!"
echo "========================================================"

