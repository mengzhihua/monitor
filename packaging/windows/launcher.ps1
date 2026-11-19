param([ValidateSet('start', 'stop')][string]$Action = 'start')
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function FullPath([string]$Value) {
    if (-not [IO.Path]::IsPathRooted($Value)) { $Value = Join-Path $PSScriptRoot $Value }
    return [IO.Path]::GetFullPath($Value)
}
function QuoteArgument([string]$Value) {
    return '"' + ($Value -replace '(\\+)$', '$1$1') + '"'
}
$binary = FullPath $(if ($env:MONITOR_BIN) { $env:MONITOR_BIN } else { 'monitord.exe' })
$data = FullPath $(if ($env:MONITOR_DATA_DIR) { $env:MONITOR_DATA_DIR } else { 'data' })
$config = if ($env:MONITOR_CONFIG) { FullPath $env:MONITOR_CONFIG } else { '' }
$listen = if ($env:MONITOR_LISTEN) { $env:MONITOR_LISTEN } else { '127.0.0.1:20099' }
[IO.Directory]::CreateDirectory($data) | Out-Null
$recordPath = Join-Path $data 'launcher.json'
$lock = $null
$owned = $null
try {
    # An OS file lock serializes start/stop without a stale-lock recovery guess.
    $lock = [IO.File]::Open((Join-Path $data 'launcher.lock'), 'OpenOrCreate', 'ReadWrite', 'None')
    $running = $null
    if (Test-Path -LiteralPath $recordPath) {
        $record = Get-Content -Raw -LiteralPath $recordPath | ConvertFrom-Json
        if ($record.binary -ne $binary -or $record.data -ne $data) { throw 'Launcher identity belongs to another instance.' }
        $running = Get-Process -Id $record.pid -ErrorAction SilentlyContinue
        if ($running -and ($running.Path -ne $binary -or $running.StartTime.ToUniversalTime().Ticks.ToString() -ne $record.started)) {
            throw 'PID identity does not match; refusing to stop or replace it.'
        }
    } elseif (Test-Path -LiteralPath (Join-Path $data 'monitord.pid')) {
        throw 'Legacy pidfile has no process identity; verify and stop that instance before using this launcher.'
    }
    if ($Action -eq 'stop') {
        if (-not $running) { throw 'No matching running instance.' }
        # Windows has no POSIX SIGTERM. This terminates only the recorded process;
        # monitord replays its WAL on restart. Do not imply a graceful checkpoint.
        $running.Kill()
        if (-not $running.WaitForExit(10000)) { throw 'Instance did not exit within 10 seconds.' }
        Remove-Item -LiteralPath $recordPath
        Write-Output 'Recorded Monitor instance stopped; persisted data will be recovered on restart.'
    } else {
        if ($running) { throw 'This Monitor instance is already running.' }
        if (-not (Test-Path -LiteralPath $binary -PathType Leaf)) { throw 'Monitor binary is missing.' }
        if ($config -and -not (Test-Path -LiteralPath $config -PathType Leaf)) { throw 'Monitor config is missing.' }
        # Windows filenames cannot contain a quote. Reject quoted CLI values so
        # Start-Process cannot split them into additional arguments.
        foreach ($value in @($binary, $data, $config, $listen)) {
            if ($value.Contains('"') -or $value.Contains("`r") -or $value.Contains("`n")) { throw 'Invalid launcher argument.' }
        }
        $arguments = @('-listen', (QuoteArgument $listen), '-data-dir', (QuoteArgument $data))
        if ($config) { $arguments += @('-config', (QuoteArgument $config)) }
        $owned = Start-Process -FilePath $binary -ArgumentList $arguments -WorkingDirectory $PSScriptRoot -PassThru -WindowStyle Hidden `
            -RedirectStandardOutput (Join-Path $data 'launcher.stdout.log') -RedirectStandardError (Join-Path $data 'launcher.stderr.log')
        Start-Sleep -Milliseconds 400
        $owned.Refresh()
        if ($owned.HasExited) { throw 'Monitor exited during startup; inspect launcher.stderr.log.' }
        $identity = @{ pid = $owned.Id; started = $owned.StartTime.ToUniversalTime().Ticks.ToString(); binary = $binary; data = $data }
        $temporary = $recordPath + '.tmp'
        $identity | ConvertTo-Json | Set-Content -Encoding UTF8 -LiteralPath $temporary
        Move-Item -Force -LiteralPath $temporary -Destination $recordPath
        $owned = $null # Ownership handed to the validated record, not a PID search.
        Write-Output ('Monitor started at http://' + $listen + '/; password file: ' + (Join-Path $data 'web-password'))
    }
} catch {
    if ($owned -and -not $owned.HasExited) { $owned.Kill(); $owned.WaitForExit() }
    Write-Error $_ -ErrorAction Continue
    exit 1
} finally {
    if ($lock) { $lock.Dispose() }
}
