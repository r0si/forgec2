<#
.SYNOPSIS
    Build, deploy and verify a ForgeC2 server instance.

.DESCRIPTION
    One command that takes the working tree to a running, verified server:

        preflight -> build (frontend bundle + Go binary) -> stop the old
        instance -> start the new one -> health/ready/login smoke -> rollback

    The previous binary is kept as forgec2-server.exe.prev. A new instance
    that does not come up healthy is rolled back to that binary and started
    again, so a bad build never leaves the box down.

    The build itself lives in scripts/build-embedded.ps1 (the canonical
    frontend -> webdist sync). This script only adds the deployment half.

.PARAMETER Config
    Config file to run with (default: config.yaml).

.PARAMETER Port
    Port for the health probe. 0 = read server.port from the config, else 8000.

.PARAMETER SkipChecks
    Skip the frontend gates (typecheck/lint/test/check) for a quick redeploy.

.PARAMETER SkipFrontend
    Skip the frontend build entirely (backend-only change).

.PARAMETER NoStart
    Build only: do not touch the running instance.

.PARAMETER NoRollback
    Do not roll back to the previous binary when the new instance fails.

.PARAMETER HealthTimeoutSeconds
    How long to wait for the new instance to answer /health (default 60).

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File scripts/deploy.ps1

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File scripts/deploy.ps1 -SkipChecks -Port 8000
#>
param(
    [string]$Config = "config.yaml",
    [int]$Port = 0,
    [switch]$SkipChecks,
    [switch]$SkipFrontend,
    [switch]$NoStart,
    [switch]$NoRollback,
    [int]$HealthTimeoutSeconds = 60
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot

# Wait-ForHealth polls /health over plain HTTP and then over the self-signed
# HTTPS profile (the default is TLS on, and PS 5.1 has no
# -SkipCertificateCheck, so the probe relaxes validation for itself only).
function Wait-ForHealth {
    param(
        [System.Diagnostics.Process]$Process,
        [int]$Port,
        [int]$TimeoutSeconds = 60
    )
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if ($Process -and $Process.HasExited) { return $false }
        Start-Sleep -Seconds 1
        try {
            $health = Invoke-RestMethod -Uri "http://127.0.0.1:$Port/health" -TimeoutSec 3 -ErrorAction Stop
            if ($null -ne $health -and $health.status -eq "ok") { return $true }
        } catch { }
        try {
            add-type @"
using System.Net;
using System.Security.Cryptography.X509Certificates;
public class TrustAllCertsPolicy : ICertificatePolicy {
    public bool CheckValidationResult(ServicePoint srvPoint, X509Certificate certificate, WebRequest request, int certificateProblem) {
        return true;
    }
}
"@ -ErrorAction SilentlyContinue
            [System.Net.ServicePointManager]::CertificatePolicy = New-Object TrustAllCertsPolicy
            $health = Invoke-RestMethod -Uri "https://127.0.0.1:$Port/health" -TimeoutSec 3 -ErrorAction Stop
            if ($null -ne $health -and $health.status -eq "ok") { return $true }
        } catch { }
    }
    return $false
}

