param(
    [string]$DistributionDirectory = (Join-Path $PSScriptRoot 'dist'),
    [switch]$Compress,
    [switch]$CurseForgeKeyFromStdin
)

$ErrorActionPreference = 'Stop'
$distribution = [IO.Path]::GetFullPath($DistributionDirectory)
$staging = Join-Path $distribution ('.stage-' + [Guid]::NewGuid().ToString('N'))
$keyStage = $null
$oldGoFlags = $env:GOFLAGS
$oldGoCache = $env:GOCACHE
$oldGoTemp = $env:GOTMPDIR
Push-Location -LiteralPath $PSScriptRoot
try {
    if ($CurseForgeKeyFromStdin) {
        $key = [Console]::In.ReadToEnd().Trim()
        if ($key -notmatch '\A[!-~]+\z') {
            throw 'Supply a nonempty CurseForge API key on standard input.'
        }
        $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
        $keyStage = Join-Path $tempRoot ('hjml-build-key-' + [Guid]::NewGuid().ToString('N'))
        New-Item -ItemType Directory -Path $keyStage | Out-Null
        $source = Join-Path $keyStage 'curseforge_build_key.go'
        $original = Join-Path $PSScriptRoot 'internal/engine/curseforge_build_key.go'
        $overlay = Join-Path $keyStage 'overlay.json'
        $goString = ConvertTo-Json -Compress -InputObject $key
        [IO.File]::WriteAllText($source, "package engine`nconst builtInCurseForgeKey = $goString`n")
        $mapping = @{}
        $mapping[$original] = $source
        [IO.File]::WriteAllText($overlay, (@{Replace = $mapping} | ConvertTo-Json -Compress))
        $env:GOFLAGS = ('{0} "-overlay={1}"' -f $oldGoFlags, $overlay).Trim()
        $env:GOCACHE = Join-Path $keyStage 'go-cache'
        $env:GOTMPDIR = $keyStage
    }
    go tool mygo build -platform windows/amd64 -skip-installer -o $staging
    if ($LASTEXITCODE -ne 0) { throw 'MyGo build failed.' }
    $built = Join-Path $staging 'windows-amd64'
    $builtExe = Join-Path $built 'HJML.exe'
    if ($Compress) {
        $upx = Get-Command upx -CommandType Application -ErrorAction Stop
        & $upx.Source --ultra-brute $builtExe
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
    if ($keyStage) {
        $env:GOFLAGS = $oldGoFlags
        $env:GOCACHE = $oldGoCache
        $env:GOTMPDIR = $oldGoTemp
        if (Test-Path -LiteralPath $keyStage) {
            $keyDir = Get-Item -LiteralPath $keyStage
            if (!$keyDir.LinkType -and $keyDir.FullName -eq $keyStage -and $keyDir.Parent.FullName -eq $tempRoot.TrimEnd('\')) {
                Remove-Item -LiteralPath $keyStage -Recurse -Force
            }
        }
    }
    if (Test-Path -LiteralPath $staging) {
        $stage = Get-Item -LiteralPath $staging
        if (!$stage.LinkType -and $stage.FullName -eq $staging -and $stage.Parent.FullName -eq $distribution) {
            Remove-Item -LiteralPath $staging -Recurse -Force
        }
    }
    Pop-Location
}
