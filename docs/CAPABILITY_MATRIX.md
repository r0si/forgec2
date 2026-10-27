# ForgeC2 Capability Matrix

> Status of implant tasks / transports as of **v2.7.1**.
> Quality: **Core** (production) · **Hardened** (usable OPSEC) · **Scripted** (PS/external) · **Experimental** · **Stub** (Windows-only or incomplete).  
> Command inventory: generate with `node scripts/gen-command-reference.mjs` → `docs/COMMAND_REFERENCE.md`.  
> Task inventory markers + version stamp: `node scripts/gen-capability-matrix.mjs` (CI `--check`; source `VERSION`).  
> Architecture blueprint: `docs/MATURITY_BLUEPRINT.md`.

## Transports

| Transport | Generate UI | Server listener | Implant | Quality |
|-----------|-------------|-----------------|---------|---------|
| HTTP(S) | Yes | Yes | Yes | Core |
| TCP | Yes | Yes | Yes | Core |
| DNS | Yes (+ DoH/DoT fields) | Yes | Yes | Hardened |
| WSS | Yes (Beacon Transport) | Yes | Yes | Hardened — persistent WS + binary frames + ping |
| gRPC | Yes (Beacon Transport) | Yes | Yes | Hardened — `grpcs://` TLS + keepalive/`MaxConnectionAge`; `grpc://` lab insecure only; mTLS when client CA set |
| SSH | Yes (+ creds + host-key pin) | Yes | Yes | Experimental — host key pin via ldflag/auto server key; empty pin = lab ignore |
| ICMP | Yes | Yes (FC2I fragments) | Win/Linux | Hardened — envelopes split across Echo payloads |
| UDP | Yes (experimental) | Yes (`udp_enabled`) | Yes | Experimental — one datagram per beacon |
| QUIC | Yes (experimental) | Yes (`quic_enabled`, TLS 1.3 ALPN h3/fc2) | Yes | Hardened — stream-framed v2 envelope; MaxIncomingStreams cap; path migration off; accept-loop backoff |
| mTLS / h2c | Selector only | Partial | Code present | Experimental |
| SMB P2P | P2P mode | Win pipe / Unix socket | Yes | Hardened |

## Core post-ex (all GOOS unless noted)

| Task | Windows | Linux | Darwin | Quality |
|------|---------|-------|--------|---------|
| shell / interactive shell | Yes | Yes | Yes | Core |
| ls / read / write / upload / download / find | Yes | Yes | Yes | Core |
| file_hunt | Yes | Yes | Yes | Hardened — listing-first, count/size/depth caps; optional small-file download |
| screen_trigger_start / stop | Yes | Yes (xdotool) | Yes (osascript) | Hardened — title match + cooldown; frames saved as screenshots |
| usb_enum | Yes (GetDriveType) | Yes (/sys/block) | Yes (diskutil) | Hardened — discovery only |
| usb_drop | Yes | Yes | Yes | Hardened — explicit source path required; dest must be removable; never copies the implant exe |
| browser_history | Yes | Yes | Yes | Hardened — copy-then-query SQLite, 200 rows |
| session_recon | Yes | Yes | Yes | Hardened — sessions/idle/active window |
| ps / killproc / suspend / resume | Yes | Yes | Limited | Core |
| screenshot | Yes | Tools-based | Tools-based | Hardened |
| set_sleep / config_push / profile_rotate | Yes | Yes | Yes | Core |
| process_tree | Yes (PPID tree) | Yes (/proc) | Yes (ps -axo) | Core |
| socks / rportfwd | Yes | Limited | Limited | Hardened |
| tun_start / tun_stop | Yes (bundled Wintun; needs admin) | Yes (/dev/net/tun) | Stub | Linux TUN + teamserver UDP helper; Windows ships the signed Wintun runtime — adapter creation needs an elevated implant |

## Credentials & AD (Windows primary)