Push-Location $root
try {
    function Step($msg) { Write-Host "`n==> $msg" -ForegroundColor Cyan }
    function Ok($msg) { Write-Host "  OK   $msg" -ForegroundColor Green }
    function Warn($msg) { Write-Host "  WARN $msg" -ForegroundColor Yellow }
    function Fail($msg) { Write-Host "  FAIL $msg" -ForegroundColor Red }

    # ---------------------------------------------------------------- 0. preflight
    Step "Preflight"
    foreach ($cmd in @("go", "node", "npm")) {
        if (-not (Get-Command $cmd -ErrorAction SilentlyContinue)) {
            throw "$cmd is not on PATH - install it before deploying"
        }
    }
    if (-not (Test-Path $Config)) {
        throw "config '$Config' not found - run 'bash scripts/setup-dev.sh' or copy config.example.yaml"
    }
    Ok "toolchain and config present"

    # The probe port: the argument wins, then the config, then the default.
    if ($Port -le 0) {
        $Port = 8000
        $portLine = Select-String -Path $Config -Pattern '^\s*port:\s*(\d+)' -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($portLine -and $portLine.Matches.Count -gt 0) {
            $Port = [int]$portLine.Matches[0].Groups[1].Value
        }
    }
    Ok "probe port $Port"

    # The bind address decides whether the console is reachable from another
    # host. Same-origin access needs no allowlist, so a wildcard bind means
    # any host that can route here can reach the login page - the exposure is
    # worth calling out even though authentication still gates everything.
    $hostLine = Select-String -Path $Config -Pattern '^\s*host:\s*"?([0-9a-fA-F.:\[\]]+)"?' -ErrorAction SilentlyContinue | Select-Object -First 1
    $bindHost = "0.0.0.0"
    if ($hostLine -and $hostLine.Matches.Count -gt 0) {
        $bindHost = $hostLine.Matches[0].Groups[1].Value
    }
    if ($bindHost -eq "0.0.0.0" -or $bindHost -eq "::" -or $bindHost -eq "") {
        Warn "the console binds $bindHost - any host that can route to this machine can reach the login page; set server.host: 127.0.0.1 to keep it local"
    }

    # ------------------------------------------------------------------ 1. build
    Step "Build"
    $buildArgs = @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", (Join-Path $PSScriptRoot "build-embedded.ps1"))
    if ($SkipChecks) { $buildArgs += "-SkipChecks" }
    if ($SkipFrontend) { $buildArgs += "-SkipFrontend" }
    # -NoRestart: this script owns the stop/start/health/rollback sequence.
    $buildArgs += "-NoRestart"
    & powershell @buildArgs
    if ($LASTEXITCODE -ne 0) { throw "build failed (see the output above)" }
    Ok "build finished"

    $binary = Join-Path $root "forgec2-server.exe"
    $candidate = Join-Path $root "forgec2-server.new.exe"
    $previous = Join-Path $root "forgec2-server.exe.prev"
    if (-not (Test-Path -LiteralPath $candidate)) {
        throw "build reported success but candidate '$candidate' is missing"
    }

    if ($NoStart) {
        Write-Host "`nBuild only (-NoStart): candidate ready at $candidate; the running instance was not touched." -ForegroundColor DarkGray
        return
    }

    # ------------------------------------------------- 2. keep the last good build
    if (Test-Path -LiteralPath $binary) {
        Copy-Item -LiteralPath $binary -Destination $previous -Force
        Ok "previous binary kept as forgec2-server.exe.prev"
    }

    # ------------------------------------------------------------ 3. stop the old one
    Step "Stop the running instance"
    $running = Get-Process -Name "forgec2-server" -ErrorAction SilentlyContinue
    if ($running) {
        foreach ($proc in $running) {
            Write-Host "  stopping PID $($proc.Id)"
            Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
        }
        # Wait for the port to be released: the new instance cannot bind while
        # the old one still holds it.
        for ($i = 0; $i -lt 20; $i++) {
            Start-Sleep -Milliseconds 500
            $stillUp = Get-Process -Name "forgec2-server" -ErrorAction SilentlyContinue
            if (-not $stillUp) { break }
        }
        if (Get-Process -Name "forgec2-server" -ErrorAction SilentlyContinue) {
            Fail "the old instance is still running - aborting before the port fight"
            exit 1
        }
        Ok "old instance stopped"
    } else {
        Ok "no running instance found"
    }

    # The candidate was completed before the old process stopped. Replace the
    # locked executable only now, while the previous binary is available for
    # rollback.
    if (Test-Path -LiteralPath $binary) {
        Remove-Item -LiteralPath $binary -Force
    }
    Move-Item -LiteralPath $candidate -Destination $binary
    Ok "candidate promoted to forgec2-server.exe"

    # --------------------------------------------------------------- 4. start it up
    Step "Start the new instance"
    if (-not (Test-Path "logs")) { New-Item -ItemType Directory -Path "logs" | Out-Null }
    # The child's stdout/stderr go to files: with the default inheritance the
    # server keeps the parent's console handles open and the caller blocks
    # until the process exits.
    $proc = Start-Process -WindowStyle Hidden -FilePath $binary `
        -ArgumentList "-config $Config" -PassThru `
        -RedirectStandardOutput "logs\stdout.log" -RedirectStandardError "logs\stderr.log"
    Ok "started (PID $($proc.Id))"

    # ----------------------------------------------------------------- 5. verify
    Step "Health check"
    $healthy = Wait-ForHealth -Process $proc -Port $Port -TimeoutSeconds $HealthTimeoutSeconds
    if (-not $healthy) {
        Fail "the new instance did not become healthy within ${HealthTimeoutSeconds}s"
        if (Test-Path "logs/forgec2.log") {
            Write-Host "--- logs/forgec2.log (tail) ---" -ForegroundColor Yellow
            Get-Content "logs/forgec2.log" -Tail 30
        }
        if ($NoRollback) { exit 1 }
        if (-not (Test-Path $previous)) {
            Fail "no previous binary to roll back to - the box stays down until the build is fixed"
            exit 1
        }

        Step "Rollback"
        Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
        Copy-Item -Force $previous $binary
        $rollback = Start-Process -WindowStyle Hidden -FilePath $binary `
            -ArgumentList "-config $Config" -PassThru `
            -RedirectStandardOutput "logs\stdout.log" -RedirectStandardError "logs\stderr.log"
        if (Wait-ForHealth -Process $rollback -Port $Port -TimeoutSeconds $HealthTimeoutSeconds) {
            Ok "rolled back to the previous build (PID $($rollback.Id)) - the console is up again"
        } else {
            Fail "the rollback did not come up either - check logs/forgec2.log"
        }
        exit 1
    }
    Ok "health ok"

    # A second probe: /ready must answer too (the DB is reachable), and the
    # login page must render (the embedded bundle is really there).
    try {
        $ready = Invoke-WebRequest -Uri "http://127.0.0.1:$Port/ready" -UseBasicParsing -TimeoutSec 5
        if ($ready.StatusCode -ne 200) { throw "ready returned $($ready.StatusCode)" }
        Ok "ready ok"
    } catch {
        Warn "the readiness probe failed: $($_.Exception.Message)"
    }
    try {
        $login = Invoke-WebRequest -Uri "http://127.0.0.1:$Port/login" -UseBasicParsing -TimeoutSec 5
        if ($login.StatusCode -ne 200) { throw "login returned $($login.StatusCode)" }
        Ok "console bundle served"
    } catch {
        Warn "the login page probe failed: $($_.Exception.Message)"
    }

    Write-Host "`n==> Deployed (PID $($proc.Id))" -ForegroundColor Green
    Write-Host "    console : http://127.0.0.1:$Port/  (bound to $bindHost)"
    Write-Host "    health  : http://127.0.0.1:$Port/health"
    Write-Host "    logs    : logs/forgec2.log, logs/stdout.log, logs/stderr.log"
}
finally {
    Pop-Location
}
