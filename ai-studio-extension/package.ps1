param(
    [string]$OutputDirectory = (Join-Path (Split-Path -Parent $PSScriptRoot) 'bin/ai-studio-bridge')
)

$ErrorActionPreference = 'Stop'
$source = (Resolve-Path -LiteralPath $PSScriptRoot).Path
$files = @(
    'manifest.json', 'panel.html', 'panel.css', 'panel.js',
    'background.js', 'content.js', 'protocol.js', 'session-state.js', 'attachments.js', 'studio-adapter.js', 'mcp-client.js', 'local-client.js',
    'vendor/json-tools.js', 'vendor/LICENSES.txt'
)

foreach ($file in $files) {
    if (-not (Test-Path -LiteralPath (Join-Path $source $file) -PathType Leaf)) {
        throw "Required extension source is missing: $file"
    }
}

$destination = [System.IO.Path]::GetFullPath($OutputDirectory)
New-Item -ItemType Directory -Path $destination -Force | Out-Null
$archive = Join-Path $destination 'webcodex-ai-studio-bridge.zip'
Add-Type -AssemblyName System.IO.Compression
Add-Type -AssemblyName System.IO.Compression.FileSystem
$archiveStream = [System.IO.File]::Open($archive, [System.IO.FileMode]::Create)
$zip = New-Object System.IO.Compression.ZipArchive($archiveStream, [System.IO.Compression.ZipArchiveMode]::Create)
try {
    foreach ($file in $files) {
        [System.IO.Compression.ZipFileExtensions]::CreateEntryFromFile($zip, (Join-Path $source $file), $file) | Out-Null
    }
} finally {
    $zip.Dispose()
    $archiveStream.Dispose()
}
Write-Output "Extension ZIP created: $archive (JavaScript is not compiled; Gate and agent are not included.)"