| Task | Quality | Notes |
|------|---------|-------|
| creds | Scripted | Disk artifacts (SAM/minidump paths) |
| mimikatz / dcsync / tickets | Scripted | Requires **Modules** store `Invoke-Mimikatz.ps1` (no remote IEX) |
| kerberoast / asreproast | Scripted | Large PS payloads |
| ADCS ESC1–8 | Experimental | PS + certreq |
| browser / cookie / wifi / vpn | Hardened | Platform-specific; Chrome 127+ v20 App-Bound (DPAPI + honest IElevator miss) |
| cookie isolated proxy | Hardened | 127.0.0.1 HTTP inject; HTTPS CONNECT is tunnel-only (import Netscape jar) |
| SCCM recon | Scripted | Windows registry + WMI; discovery only |
| Entra PRT recon | Scripted | dsregcmd / CloudAP join state — not a PRT dump |
| Entra device-code / consent | Hardened | Server-side; lab client_id required; tokens not persisted |

## Lateral & privesc

| Task | Windows | Linux/macOS | Quality |
|------|---------|-------------|---------|
| lateral_wmi / winrm / psexec / dcom | Yes | Stub | Scripted / Hardened |
| ssh_lateral / scp / ssh_tunnel | Yes | Yes | Hardened (Go SSH) |
| token_* / UAC / potato | Yes | Stub | Hardened (Win) |
| container_detect | Detect | Linux real | Experimental escape |

## Execution / inject

| Task | Quality | Notes |
|------|---------|-------|
| execute_assembly / powerpick / peloader | Hardened | Windows |
| bof | Hardened | Windows |
| inject / shinject / threadless… | Hardened | Windows-only suite; hollow uses per-call random benign host (rundll32/dllhost/svchost/explorer) with fallback |
| edr_blind / edr_kill / byovd_load | Experimental | Windows-only EDR pack; approval-gated; byovd_load takes an operator-supplied .sys (no bundled driver) |
| ppl_check | Hardened | Windows-only read-only protection-level query |

## Server product modules

| Feature | Quality | Notes |
|---------|---------|-------|
| Agents / Listeners / Generate | Core | |
| AI assistant | Hardened | Needs API key |
| Automation / webhooks | Core | |
| Phishing SMTP + landing | Hardened | Real send; authorized use only |
| Integrations CRUD | Hardened | Persisted webhooks |
| Modules store | Core | Settings → Modules; deploy to agent via upload task |
| Scripting | Hardened | DB-persisted scripts |
| Remote desktop | Experimental | Screenshot stream + input |
| NTLM coerce/relay UI | Experimental | Lab section; Windows-centric |
| Cloud steal UI | Experimental | Lab; result polling |
| Container UI | Experimental | Detect better on Linux; escape often stub elsewhere |
| OpenAPI | Hardened | ~98% inventory; core schemas for health/agents/modules/dashboard; stale=0; CI ≥90% |
| API smoke | Hardened | `scripts/api-smoke.ps1` (+ `-TryDefaultAdmin`); modules JSON envelope check |
| Lateral UI | Hardened / Scripted | Method-dependent; Win-primary for many paths |
| Privesc UI | Hardened / Scripted | Checks ≠ guaranteed elevation |
| Scanner UI | Hardened | Agent-side port scan; not full Nmap |
| ATT&CK coverage UI | Hardened | Task-type mapping only — not proof of compromise |
| Circuit breaker UI | Hardened | Listener probe/failover; not full multi-C2 mesh |

## Malleable C2 profiles (v2)

| Feature | Status | Notes |
|---------|--------|-------|
| Multi-URI rotation (round-robin) | Hardened | `beacon_uris`; primary stays `beacon_uri` for old agents; server NoRoute accepts custom URIs even with no global preset |
| Request placements (cookie/query/header cover copies) | Hardened | `placements: [{target, chain}]`; canonical body always sent; server scans all query/cookie values so param-name rotation works |
| Transform chains (base64/url, netbios(u), xor, mask, strrep, case, urlencode, uri_append) | Hardened | Agent/server engines aligned (full-key xor, `key;offset` mask); Validate endpoint round-trips each chain |
| Header order determinism + UA pool | Hardened | Fixed browser order, sorted custom keys, per-beacon UA rotation (profile pool + built-ins) |
| URI jitter + param-name rotation + work window | Hardened | Junk query per beacon; query names rotate through pool; `work_start/end/tz` gates beacons |
| CS `.profile` import + Validate (dry-run) | Hardened | `POST /api/generate/profile/import-text`, `/api/generate/profile/validate` |
| Response output chains | Experimental | `server_output` encodes only via matching global preset; per-file chains preview-only until global preset matches; server/agent engines aligned (reverse-order decode, `print` hex parity, prepend/append strip); Validate endpoint round-trips mixed-case envelopes and warns on lossy `case` chains + C-implant incompatibility (C decodes nothing) |

