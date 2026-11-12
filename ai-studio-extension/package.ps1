param(
    [string]$OutputDirectory = (Join-Path (Split-Path -Parent $PSScriptRoot) 'bin/ai-studio-bridge')
)

$ErrorActionPreference = 'Stop'
$source = (Resolve-Path -LiteralPath $PSScriptRoot).Path
$files = @(
    'manifest.json', 'panel.html', 'panel.css', 'panel.js',
    'background.js', 'content.js', 'protocol.js', 'studio-adapter.js', 'mcp-client.js', 'local-client.js'
)

foreach ($file in $files) {
    if (-not (Test-Path -LiteralPath (Join-Path $source $file) -PathType Leaf)) {
        throw "Required extension source is missing: $file"
    }
}

$destination = [System.IO.Path]::GetFullPath($OutputDirectory)
New-Item -ItemType Directory -Path $destination -Force | Out-Null
$archive = Join-Path $destination 'webcodex-ai-studio-bridge.zip'
Compress-Archive -LiteralPath ($files | ForEach-Object { Join-Path $source $_ }) -DestinationPath $archive -Force
Write-Output "Extension ZIP created: $archive (JavaScript is not compiled; Gate and agent are not included.)"
