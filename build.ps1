$ErrorActionPreference = 'Stop'
Push-Location $PSScriptRoot
try {
    $release = Get-Content ai-studio-extension/package.json -Raw | ConvertFrom-Json
    New-Item -ItemType Directory -Path bin/aix -Force | Out-Null
    & go build -trimpath -tags aistudio -ldflags ("-X webcodex/internal/buildinfo.Version=" + $release.webcodexRelease.agent) -o bin/aix/webcodex-agent.exe ./cmd/agent
    if ($LASTEXITCODE -ne 0) { throw 'Local agent build failed' }
    & ./ai-studio-extension/package.ps1 -OutputDirectory (Join-Path $PSScriptRoot 'bin/aix')
    Copy-Item -LiteralPath README.ru.md -Destination bin/aix/README.ru.md
    New-Item -ItemType Directory -Path bin/aix/docs -Force | Out-Null
    Copy-Item -LiteralPath docs/model-tasks.ru.md -Destination bin/aix/docs/model-tasks.ru.md
    Write-Output 'Ready local AI Studio bundle: bin/aix'
} finally { Pop-Location }