Rebuild implants after changing placement/header/timing fields — old agents keep the old shape.

## Operator rules

1. Prefer **Core/Hardened** for real engagements.  
2. **Scripted** tasks need modules/tools and leave more telemetry.  
3. **Experimental** must be validated per target; may return unknown/stub.  
4. After implant code changes, **regenerate** payloads (sleep/config alone is not enough).  
5. Upload `Invoke-Mimikatz.ps1` under **Settings → Modules** before credential dumps.

## Plugins & OTA

| Feature | Quality | Notes |
|---------|---------|-------|
| Manifest plugins (command/hook/report) | Hardened | 52 packaged; stdin JSON; env scrub + PATH allowlist; 2 MiB output cap |
| Plugin process isolation | Hardened | Windows Job Object KILL_ON_JOB_CLOSE; Unix Setpgid + group kill (P4-3) |
| goja JS scripting | Hardened | DB-persisted scripts |
| OTA self_update | Hardened | Ed25519 pin (`updatePinnedPubKeyHex`); bare URL refused; server sign API under Admin |
| Hot update (server) | Hardened | `crypto.update_signing_key` optional release signature |


## Task inventory (generated)

<!-- BEGIN GENERATED TASK INVENTORY -->

> **Generated** from `pkg/protocol/taskspec_data.go` by `node scripts/gen-capability-matrix.mjs`. Do not edit inside these markers.

- **Dispatchable task types:** 223 · **aliases:** 3 · **approval-gated:** 57

