$ErrorActionPreference = 'Stop'
$distribution = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot 'dist'))
$staging = Join-Path $distribution ('.stage-' + [Guid]::NewGuid().ToString('N'))
Push-Location -LiteralPath $PSScriptRoot
try {
    cargo build --release --locked --target x86_64-pc-windows-msvc --manifest-path runtime-helper/Cargo.toml
    if ($LASTEXITCODE -ne 0) { throw 'Rust build failed.' }
    New-Item -ItemType Directory -Path internal/engine/bin -Force | Out-Null
    Copy-Item -LiteralPath runtime-helper/target/x86_64-pc-windows-msvc/release/hjml-runtime.exe -Destination internal/engine/bin/hjml-runtime.exe
    go tool mygo build -platform windows/amd64 -skip-installer -o $staging
    if ($LASTEXITCODE -ne 0) { throw 'MyGo build failed.' }
    $built = Join-Path $staging 'windows-amd64'
    $output = Join-Path $distribution 'windows-amd64'
    New-Item -ItemType Directory -Path $output -Force | Out-Null
    Copy-Item -LiteralPath (Join-Path $built 'icon.png') -Destination $output -Force
    $exe = Join-Path $output 'HaoJieMinecraftLauncher.exe'
    if (Get-Process -Name HaoJieMinecraftLauncher -ErrorAction SilentlyContinue | Where-Object Path -EQ $exe) {
        $exe = Join-Path $output ('HaoJieMinecraftLauncher-' + [Guid]::NewGuid().ToString('N').Substring(0, 8) + '.exe')
    }
    Copy-Item -LiteralPath (Join-Path $built 'HaoJieMinecraftLauncher.exe') -Destination $exe -Force
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
