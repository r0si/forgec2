# ForgeC2 Go-agent local E2E: a throwaway server generates a real agent through
# the generate API, the agent registers/beacons, and real tasks round-trip.
#
#   powershell -NoProfile -File scripts\e2e-agent-local.ps1     # Windows EXE
#   pwsh      -NoProfile -File scripts/e2e-agent-local.ps1     # Linux ELF (CI)
#
# This is the operator-path counterpart to the build gates: it never calls the
# generator's Go functions directly. Everything goes through the same REST API
# an operator uses, so a regression anywhere between HTTP login, listener start,
# ldflag injection, registration, the beacon loop, task dispatch and result
# storage fails here - not in a lab VM a week later.
#
#   * builds a throwaway server (fresh data dir, plain-HTTP lab config,
#     generated beacon key and admin password; nothing is written outside
#     -WorkDir)
#   * creates an HTTP listener through POST /api/listeners
#   * asks POST /generate/{linux,exe} for a real agent, polls the build job and
#     downloads the artifact (the server mints the v3 registration secret)
#   * runs the agent with HOME/TEMP/XDG_CACHE_HOME/LOCALAPPDATA redirected, so
#     an operator's own agent state is never touched
#   * dispatches shell / read / find / get_sleep_mode and asserts the results
#     (shell is ops-profile-aware: a host the agent classifies as a sandbox
#     refuses it by design, so the script asserts that refusal instead of the
#     marker; read/task output is base64 on the wire and plain text once stored)
#   * with -Modules (comma-separated include list, e.g. "core") it also
#     exercises the honest exclusion layer end to end: a task owned by an
#     excluded module must fail with the named "not compiled into this build"
#     error, and the help listing must mark the exclusions
#
# Requires: go, curl, PowerShell 5.1+ (Windows) or pwsh (Linux).
# The Windows variant is the primary implant; the Linux variant runs in CI.
[CmdletBinding()]
param(
    [string]$RepoRoot,
    [string]$WorkDir = (Join-Path ([System.IO.Path]::GetTempPath()) 'forgec2-agent-e2e'),
    [int]$Port = 8110,
    [int]$BeaconPort = 8111,
    [string]$User = 'admin',
    [string]$Password,
    # Comma-separated agent module include list (empty = every module, "core" =
    # the minimal build). See docs/AGENT_MODULES.md.
    [string]$Modules,
    [ValidateSet('auto', 'windows', 'linux')][string]$TargetOs = 'auto',
    [int]$BuildTimeoutSec = 900,
    [int]$TaskTimeoutSec = 120,
    [switch]$KeepWork
)

$ErrorActionPreference = 'Stop'

$HostIsWindows = ($env:OS -eq 'Windows_NT')
if ($TargetOs -eq 'auto') { $TargetOs = if ($HostIsWindows) { 'windows' } else { 'linux' } }
if (($TargetOs -eq 'windows') -ne $HostIsWindows) {
    $other = if ($HostIsWindows) { 'windows' } else { 'linux' }
    throw "TargetOs=$TargetOs cannot execute on this host; pass -TargetOs $other"
}
$Curl = if ($HostIsWindows) { 'curl.exe' } else { 'curl' }
$SrvExe = if ($HostIsWindows) { 'forgec2-server.exe' } else { 'forgec2-server' }

function Step([string]$m) { Write-Host "=== $m ===" }