| Category | Types |
|----------|-------|
| Execution (30) | `abort`, `bof`, `bof_infection`, `chmod`, `clr_exec_assembly`, `clr_powershell`, `container_docker`, `container_k8s`, `download_url`, `execute_assembly`, `inject`, `inject_methods`, `interactive_shell_start`, `interactive_shell_stop`, `interactive_shell_write`, `killproc`, `mkdir`, `peloader`, `powerpick`, `reflectdll_inject`, `rename`, `resume`, `scp_upload`, `shell`, `shinject`, `shspawn`, `spawn`, `suspend`, `upload`, `window_close` |
| Discovery (36) | `adcs_find`, `adcs_full_audit`, `av`, `cert_store_list`, `container_detect`, `drives`, `edr_status`, `file_hunt`, `file_scan`, `file_search`, `find`, `find_delegation`, `hostinfo`, `ldap_acl`, `ldap_computers`, `ldap_groups`, `ldap_query`, `ldap_spn`, `ldap_users`, `ls`, `net`, `netstat`, `portscan`, `ppl_check`, `process_tree`, `ps`, `reg_get`, `run_egress`, `sccm_recon`, `services`, `session_recon`, `sharphound`, `token_whoami`, `usb_enum`, `users`, `window_list` |
| Collection (18) | `browser_history`, `clipboard_get`, `clipboard_set`, `cookie_export`, `download`, `file_thumb`, `keylogger_dump`, `keylogger_start`, `keylogger_stop`, `mic`, `read`, `remote_input`, `screen_trigger_start`, `screen_trigger_stop`, `screenshot`, `screenshot_window`, `webcam`, `wechat_history` |
| Credential Access (37) | `adcs_esc1`, `adcs_esc2`, `adcs_esc3`, `adcs_esc4`, `adcs_esc5`, `adcs_esc6`, `adcs_esc7`, `adcs_esc8`, `adcs_request`, `asreproast`, `bronze_bit`, `browser_steal`, `cloud_steal`, `coerce_dfs`, `coerce_petitpotam`, `coerce_printerbug`, `constrained_deleg`, `cred_check`, `creds`, `dcsync`, `dcsync_machine`, `dpapi_blob`, `dpapi_browser`, `dpapi_masterkey`, `entra_prt`, `golden_ticket`, `kerberoast`, `mimikatz`, `ntlm_help`, `password_spray`, `rbcd`, `relay_ntlm_start`, `relay_ntlm_stop`, `shadow_creds`, `silver_ticket`, `vpn_creds`, `wifi_creds` |
| Defense Evasion (29) | `amsi_bypass`, `amsi_hardware_bp`, `blockdlls`, `byovd_load`, `cleanup`, `edr_blind`, `edr_kill`, `enum_callbacks`, `etw_bypass`, `etw_hardware_bp`, `etwti`, `imgload`, `kernel_callback`, `kill_av`, `log_wipe`, `lsa_bypass`, `migrate`, `objcb`, `protect_process`, `reg_delete`, `reg_set`, `run_evasion`, `sandbox_detect`, `sandbox_detect_advanced`, `self_delete`, `set_sleep_mask`, `set_sleep_mask_advanced`, `track_wipe`, `unhook_ntdll` |
| Lateral Movement (10) | `lateral`, `lateral_dcom`, `lateral_psexec`, `lateral_winrm`, `lateral_wmi`, `pass_the_hash`, `pass_the_ticket`, `ssh_keygen`, `ssh_lateral`, `usb_drop` |
| Privilege Escalation (15) | `computerdefaults`, `container_escape`, `elevate`, `elevate_printnightmare`, `eventvwr`, `fodhelper`, `juicy_potato`, `named_pipe_impersonate`, `privesc_check`, `slui`, `token_list_procs`, `token_make`, `token_revert`, `token_steal`, `uac_bypass` |
| Persistence (4) | `adminsdholder`, `persistence_add`, `persistence_list`, `persistence_remove` |
| C2 / Session (27) | `beacon_now`, `clear_kill_date`, `config_push`, `get_sleep_mode`, `ghost_mode_exit`, `ghost_mode_status`, `gossip_discover`, `help`, `kill`, `lportfwd_start`, `lportfwd_stop`, `profile_rotate`, `rportfwd_start`, `rportfwd_stop`, `self_update`, `set_c2_mode`, `set_kill_date`, `set_sleep`, `set_sleep_mode`, `set_working_hours`, `socks`, `ssh_tunnel`, `tun_start`, `tun_stop`, `tunnel_add_route`, `tunnel_remove_route`, `uninstall` |
| Impact (4) | `delete`, `reboot`, `shutdown`, `wallpaper` |
| Other (13) | `amsi_session_bypass`, `etw_ntrace_bypass`, `execute_assembly_forkrun`, `lateral_list`, `lateral_scf`, `list_inject_methods`, `net_enum_hosts`, `net_scan_smb`, `rev2self`, `screen_stream_start`, `screen_stream_stop`, `screen_webrtc_start`, `screen_webrtc_stop` |

Full per-command parameters, aliases and help: [`COMMAND_REFERENCE.md`](COMMAND_REFERENCE.md).

<!-- END GENERATED TASK INVENTORY -->
## Regenerating this matrix

When adding task types, update:

- `pkg/protocol/tasks.go`
- `pkg/protocol/taskspec_data.go` (metadata — single source of truth)
- `internal/payload/agent/task_registry.go`
- `internal/server/tasktypes.go` (if API surface changes)
- This document (hand-written sections) + regenerate generated artifacts:

```bash
node scripts/gen-capability-matrix.mjs   # inject task inventory + VERSION stamp
node scripts/gen-command-reference.mjs   # docs/COMMAND_REFERENCE.md
node scripts/gen-capability-matrix.mjs --check
node scripts/gen-command-reference.mjs --check
```

Version header is stamped from the root `VERSION` file (`cat VERSION`).
