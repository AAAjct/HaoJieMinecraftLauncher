param(
    [string]$DistributionDirectory = (Join-Path $PSScriptRoot 'dist'),
    [switch]$Compress
)

$ErrorActionPreference = 'Stop'
$distribution = [IO.Path]::GetFullPath($DistributionDirectory)
$staging = Join-Path $distribution ('.stage-' + [Guid]::NewGuid().ToString('N'))
Push-Location -LiteralPath $PSScriptRoot
try {
    go tool mygo build -platform windows/amd64 -skip-installer -o $staging
    if ($LASTEXITCODE -ne 0) { throw 'MyGo build failed.' }
    $built = Join-Path $staging 'windows-amd64'
    $builtExe = Join-Path $built 'HJML.exe'
    if ($Compress) {
        $upx = Get-Command upx -CommandType Application -ErrorAction Stop
        & $upx.Source --best --lzma $builtExe
        if ($LASTEXITCODE -ne 0) { throw 'UPX compression failed.' }
        & $upx.Source -t $builtExe
        if ($LASTEXITCODE -ne 0) { throw 'UPX verification failed.' }
    }
    $output = Join-Path $distribution 'windows-amd64'
    New-Item -ItemType Directory -Path $output -Force | Out-Null
    $exe = Join-Path $output 'HJML.exe'
    Copy-Item -LiteralPath $builtExe -Destination $exe -Force
    $oldIcon = Join-Path $output 'icon.png'
    if (Test-Path -LiteralPath $oldIcon -PathType Leaf) {
        Remove-Item -LiteralPath $oldIcon -Force
    }
    Write-Output "Built $exe"
}
finally {
    if (Test-Path -LiteralPath $staging) {
        $stage = Get-Item -LiteralPath $staging
        if (!$stage.LinkType -and $stage.FullName -eq $staging -and $stage.Parent.FullName -eq $distribution) {
            Remove-Item -LiteralPath $staging -Recurse -Force
        }
    }
    Pop-Location
}