function New-Hex([int]$n) {
    $bytes = New-Object byte[] $n
    [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
    return (-join ($bytes | ForEach-Object { '{0:x2}' -f $_ }))
}

function New-StrongPassword {
    # Needs upper/lower/digit and >= 12 chars (config.isWeakDefaultPassword).
    return "E2e-Gk7-$(New-Hex 8)"
}

if (-not $Password) { $Password = New-StrongPassword }
$BeaconKey = New-Hex 32

$server = $null
$agent = $null
$pushed = $false
$fail = New-Object System.Collections.ArrayList
$script:jar = $null
$script:csrf = $null
# Only a run that reached the assertions with no failures may clean up: a crash
# anywhere above must leave the DB, binaries and logs behind for diagnosis.
$runGreen = $false

function Check([string]$name, [bool]$cond, [string]$detail) {
    if ($cond) {
        Write-Host "  PASS $name $detail"
    } else {
        Write-Host "  FAIL $name $detail"
        [void]$fail.Add($name)
    }
}

function Read-Json([string]$path) {
    # UTF-8, not the ANSI default: agent hostnames and task output are not
    # guaranteed to be ASCII.
    return ([System.IO.File]::ReadAllText($path, [System.Text.Encoding]::UTF8) | ConvertFrom-Json)
}

function Api-Get([string]$url, [string]$outFile) {
    & $Curl -s -b $script:jar -c $script:jar -H 'Accept: application/json' -o $outFile $url
    if ($LASTEXITCODE -ne 0) { throw "GET $url failed (curl exit $LASTEXITCODE)" }
    return (Read-Json $outFile)
}

function Api-PostJson([string]$url, [string]$json, [string]$outFile) {
    [System.IO.File]::WriteAllText($script:reqFile, $json, (New-Object System.Text.UTF8Encoding($false)))
    & $Curl -s -b $script:jar -c $script:jar -H 'Content-Type: application/json' -H 'Accept: application/json' `
        -H "X-CSRF-Token: $script:csrf" -X POST --data-binary "@$script:reqFile" -o $outFile $url
    if ($LASTEXITCODE -ne 0) { throw "POST $url failed (curl exit $LASTEXITCODE)" }
    return (Read-Json $outFile)
}

function Api-PostForm([string]$url, [string]$form, [string]$outFile) {
    & $Curl -s -b $script:jar -c $script:jar -H 'Accept: application/json' `
        -H "X-CSRF-Token: $script:csrf" -X POST -d $form -o $outFile $url
    if ($LASTEXITCODE -ne 0) { throw "POST $url failed (curl exit $LASTEXITCODE)" }
    return (Read-Json $outFile)
}

function Get-CsrfToken([string]$jarPath) {
    foreach ($line in [System.IO.File]::ReadAllLines($jarPath)) {
        # The CSRF cookie is HttpOnly, so its domain field may carry the curl
        # "#HttpOnly_" prefix; the name/value columns are unaffected.
        if ($line -notmatch 'forgec2_csrf') { continue }
        $fields = $line -split "`t"
        if ($fields.Count -ge 7 -and $fields[5] -eq 'forgec2_csrf') { return $fields[6] }
    }
    return $null
}

function Get-LogTail([string]$path, [int]$lines) {
    if (Test-Path $path) { Get-Content $path -Tail $lines | ForEach-Object { Write-Host "    $_" } }
}

try {
    if (-not $RepoRoot) {
        # $PSScriptRoot is still empty while a param() default is evaluated, so
        # resolve the repository here instead.
        $scriptPath = $MyInvocation.MyCommand.Path
        if (-not $scriptPath) { $scriptPath = $PSCommandPath }
        $RepoRoot = Split-Path -Parent (Split-Path -Parent $scriptPath)
    }
    if (-not (Get-Command $Curl -ErrorAction SilentlyContinue)) { throw "$Curl not found in PATH" }
    $WorkDir = [System.IO.Path]::GetFullPath($WorkDir)
    $RepoRoot = [System.IO.Path]::GetFullPath($RepoRoot)
    # Resolved here, before any step can fail, because the finally block tails this
    # file on a red run: if it were only assigned inside Step 3, a config-rewrite or
    # server-build failure would leave the diagnostic path empty and the whole point
    # of the tail ("the server log carries the real reason") would be lost exactly
    # when it is needed. Join-Path, not a literal "\": SetupLogRotation keeps only
    # filepath.Dir(-log-file) and writes forgec2.log under it, and on Linux a
    # backslash is not a separator, so "$WorkDir/logs\forgec2.log" is ONE filename —
    # the log landed at $WorkDir/forgec2.log while both the tail below and the CI
    # step read logs/forgec2.log. Every E2E failure printed "no server log".
    $script:LogFile = Join-Path (Join-Path $WorkDir 'logs') 'forgec2.log'
    $script:reqFile = "$WorkDir/request.json"
    $base = "http://127.0.0.1:$Port"
    Step "work dir: $WorkDir (target os: $TargetOs)"
    if (Test-Path $WorkDir) { Remove-Item -Recurse -Force $WorkDir }
    New-Item -ItemType Directory -Force -Path "$WorkDir/tmp" | Out-Null
    New-Item -ItemType Directory -Force -Path "$WorkDir/state" | Out-Null
    Push-Location $RepoRoot
    $pushed = $true

    Step "1/9 build the throwaway server"
    go build -o "$WorkDir/$SrvExe" ./cmd/server
    if ($LASTEXITCODE -ne 0) { throw 'server build failed' }

    Step "2/9 write the throwaway config"
    $cfg = Get-Content config.example.yaml -Raw
    $cfg = $cfg -replace '(?m)^(\s*)port: 8000', "`${1}port: $Port"
    $cfg = $cfg -replace '(?m)^(\s*)host: 0.0.0.0', "`${1}host: 127.0.0.1"
    $cfg = $cfg -replace '(?m)^(\s*)tls_enabled: true', "`${1}tls_enabled: false"
    $cfg = $cfg -replace '(?m)^(\s*)require_tls_for_auth: true', "`${1}require_tls_for_auth: false"
    # No end-of-line anchor: the example carries a trailing comment on the
    # data_dir line, and the trailing text must survive the rewrite.
    $cfg = $cfg -replace '(?m)^([ \t]*)data_dir: data\b', "`${1}data_dir: $WorkDir/data"
    $cfg = $cfg -replace '(?m)^(\s*)path: data/db/forgec2.db', "`${1}path: $WorkDir/data/db/forgec2.db"
    $cfg = $cfg -replace '(?m)^(\s*)beacon_key: ""', "`${1}beacon_key: `"$BeaconKey`""
    $cfg = $cfg -replace '(?m)^(\s*)default_password: ""', "`${1}default_password: `"$Password`""
    if ($cfg -notmatch "port: $Port" -or $cfg -notmatch 'tls_enabled: false' -or
        $cfg -notmatch 'require_tls_for_auth: false' -or $cfg -notmatch [regex]::Escape("data_dir: $WorkDir/data") -or
        $cfg -notmatch [regex]::Escape("path: $WorkDir/data/db/forgec2.db") -or
        $cfg -notmatch [regex]::Escape($Password) -or $cfg -notmatch [regex]::Escape($BeaconKey)) {
        throw 'config rewrite did not apply cleanly - is config.example.yaml unchanged?'
    }
    # No BOM: the server's YAML reader does not need one, and UTF-8 keeps any
    # non-ASCII comment intact.
    [System.IO.File]::WriteAllText("$WorkDir/config.e2e.yaml", $cfg, (New-Object System.Text.UTF8Encoding($false)))
    Write-Host "  wrote $WorkDir/config.e2e.yaml"

    Step "3/9 start the throwaway server on $base"
    # The server must run with the repository as its working directory: agent
    # generation adds a `replace github.com/forgec2/forgec2 => <module root>`
    # directive found by walking up from the CWD (payload.forgeC2ModuleReplace),
    # so a server started elsewhere cannot build payloads. Logs are redirected
    # into the work dir with -log-file to keep the repository's logs/ untouched
    # ($script:LogFile, resolved up top so the finally block can tail it even if a
    # step before this one throws).
    $sp = @{
        FilePath         = "$WorkDir/$SrvExe"
        ArgumentList     = @('-config', "$WorkDir/config.e2e.yaml", '-log-file', $script:LogFile)
        WorkingDirectory = $RepoRoot
        PassThru         = $true
    }
    if ($HostIsWindows) { $sp['WindowStyle'] = 'Hidden' }
    $server = Start-Process @sp
    $healthy = $false
    for ($i = 0; $i -lt 80; $i++) {
        Start-Sleep -Milliseconds 500
        $health = & $Curl -s --max-time 2 "$base/health"
        if ($LASTEXITCODE -eq 0 -and $health -match 'ok') { $healthy = $true; break }
    }
    if (-not $healthy) { Get-LogTail $script:LogFile 30; throw "the throwaway server never became healthy on port $Port" }
    Write-Host '  /health ok'

    Step '4/9 log in and read the CSRF cookie'
    # A successful /api/login answers 302 towards the SPA; curl keeps the
    # Set-Cookie header either way (and does not follow redirects by default).
    $script:jar = "$WorkDir/cookies.txt"
    $code = & $Curl -s -c $script:jar -o "$WorkDir/login-body.txt" -w '%{http_code}' `
        -H 'Accept: application/json' -d "username=$User&password=$Password" "$base/api/login"
    if ($code -ne '302' -and $code -ne '200') {
        throw "login failed with HTTP ${code}: $(Get-Content "$WorkDir/login-body.txt" -Raw)"
    }
    # An authenticated GET rotates the CSRF cookie to the session-derived value
    # (see AGENTS.md); mutations below echo it back in X-CSRF-Token.
    [void](Api-Get "$base/api/v1/tasks" "$WorkDir/csrf-probe.json")
    $script:csrf = Get-CsrfToken $script:jar
    if (-not $script:csrf) { throw 'forgec2_csrf cookie not found after login' }
    Write-Host "  logged in as $User, csrf $($script:csrf.Substring(0, [Math]::Min(8, $script:csrf.Length)))..."

    Step "5/9 create the HTTP beacon listener on 127.0.0.1:$BeaconPort"
    $listenerJson = (@{
            name    = 'e2e-agent-listener'
            scheme  = 'http'
            host    = '127.0.0.1'
            port    = $BeaconPort
            enabled = $true
        } | ConvertTo-Json -Compress)
    $created = Api-PostJson "$base/api/listeners" $listenerJson "$WorkDir/listener.json"
    if (-not $created.success) { throw "listener creation failed: $(Get-Content "$WorkDir/listener.json" -Raw)" }
    # Envelope: handleCreateListener answers respond(c, {"listener": l}), so the
    # raw curl body is {"success":true,"data":{"listener":{...}}}. The task/agent
    # calls below already read .data; these three (listener / generate / build
    # status) did not, so id and status came back empty and the status -ne
    # 'running' check below threw on a listener that really was running.
    $listenerId = $created.data.listener.id
    $listenerStatus = $created.data.listener.status
    Write-Host "  listener id=$listenerId status=$listenerStatus"
    if ($listenerStatus -ne 'running') { throw "listener did not start (status=$listenerStatus)" }

    Step '6/9 generate a real agent through the API'
    $agentName = if ($TargetOs -eq 'windows') { 'forgec2-e2e-agent.exe' } else { 'forgec2-e2e-agent' }
    $genPath = if ($TargetOs -eq 'windows') { '/generate/exe' } else { '/generate/linux' }
    # interval=2 keeps the round-trip fast; the clamp floor is 1s.
    $genForm = "listener_id=$listenerId&interval=2&jitter=0&filename=$agentName"
    if ($Modules) { $genForm += "&modules=$Modules" }
    $gen = Api-PostForm "$base$genPath" $genForm "$WorkDir/generate.json"
    if (-not $gen.success) { throw "generate request failed: $(Get-Content "$WorkDir/generate.json" -Raw)" }
    $buildId = $gen.data.build_id
    Write-Host "  build $buildId queued"
    $deadline = (Get-Date).AddSeconds($BuildTimeoutSec)
    $status = 'building'
    $polls = 0
    while ((Get-Date) -lt $deadline) {
        Start-Sleep -Seconds 2
        $polls++
        $b = Api-Get "$base/generate/builds/$buildId" "$WorkDir/build.json"
        $status = $b.data.status
        if ($status -ne 'building') { break }
        if ($polls % 10 -eq 0) { Write-Host "  still building ($([int]((Get-Date) - $deadline.AddSeconds($BuildTimeoutSec)).TotalSeconds)s elapsed)..." }
    }
    if ($status -ne 'completed') {
        $err = if ($b.data.error) { $b.data.error } else { '(no error field)' }
        throw "agent build did not complete: status=$status error=$err"
    }
    $agentPath = "$WorkDir/$agentName"
    # The SPA middleware (server_http.go) answers any non-API GET that does not
    # ask for JSON with index.html, so this GET must carry the Accept header the
    # status polls already send - without it the "download" silently saves the
    # SPA shell.
    & $Curl -s -b $script:jar -H 'Accept: application/json' -o $agentPath "$base/generate/builds/$buildId/download"
    if ($LASTEXITCODE -ne 0) { throw 'agent download failed' }
    if (-not (Test-Path $agentPath)) { throw 'agent download produced no file' }
    $agentSize = (Get-Item $agentPath).Length
    if ($agentSize -lt 1024) { throw "agent artifact is only $agentSize bytes: $(Get-Content $agentPath -Raw)" }
    # An HTTP error body or a stray index.html would still be >1KB: require the
    # executable magic before running it.
    $magic = [System.IO.File]::ReadAllBytes($agentPath)[0..3]
    $magicOk = if ($TargetOs -eq 'windows') {
        $magic[0] -eq 0x4D -and $magic[1] -eq 0x5A                                    # MZ
    } else {
        $magic[0] -eq 0x7F -and $magic[1] -eq 0x45 -and $magic[2] -eq 0x4C -and $magic[3] -eq 0x46  # ELF
    }
    if (-not $magicOk) {
        throw "downloaded artifact is not a $TargetOs executable: $($agentSize) bytes, magic $([System.BitConverter]::ToString($magic))"
    }
    if ($TargetOs -eq 'linux') { & chmod +x $agentPath; if ($LASTEXITCODE -ne 0) { throw 'chmod +x failed' } }
    Write-Host "  downloaded $agentName ($agentSize bytes)"

    Step '7/9 run the agent with isolated state and wait for check-in'
    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = $agentPath
    $psi.WorkingDirectory = $WorkDir
    $psi.UseShellExecute = $false
    # Redirect every directory the agent persists into: identity.key lives under
    # the user cache dir, scratch files under the temp dir.
    #
    # Forward slashes throughout, like the TEMP lines above: on Linux a backslash
    # is an ordinary filename character, so "$WorkDir/state\home" would be a
    # sibling named `state\home` under the work dir rather than `home` under
    # state/, which the isolation intent and the cleanup step (which removes
    # state/) both assume is not the case.
    $psi.EnvironmentVariables['TEMP'] = "$WorkDir/tmp"
    $psi.EnvironmentVariables['TMP'] = "$WorkDir/tmp"
    $psi.EnvironmentVariables['TMPDIR'] = "$WorkDir/tmp"
    $psi.EnvironmentVariables['XDG_CACHE_HOME'] = "$WorkDir/state/cache"
    $psi.EnvironmentVariables['HOME'] = "$WorkDir/state/home"
    $psi.EnvironmentVariables['LOCALAPPDATA'] = "$WorkDir/state/appdata"
    $agent = [System.Diagnostics.Process]::Start($psi)
    $agentId = $null
    $agentStatus = $null
    $agentInfo = $null
    for ($i = 0; $i -lt 90; $i++) {
        Start-Sleep -Seconds 1
        if ($agent.HasExited) { throw "the agent exited early with code $($agent.ExitCode)" }
        # apiListAgents answers respond(c, {"agents": [...]}), so the array is at
        # .data.agents — reading .data yields the wrapper OBJECT, $agents[0].id is
        # then empty, and a fully registered online agent is reported as "never
        # registered". The leftover agents.json in the work dir is the proof.
        try { $agents = @((Api-Get "$base/api/v1/agents" "$WorkDir/agents.json").data.agents) } catch { continue }
        if ($agents.Count -gt 0) {
            $agentId = $agents[0].id
            $agentStatus = $agents[0].status
            $agentInfo = $agents[0]
            # The v3 registration frame carries no host info (the server creates
            # a minimal row on purpose - see ensureBeaconImplantRow); identity
            # attributes arrive with the next encrypted beacon and are
            # re-asserted from then on. Waiting for them here exercises the
            # refresh path instead of just the first row insert.
            if ($agentStatus -eq 'online' -and $agentInfo.hostname) { break }
        }
    }
    if (-not $agentId) { throw 'the agent never registered' }
    Write-Host "  agent $agentId status=$agentStatus host=$($agentInfo.hostname) os=$($agentInfo.os)/$($agentInfo.arch)"
    if ($agentStatus -ne 'online') { throw "agent is not online: $agentStatus" }

    Step '8/9 create the file fixtures and dispatch tasks'
    $markerHex = New-Hex 6
    $shellMarker = "FORGEC2-E2E-SHELL-$markerHex"
    $fileMarker = "FORGEC2-E2E-FILE-$markerHex"
    $markerName = "e2e-marker-$markerHex.txt"
    $markerPath = "$WorkDir/$markerName"
    [System.IO.File]::WriteAllText($markerPath, $fileMarker, (New-Object System.Text.UTF8Encoding($false)))

    # Contract probe (regression guard): read's spec parameter is 'command'
    # (the file path). Sending only 'path' must answer 400 naming the missing
    # parameter - the opaque 500 "task creation failed" is what this E2E
    # originally hit on the operator path.
    $badBody = (@{ agent_id = $agentId; type = 'read'; path = $markerPath } | ConvertTo-Json -Compress)
    [System.IO.File]::WriteAllText($script:reqFile, $badBody, (New-Object System.Text.UTF8Encoding($false)))
    $badCode = & $Curl -s -b $script:jar -H 'Accept: application/json' -H "X-CSRF-Token: $script:csrf" `
        -H 'Content-Type: application/json' -X POST --data-binary "@$script:reqFile" `
        -o "$WorkDir/task-create-read-missing-param.json" -w '%{http_code}' "$base/api/v1/tasks"
    $badErr = [string](Read-Json "$WorkDir/task-create-read-missing-param.json").error

    # Honest-exclusion probes (only when this run compiles a module subset):
    # gossip_discover belongs to the optional p2p module and is side-effect
    # free, so it is the probe whenever p2p is not in the include list; help
    # must mark the exclusions in its listing.
    $probeTask = $null
    $probeModule = $null
    if ($Modules) {
        $included = @($Modules -split ',' | ForEach-Object { $_.Trim().ToLowerInvariant() })
        if ($included -notcontains 'p2p') { $probeTask = 'gossip_discover'; $probeModule = 'p2p' }
    }

    $cases = @(
        @{ name = 'shell';          type = 'shell';          command = "echo $shellMarker"; path = $null },
        @{ name = 'read';           type = 'read';           command = $markerPath;         path = $null },
        @{ name = 'find';           type = 'find';           command = 'e2e-marker-*.txt';  path = $WorkDir },
        @{ name = 'get_sleep_mode'; type = 'get_sleep_mode'; command = $null;               path = $null }
    )
    if ($Modules) { $cases += @{ name = 'help'; type = 'help'; command = $null; path = $null } }
    if ($probeTask) { $cases += @{ name = $probeTask; type = $probeTask; command = $null; path = $null } }
    $results = @{}
    foreach ($case in $cases) {
        $body = [ordered]@{ agent_id = $agentId; type = $case.type }
        if ($case.command) { $body['command'] = $case.command }
        if ($case.path) { $body['path'] = $case.path }
        $createdTask = Api-PostJson "$base/api/v1/tasks" ($body | ConvertTo-Json -Compress) "$WorkDir/task-create-$($case.name).json"
        $taskId = $createdTask.data.id
        if (-not $taskId) { throw "task $($case.name) was not created: $(Get-Content "$WorkDir/task-create-$($case.name).json" -Raw)" }
        $taskDeadline = (Get-Date).AddSeconds($TaskTimeoutSec)
        $task = $null
        while ((Get-Date) -lt $taskDeadline) {
            Start-Sleep -Seconds 1
            $task = (Api-Get "$base/api/v1/tasks/$taskId" "$WorkDir/task-$($case.name).json").data
            if ($task.status -eq 'completed' -or $task.status -eq 'failed') { break }
        }
        $results[$case.name] = $task
        Write-Host "  [$($case.name)] task $taskId status=$($task.status)"
    }

    Step '9/9 assertions'
    Check 'agent registered' ($null -ne $agentId) "(id $agentId, $($agentInfo.hostname))"
    Check 'agent online' ($agentStatus -eq 'online') ''
    # Identity arrives on the beacon after registration: proving it here covers
    # the row-refresh path that keeps hostname/os/arch/pid/listener populated.
    Check 'agent identity refreshed' ([bool]$agentInfo.hostname -and [bool]$agentInfo.os -and [bool]$agentInfo.arch) `
        "(host=$($agentInfo.hostname) os=$($agentInfo.os)/$($agentInfo.arch) pid=$($agentInfo.pid) listener=$($agentInfo.listener_id))"
    Check 'missing parameter answers 400' ($badCode -eq 400 -and $badErr -match "requires 'command' parameter") "(HTTP ${badCode}: $badErr)"
    foreach ($case in $cases) {
        $t = $results[$case.name]
        if (-not $t) { Check "$($case.name) reached the agent" $false '(no result)'; continue }
        # shell is ops-profile-aware: a host the agent classifies as sandbox
        # refuses it by design, and that refusal is the correct end-to-end
        # outcome there (it still proves dispatch, execution gating and result
        # storage).
        $shellRefused = ($case.name -eq 'shell') -and ($t.error -match 'blocked by ops profile')
        # The excluded-module probe is expected to fail - with the named error.
        $probeRefused = ($probeTask -and $case.name -eq $probeTask) -and ($t.status -eq 'failed') -and ([string]$t.error -match 'not compiled into this build')
        Check "$($case.name) reached the agent" ($t.status -eq 'completed' -or $shellRefused -or $probeRefused) `
            $(if ($t.status -ne 'completed') { "error: $($t.error)" } else { '' })
    }
    $shellResult = [string]$results['shell'].result
    $readResult = [string]$results['read'].result
    $findResult = [string]$results['find'].result
    $sleepResult = [string]$results['get_sleep_mode'].result
    # A marker that could come from the script's own echo is not evidence: the
    # shell marker is random per run.
    if ($results['shell'].error -match 'blocked by ops profile') {
        # The refusal must be a refusal: no command output may leak through.
        Check 'shell refused by ops profile' (-not ($shellResult -match $shellMarker)) "(host classified sandbox: $($results['shell'].error))"
    } else {
        Check 'shell output' ($shellResult -match $shellMarker) ''
    }
    # read/task base64 output is decoded by the server before storage, so the
    # file content reaches the operator as plain text.
    Check 'read file content' ($readResult -match $fileMarker) ''
    Check 'find hit' ($findResult -match "$([regex]::Escape($markerName))`t\d+`t\d{4}-\d{2}-\d{2} \d{2}:\d{2}") ''
    Check 'get_sleep_mode' ($sleepResult -match 'default') ''
    if ($probeTask) {
        $probeResult = $results[$probeTask]
        $probeError = [string]$probeResult.error
        Check 'excluded module answers honestly' `
            ($probeResult.status -eq 'failed' -and $probeError -match 'not compiled into this build' -and $probeError -match [regex]::Escape($probeModule)) `
            "(status=$($probeResult.status) error=$probeError)"
        $helpResult = [string]$results['help'].result
        Check 'help marks excluded modules' `
            ($helpResult -match 'not compiled into this build' -and $helpResult -match "module `"$probeModule`"") `
            "(help header: $($helpResult.Split("`n")[0]))"
        # The header summary must describe the actual build: an excluded-module
        # build must never claim "all modules (hand-built agent)".
        Check 'help reports the compiled set' `
            ($helpResult -match 'agent modules: ' -and $helpResult -notmatch 'all modules') `
            "(help header: $($helpResult.Split("`n")[0]))"
    }
    $runGreen = ($fail.Count -eq 0)
}
finally {
    if ($agent -and -not $agent.HasExited) { $agent.Kill(); Write-Host '  agent stopped' }
    if ($server -and -not $server.HasExited) { $server.Kill(); Write-Host '  server stopped' }
    if ($server) { try { $server.WaitForExit(5000) | Out-Null } catch { } }
    if ($pushed) { Pop-Location }
    if (-not $runGreen) {
        # Red run: the server log carries the real reason (payload build errors,
        # beacon rejections) while the HTTP responses stay generic.
        Get-LogTail $script:LogFile 60
    }
    if ($runGreen -and -not $KeepWork) {
        # Drop the throwaway DB, config, cookie jar and binaries of a green run
        # (they carry a server-minted registration secret).
        foreach ($p in @($SrvExe, 'config.e2e.yaml', 'cookies.txt', 'data', 'state', 'tmp', 'logs',
                'agents.json', 'csrf-probe.json', 'listener.json', 'generate.json', 'build.json',
                'request.json', 'login-body.txt')) {
            Remove-Item -Recurse -Force (Join-Path $WorkDir $p) -ErrorAction SilentlyContinue
        }
        Get-ChildItem $WorkDir -Filter 'task-*.json' -ErrorAction SilentlyContinue | Remove-Item -Force -ErrorAction SilentlyContinue
        Get-ChildItem $WorkDir -Filter 'e2e-marker-*.txt' -ErrorAction SilentlyContinue | Remove-Item -Force -ErrorAction SilentlyContinue
        Get-ChildItem $WorkDir -Filter 'forgec2-e2e-agent*' -ErrorAction SilentlyContinue | Remove-Item -Force -ErrorAction SilentlyContinue
        Write-Host '  cleaned run artifacts'
    } else {
        Write-Host "  artifacts kept in $WorkDir"
    }
}

Write-Host ''
if ($fail.Count -gt 0) { Write-Host "Go agent E2E FAILED: $($fail -join ', ')"; exit 1 }
Write-Host 'Go agent E2E ALL GREEN'
exit 0
