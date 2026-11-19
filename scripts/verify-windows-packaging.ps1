param([string]$Binary = 'core/bin/monitord')
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$root = Join-Path ([IO.Path]::GetTempPath()) ('monitor-launcher-' + [Guid]::NewGuid())
$package = Join-Path $root 'package with spaces'
$data = Join-Path $root 'data with spaces'
$ownedRecords = @()
$decoy = $null
function Assert($Condition, [string]$Message) { if (-not $Condition) { throw $Message } }
try {
    New-Item -ItemType Directory -Path $package, $data | Out-Null
    Copy-Item -LiteralPath $Binary -Destination (Join-Path $package 'monitord.exe')
    Copy-Item packaging/windows/* -Destination $package
    $config = Join-Path $root 'config with spaces.yaml'
    'collectors: {enabled: [mem]}', 'plugins: {enabled: false}', 'health: {enabled: false}' | Set-Content $config
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $listener.Start(); $port = $listener.LocalEndpoint.Port; $listener.Stop()
    $env:MONITOR_DATA_DIR = $data
    $env:MONITOR_CONFIG = $config
    $env:MONITOR_LISTEN = '127.0.0.1:' + $port
    Remove-Item Env:MONITOR_BIN -ErrorAction SilentlyContinue
    $launcher = Join-Path $package 'launcher.ps1'
    function Launch([string]$Action, [bool]$Success) {
        & powershell.exe -NoProfile -ExecutionPolicy Bypass -File $launcher -Action $Action
        Assert (($LASTEXITCODE -eq 0) -eq $Success) ('Unexpected ' + $Action + ' result')
    }
    function Ready() {
        $until = [DateTime]::UtcNow.AddSeconds(20)
        while ([DateTime]::UtcNow -lt $until) {
            try { $null = Invoke-WebRequest ('http://' + $env:MONITOR_LISTEN + '/healthz') -UseBasicParsing -TimeoutSec 1; return } catch { Start-Sleep -Milliseconds 100 }
        }
        throw 'Packaged instance did not become healthy.'
    }
    Launch start $true
    $recordPath = Join-Path $data 'launcher.json'
    $recordText = Get-Content -Raw $recordPath
    $record = $recordText | ConvertFrom-Json
    $ownedRecords += ($recordText | ConvertFrom-Json)
    Ready
    $password = Get-Content -Raw (Join-Path $data 'web-password')
    Launch start $false
    # A stale timestamp for the same PID must not terminate even our own process.
    $record.started = '0'
    $record | ConvertTo-Json | Set-Content $recordPath
    Launch stop $false
    Assert ($null -ne (Get-Process -Id $record.pid -ErrorAction SilentlyContinue)) 'Stale record killed the instance'
    # A real different executable must not be accepted merely because its PID exists.
    $decoy = Start-Process powershell.exe -ArgumentList '-NoProfile -Command Start-Sleep -Seconds 120' -PassThru -WindowStyle Hidden
    $record.pid = $decoy.Id
    $record.started = $decoy.StartTime.ToUniversalTime().Ticks.ToString()
    $record | ConvertTo-Json | Set-Content $recordPath
    Launch stop $false
    Assert (-not $decoy.HasExited) 'Decoy process was terminated'
    $recordText | Set-Content $recordPath
    Launch stop $true
    Assert (-not (Test-Path $recordPath)) 'Stopped identity was retained'
    Launch start $true
    $record = Get-Content -Raw $recordPath | ConvertFrom-Json
    $ownedRecords += $record
    Ready
    Assert ((Get-Content -Raw (Join-Path $data 'web-password')) -eq $password) 'Persistent login changed on restart'
    Launch stop $true
    Write-Output 'PASS: spaces, real startup/restart, duplicate start, PID reuse, executable identity, persistent login and instance-only stop'
} finally {
    foreach ($ownedRecord in $ownedRecords) {
        $candidate = Get-Process -Id $ownedRecord.pid -ErrorAction SilentlyContinue
        if ($candidate -and $candidate.Path -eq $ownedRecord.binary -and $candidate.StartTime.ToUniversalTime().Ticks.ToString() -eq $ownedRecord.started) {
            $candidate.Kill(); $candidate.WaitForExit()
        }
    }
    if ($decoy -and -not $decoy.HasExited) { $decoy.Kill(); $decoy.WaitForExit() }
    Remove-Item -Recurse -Force $root -ErrorAction SilentlyContinue
}
