param(
    [switch]$Live
)

$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
$testDir = Join-Path ([System.IO.Path]::GetTempPath()) ('webcdx-subagents-' + [guid]::NewGuid().ToString('N'))
$agent = Join-Path $testDir 'webcodex-agent.exe'
$fixture = Join-Path $testDir 'opencode-fixture.exe'
$vars = @('WEBCODEX_SMOKE_AGENT', 'WEBCODEX_SMOKE_FIXTURE', 'WEBCODEX_SMOKE_MODE')
$previous = @{}
foreach ($name in $vars) {
    $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}

if ($Live) {
    if (-not [System.IO.Path]::IsPathRooted($env:WEBCODEX_OPENCODE_PATH) -or
        -not (Test-Path -LiteralPath $env:WEBCODEX_OPENCODE_PATH -PathType Leaf)) {
        throw 'Set WEBCODEX_OPENCODE_PATH to the full path of opencode.exe.'
    }
    $keySources = @($env:WEBCODEX_TASK_KEY_FILE, $env:WEBCODEX_TASK_OPENMCP_ENV, $env:WEBCODEX_TASK_OPENCODE_AUTH, $env:WEBCODEX_TASK_OPENCODE_DB)
    $configuredSources = @($keySources | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    if ($configuredSources.Count -ne 1) {
        throw 'Set one of WEBCODEX_TASK_KEY_FILE, WEBCODEX_TASK_OPENMCP_ENV, WEBCODEX_TASK_OPENCODE_AUTH, or WEBCODEX_TASK_OPENCODE_DB.'
    }
}

New-Item -ItemType Directory -Path $testDir | Out-Null
Push-Location -LiteralPath $repo
try {
    & go build -tags aistudio -o $agent ./cmd/agent
    if ($LASTEXITCODE -ne 0) { throw 'Could not build the local AIX agent.' }

    $env:WEBCODEX_SMOKE_AGENT = $agent
    $env:WEBCODEX_SMOKE_FIXTURE = ''
    $env:WEBCODEX_SMOKE_MODE = 'live'
    if (-not $Live) {
        & go test -c -o $fixture ./cmd/agent
        if ($LASTEXITCODE -ne 0) { throw 'Could not build the OpenCode test fixture.' }
        $env:WEBCODEX_SMOKE_MODE = 'fixture'
        $env:WEBCODEX_SMOKE_FIXTURE = $fixture
    }

    & node --test ai-studio-extension/subagents-smoke.cjs
    if ($LASTEXITCODE -ne 0) { throw 'Subagent smoke tests failed.' }
} finally {
    Pop-Location
    foreach ($name in $vars) {
        [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process')
    }
    Remove-Item -LiteralPath $testDir -Recurse -Force
}
