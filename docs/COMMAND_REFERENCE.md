# ForgeC2 Command Reference

> **Generated** from `pkg/protocol/taskspec_data.go` by
> `node scripts/gen-command-reference.mjs`. Do not edit by hand.

- **Task types:** 223
- **Constants in tasks.go:** 224
- **Aliases:** 3
- **Approval-gated:** 57
- **Parameters (total):** 80
- **Constants without specs:** shell_output (intentional: RESULT-type only, not dispatchable — see pkg/protocol/tasks.go)

## Categories

- [Execution](#execution) (30)
- [Discovery](#discovery) (36)
- [Collection](#collection) (18)
- [Credential Access](#credential-access) (37)
- [Defense Evasion](#defense-evasion) (29)
- [Lateral Movement](#lateral-movement) (10)
- [Persistence](#persistence) (4)
- [C2 / Session](#c2-session) (27)
- [Impact](#impact) (4)
- [Other](#other) (28)

## Execution

| Type | Name | Approval | Aliases | Parameters | Description |
|------|------|----------|---------|------------|-------------|
| `abort` | Abort Task | no | — | command:int* | Cancel a running or queued task by its task id |
| `bof` | BOF | no | — | command:string* | Execute Beacon Object File |
| `bof_infection` | BOF Infection | no | — | — | Download and execute BOF from server |
| `chmod` | Change Mode | no | — | command:string*, data:string* | Change file mode bits (POSIX; on Windows only the read-only bit is mapped) |
| `clr_exec_assembly` | CLR Exec Assembly | no | — | — | Execute .NET assembly via CLR |
| `clr_powershell` | CLR PowerShell | no | — | — | Run PowerShell via CLR |
| `container_docker` | Container Docker | no | — | — | Docker container operations |
| `container_k8s` | Container K8s | no | — | — | Kubernetes container operations |
| `download_url` | Download URL | no | — | command:string* | Download a file from a URL |
| `execute_assembly` | Execute Assembly | no | — | command:string* | Execute .NET assembly in memory |
| `inject` | Shellcode Inject | **yes** | — | command:string* | Inject shellcode into a process |
| `inject_methods` | Injection Methods | no | — | — | List available injection methods |
| `interactive_shell_start` | Interactive Shell | no | — | — | Start interactive shell session |
| `interactive_shell_stop` | Interactive Shell Stop | no | — | — | Stop interactive shell session |
| `interactive_shell_write` | Interactive Shell Write | no | — | — | Send input to interactive shell |
| `killproc` | Kill Process | no | — | command:int* | Terminate a process by PID |
| `mkdir` | Make Directory | no | — | command:string* | Create a directory (parents created as needed) |
| `peloader` | PE Loader | no | — | — | Load PE from memory |
| `powerpick` | PowerPick | no | — | command:string* | Run PowerShell without powershell.exe |
| `reflectdll_inject` | Reflective DLL Inject | **yes** | — | command:string* | Inject reflective DLL into process |
| `rename` | Rename / Move | no | — | command:string*, data:string* | Rename or move a file or directory |
| `resume` | Resume Process | no | — | command:int* | Resume a suspended process |
| `scp_upload` | SCP Upload | no | — | — | Upload file via SCP |
| `shell` | Shell | shell | — | command:string*, shell:string | Execute a shell command |
| `shinject` | Shellcode Inject (self) | **yes** | — | — | Inject shellcode into self |
| `shspawn` | Shellcode Spawn | **yes** | — | — | Spawn shellcode in new process |
| `spawn` | Spawn | no | — | — | Spawn a new agent process |
| `suspend` | Suspend Process | no | — | command:int* | Suspend a process by PID |
| `upload` | Upload File | no | — | command:string* | Upload a file to the agent |
| `window_close` | Window Close | no | — | command:string* | Close a window by HWND or title substring |

## Discovery

| Type | Name | Approval | Aliases | Parameters | Description |
|------|------|----------|---------|------------|-------------|
| `adcs_find` | ADCS Find | no | — | — | Enumerate ADCS certificate templates |
| `adcs_full_audit` | ADCS Audit | no | — | — | Full ADCS audit |
| `av` | Anti-Virus Check | no | — | — | List installed AV products |
| `cert_store_list` | Cert Store List | no | — | — | List certificate store contents |
| `container_detect` | Container Detect | no | — | — | Detect container environment |
| `drives` | List Drives | no | — | — | List available drives |
| `edr_status` | EDR Status | no | — | — | Check EDR status |
| `file_hunt` | File Hunt | no | `hunt` | command:string, path:string, data:string | Capped recursive glob listing (default: user profile, max 200 hits). Optional small-file download with byte caps — not whole-disk exfil. |
| `file_scan` | Sensitive File Scan | no | — | command:string, path:string*, data:string | Bounded read-only scan of text files for explicit sensitive-data rules. Email, phone and bank card are enabled by default; SSN, AWS key, private key, JWT and password detectors are opt-in. Raw matches stay in the encrypted task result and are redacted from AI, callbacks and exports. No OCR or document parsing. |
| `file_search` | File Metadata Search | no | — | command:string, path:string*, data:string | Structured, read-only file metadata search by category, glob, size, modification time and depth. Returns paths and metadata only (no file contents, thumbnails or OCR). |
| `find` | Find Files | no | — | command:string* | Search for files matching a pattern |
| `find_delegation` | Find Delegation | no | — | — | Find Kerberos delegation |
| `hostinfo` | Host Info | no | `hi` | command:string, data:string | Structured host profile sweep. Category selects the section so one expensive collection is never forced; result is JSON for table rendering |
| `ldap_acl` | LDAP ACL | no | — | — | Query AD ACLs via LDAP |
| `ldap_computers` | LDAP Computers | no | — | — | Query AD computers via LDAP |
| `ldap_groups` | LDAP Groups | no | — | — | Query AD groups via LDAP |
| `ldap_query` | LDAP Query | no | — | command:string* | Run arbitrary LDAP query |
| `ldap_spn` | LDAP SPN | no | — | — | Query SPN records via LDAP |
| `ldap_users` | LDAP Users | no | — | — | Query AD users via LDAP |
| `ls` | List Directory | no | — | command:string | List directory contents |
| `net` | Network Enum | no | — | command:string* | Enumerate network resources |
| `netstat` | Network Connections | no | — | — | Show active network connections |
| `portscan` | Port Scan | no | — | command:string* | Scan TCP ports on a target |
| `ppl_check` | PPL Check | no | — | — | Read-only query of own process protection level (signer/type). Windows only. |
| `process_tree` | Process List (ps alias) | no | — | — | Process list (alias of ps; not a true parent-child tree view) |
| `ps` | Process List | no | — | — | List running processes |
| `reg_get` | Registry Get | no | — | command:string* | Read a registry value |
| `run_egress` | Egress Check | no | — | — | Test egress connectivity |
| `sccm_recon` | SCCM Recon | no | — | — | Enumerate SCCM/MECM client config (registry + WMI). Windows only, discovery only. |
| `services` | List Services | no | — | — | List Windows services |
| `session_recon` | Session Recon | no | — | — | Logged-on sessions, console, lock/idle hints, active window. |
| `sharphound` | SharpHound | no | — | — | Run SharpHound for BloodHound |
| `token_whoami` | Token Whoami | no | — | — | Show current token identity |
| `usb_enum` | USB Enum | no | `usb` | — | List logical volumes and flag removable drives. Discovery only. |
| `users` | List Users | no | — | — | List logged-in users |
| `window_list` | Window List | no | — | — | List visible top-level windows (HWND, PID, title) |

## Collection

| Type | Name | Approval | Aliases | Parameters | Description |
|------|------|----------|---------|------------|-------------|
| `browser_history` | Browser History | no | — | command:string | Read Chromium/Firefox/Safari history SQLite (copy-then-query, capped). URLs and titles only. |
| `clipboard_get` | Clipboard Get | no | — | — | Read clipboard contents |
| `clipboard_set` | Clipboard Set | no | — | command:string* | Write to clipboard |
| `cookie_export` | Cookie Export | **yes** | — | — | Export browser cookies |
| `download` | Download File | no | — | command:string* | Download a file from the agent |
| `file_thumb` | File Thumbnails | no | — | path:string, data:string | Generate bounded JPEG thumbnails for up to 12 operator-selected image/video paths. Images use built-in decoders; video frames use ffmpeg when installed. Go agent only; thumbnails are cached server-side and never returned as task text. |
| `keylogger_dump` | Keylogger Dump | no | — | — | Dump captured keystrokes |
| `keylogger_start` | Keylogger Start | no | — | — | Start keylogging |
| `keylogger_stop` | Keylogger Stop | no | — | — | Stop keylogging |
| `mic` | Microphone Capture | no | — | command:string | Record audio from the default microphone |
| `read` | Read File | no | — | command:string* | Read a file from the agent |
| `remote_input` | Remote Input | no | — | — | Send mouse/keyboard input remotely |
| `screen_trigger_start` | Screen Trigger Start | no | — | command:string* | Watch the foreground window title and screenshot when it matches. Cooldown per title. |
| `screen_trigger_stop` | Screen Trigger Stop | no | — | — | Stop foreground-window screenshot trigger |
| `screenshot` | Screenshot | no | — | — | Capture screen |
| `screenshot_window` | Window Screenshot | no | — | — | Capture a specific window |
| `webcam` | Webcam Capture | no | — | command:string | Capture a still frame from the default webcam |
| `wechat_history` | WeChat History | no | — | — | Export WeChat chat history |

## Credential Access

| Type | Name | Approval | Aliases | Parameters | Description |
|------|------|----------|---------|------------|-------------|
| `adcs_esc1` | ADCS ESC1 | **yes** | — | — | ADCS ESC1 attack |
| `adcs_esc2` | ADCS ESC2 | **yes** | — | — | ADCS ESC2 attack |
| `adcs_esc3` | ADCS ESC3 | **yes** | — | — | ADCS ESC3 attack |
| `adcs_esc4` | ADCS ESC4 | **yes** | — | — | ADCS ESC4 attack |
| `adcs_esc5` | ADCS ESC5 | **yes** | — | — | ADCS ESC5 attack |
| `adcs_esc6` | ADCS ESC6 | **yes** | — | — | ADCS ESC6 attack |
| `adcs_esc7` | ADCS ESC7 | **yes** | — | — | ADCS ESC7 attack |
| `adcs_esc8` | ADCS ESC8 | **yes** | — | — | ADCS ESC8 attack |
| `adcs_request` | ADCS Request | no | — | — | Request a certificate |
| `asreproast` | AS-REP Roast | no | — | — | Roast AS-REP responses |
| `bronze_bit` | Bronze Bit | **yes** | — | — | Bronze Bit attack |
| `browser_steal` | Browser Steal | **yes** | — | — | Steal browser passwords |
| `cloud_steal` | Cloud Token Theft | **yes** | — | — | Steal cloud access tokens |
| `coerce_dfs` | Coerce DFS | **yes** | — | — | Coerce NTLM auth via DFS |
| `coerce_petitpotam` | Coerce PetitPotam | **yes** | — | — | Coerce NTLM auth via EFSRPC |
| `coerce_printerbug` | Coerce Printer Bug | **yes** | — | — | Coerce NTLM auth via MS-RPRN |
| `constrained_deleg` | Constrained Delegation | **yes** | — | — | Exploit constrained delegation |
| `cred_check` | Credential Check | no | — | command:string* | Validate a single credential against the domain with a lockout fuse |
| `creds` | Dump Credentials (scripted) | **yes** | — | — | Scripted: save SAM/SYSTEM/SECURITY hives and an lsass minidump to disk — not an in-memory parse |
| `dcsync` | DCSync | **yes** | — | command:string* | DCSync attack against a domain |
| `dcsync_machine` | DCSync Machine | **yes** | — | — | DCSync machine account |
| `dpapi_blob` | DPAPI Blob | **yes** | — | — | Decrypt DPAPI blob |
| `dpapi_browser` | DPAPI Browser | **yes** | — | — | Decrypt browser DPAPI data |
| `dpapi_masterkey` | DPAPI MasterKey | **yes** | — | — | Extract DPAPI master keys |
| `entra_prt` | Entra PRT Recon | no | — | — | dsregcmd / CloudAP recon. PRT blob dump needs SYSTEM; this reports join state. |
| `golden_ticket` | Golden Ticket | **yes** | — | command:string* | Forge a Golden Ticket |
| `kerberoast` | Kerberoast | **yes** | — | — | Request TGS for SPN accounts |
| `mimikatz` | Mimikatz (scripted) | **yes** | — | command:string* | Scripted: requires Settings → Modules Invoke-Mimikatz.ps1 (no remote IEX) |
| `ntlm_help` | NTLM Help | no | — | — | Show NTLM relay help |
| `password_spray` | Password Spray | **yes** | — | command:string*, data:string* | Try a password against multiple AD accounts with lockout awareness |
| `rbcd` | RBCD | **yes** | — | — | Resource-based constrained delegation |
| `relay_ntlm_start` | NTLM Relay | **yes** | — | — | Start NTLM relay listener |
| `relay_ntlm_stop` | NTLM Relay Stop | no | — | — | Stop NTLM relay listener |
| `shadow_creds` | Shadow Credentials | **yes** | — | — | Add shadow credentials to an account |
| `silver_ticket` | Silver Ticket | **yes** | — | command:string* | Forge a Silver Ticket |
| `vpn_creds` | VPN Credentials | no | — | — | Extract VPN credentials |
| `wifi_creds` | WiFi Credentials | no | — | — | Extract WiFi credentials |

## Defense Evasion

| Type | Name | Approval | Aliases | Parameters | Description |
|------|------|----------|---------|------------|-------------|
| `amsi_bypass` | AMSI Bypass | no | — | — | Bypass AMSI |
| `amsi_hardware_bp` | AMSI HW BP | no | — | — | AMSI bypass via HW breakpoints |
| `blockdlls` | Block DLLs | no | — | — | Block non-Microsoft DLLs |
| `byovd_load` | BYOVD Load (experimental) | **yes** | — | command:string* | Load an operator-supplied signed driver via SCM (no bundled binary). Windows only, approval-gated. |
| `cleanup` | Cleanup | no | — | — | Clean up artifacts |
| `edr_blind` | EDR Blind (experimental) | **yes** | — | — | User-mode sensor blinding: ETW patch + ntdll unhook (re-applied per beacon). Windows only, approval-gated. |
| `edr_kill` | EDR Kill (experimental) | **yes** | — | — | Terminate known AV/EDR processes. Windows only, approval-gated. |
| `enum_callbacks` | Enum Callbacks | no | — | — | Enumerate kernel callbacks |
| `etw_bypass` | ETW Bypass | no | — | — | Bypass ETW |
| `etw_hardware_bp` | ETW HW BP | no | — | — | ETW bypass via HW breakpoints |
| `etwti` | ETW TI | no | — | — | ETW threat intel evasion |
| `imgload` | Image Load Evasion | no | — | — | Image load callback evasion |
| `kernel_callback` | Kernel Callback | no | — | — | Kernel callback evasion |
| `kill_av` | Kill AV | **yes** | — | — | Attempt to kill antivirus processes |
| `log_wipe` | Log Wipe | **yes** | — | — | Wipe event logs |
| `lsa_bypass` | LSA Bypass | no | — | — | Bypass LSA protection |
| `migrate` | Migrate | **yes** | — | command:string | Copy the implant into a fresh process context and self-delete |
| `objcb` | ObjCB Evasion | no | — | — | Object callback evasion |
| `protect_process` | Protect Process | no | — | — | Protect process from termination |
| `reg_delete` | Registry Delete | no | — | command:string* | Delete a registry key |
| `reg_set` | Registry Set | no | — | command:string* | Set a registry value |
| `run_evasion` | Run Evasion | no | — | command:string* | Run an evasion technique by name |
| `sandbox_detect` | Sandbox Detect | no | — | — | Detect sandbox environment |
| `sandbox_detect_advanced` | Sandbox Detect Advanced | no | — | — | Advanced sandbox detection |
| `self_delete` | Self Delete | **yes** | — | — | Delete agent binary |
| `set_sleep_mask` | Set Sleep Mask | no | — | — | Set sleep mask |
| `set_sleep_mask_advanced` | Set Sleep Mask Advanced | no | — | — | Set advanced sleep mask |
| `track_wipe` | Track Wipe | **yes** | — | — | Wipe tracking files |
| `unhook_ntdll` | Unhook NTDLL | no | — | — | Unhook ntdll.dll |

## Lateral Movement

| Type | Name | Approval | Aliases | Parameters | Description |
|------|------|----------|---------|------------|-------------|
| `lateral` | Lateral Movement | **yes** | — | command:string* | Move laterally to another host |
| `lateral_dcom` | Lateral DCOM | **yes** | — | — | DCOM lateral movement |
| `lateral_psexec` | Lateral PsExec | **yes** | — | — | PsExec lateral movement |
| `lateral_winrm` | Lateral WinRM | **yes** | — | — | WinRM lateral movement |
| `lateral_wmi` | Lateral WMI | **yes** | — | — | WMI lateral movement |
| `pass_the_hash` | Pass the Hash | **yes** | — | command:string* | Pass-the-hash authentication |
| `pass_the_ticket` | Pass the Ticket | **yes** | — | command:string* | Pass-the-ticket authentication |
| `ssh_keygen` | SSH Keygen | no | — | — | Generate SSH key pair |
| `ssh_lateral` | SSH Lateral | **yes** | — | command:string* | SSH lateral movement |
| `usb_drop` | USB Drop | **yes** | — | path:string*, command:string, data:string | Copy an operator-specified file onto a removable volume. Refuses to use the implant binary as the source. |

## Persistence

| Type | Name | Approval | Aliases | Parameters | Description |
|------|------|----------|---------|------------|-------------|
| `adminsdholder` | AdminSDHolder | **yes** | — | — | AdminSDHolder operations |
| `persistence_add` | Persistence Add | **yes** | — | command:string* | Install persistence mechanism |
| `persistence_list` | Persistence List | no | — | — | List persistence mechanisms |
| `persistence_remove` | Persistence Remove | no | — | command:string* | Remove persistence mechanism |

## C2 / Session

| Type | Name | Approval | Aliases | Parameters | Description |
|------|------|----------|---------|------------|-------------|
| `beacon_now` | Beacon Now | no | — | — | Force immediate beacon check-in |
| `clear_kill_date` | Clear Kill Date | no | — | — | Clear agent expiry date |
| `config_push` | Config Push | no | — | — | Push configuration to agent |
| `get_sleep_mode` | Get Sleep Mode | no | — | — | Show current sleep mode |
| `ghost_mode_exit` | Ghost Mode Exit | no | — | — | Exit ghost mode |
| `ghost_mode_status` | Ghost Mode Status | no | — | — | Check ghost mode status |
| `gossip_discover` | P2P Discover | no | — | — | Discover peer agents |
| `help` | Help | no | — | — | List available commands with aliases, parameters and descriptions |
| `kill` | Kill Agent | **yes** | — | — | Terminate the agent process |
| `lportfwd_start` | Local Port Forward | no | — | command:string* | Bind a loopback listener on the agent and tunnel connections through the C2 server to the target (target sees the C2 egress IP). Gated by server.lportfwd_enabled |
| `lportfwd_stop` | Stop Local Port Forward | no | — | command:string* | Stop a tunneled local port forward by its listener port |
| `profile_rotate` | Profile Rotate | no | — | — | Rotate communication profile |
| `rportfwd_start` | Reverse Port Forward | no | — | command:string* | Start reverse port forward |
| `rportfwd_stop` | Reverse Port Forward Stop | no | — | command:string* | Stop reverse port forward |
| `self_update` | Self Update | no | — | command:string* | Replace agent binary with new version |
| `set_c2_mode` | Set C2 Mode | no | — | — | Switch between C2 modes |
| `set_kill_date` | Set Kill Date | no | — | — | Set agent expiry date |
| `set_sleep` | Set Sleep | no | — | command:int* | Change beacon sleep interval |
| `set_sleep_mode` | Set Sleep Mode | no | — | — | Change sleep variation mode |
| `set_working_hours` | Set Working Hours | no | — | — | Set agent working hours |
| `socks` | SOCKS Proxy | no | — | command:string* | Start a SOCKS proxy through agent |
| `ssh_tunnel` | SSH Tunnel | no | — | — | Create SSH tunnel |
| `tun_start` | TUN Start | no | — | command:string | Open a TUN and frame IP packets over the beacon (Linux /dev/net/tun; Windows uses the bundled Wintun runtime and needs an elevated implant for adapter creation). Pair with teamserver UDP helper. |
| `tun_stop` | TUN Stop | no | — | — | Stop the agent TUN interface |
| `tunnel_add_route` | Tunnel Add Route | no | — | — | Add tunnel route |
| `tunnel_remove_route` | Tunnel Remove Route | no | — | — | Remove tunnel route |
| `uninstall` | Uninstall Agent | **yes** | — | — | Self-uninstall the agent |

## Impact

| Type | Name | Approval | Aliases | Parameters | Description |
|------|------|----------|---------|------------|-------------|
| `delete` | Delete File | **yes** | — | command:string* | Delete a file or directory |
| `reboot` | Reboot | no | — | — | Reboot the system |
| `shutdown` | Shutdown | no | — | — | Shut down the system |
| `wallpaper` | Wallpaper | no | — | command:string*, data:string | Set the desktop wallpaper from an image URL or local path (Windows only) |

## Other

| Type | Name | Approval | Aliases | Parameters | Description |
|------|------|----------|---------|------------|-------------|
| `amsi_session_bypass` | AMSISession Byp | no | — | — |  |
| `computerdefaults` | ComputerDefaults UAC | no | — | — | ComputerDefaults UAC bypass |
| `container_escape` | Container Escape | **yes** | — | — | Attempt container escape |
| `elevate` | Elevate | elev | — | — | Attempt privilege escalation |
| `elevate_printnightmare` | PrintNightmare | no | — | — | Elevate via PrintNightmare |
| `etw_ntrace_bypass` | ETWNtrace Bypass | no | — | — |  |
| `eventvwr` | EventVwr UAC | no | — | — | Event Viewer UAC bypass |
| `execute_assembly_forkrun` | Exec Assembly FR | no | — | — |  |
| `fodhelper` | Fodhelper | no | — | — | Fodhelper UAC bypass |
| `juicy_potato` | Juicy Potato | no | — | — | Juicy Potato privilege escalation |
| `lateral_list` | Lateral List | no | — | — |  |
| `lateral_scf` | Lateral SCF | no | — | — |  |
| `list_inject_methods` | List Inject Meth | no | — | — |  |
| `named_pipe_impersonate` | Named Pipe Impersonation | no | — | — | Impersonate via named pipe |
| `net_enum_hosts` | Net Enum Hosts | no | — | — |  |
| `net_scan_smb` | Net Scan SMB | no | — | — |  |
| `privesc_check` | PrivEsc Check | no | — | — | Recon check for escalation vectors — not a guaranteed elevation |
| `rev2self` | Rev2 Self | no | — | — |  |
| `screen_stream_start` | Screen Stream St | no | — | — |  |
| `screen_stream_stop` | Screen Stream Sp | no | — | — |  |
| `screen_webrtc_start` | WebRTC Screen Stream | no | — | data:string | Start a real-time H.264 WebRTC remote-desktop stream (Pion + TURN). The server returns a short-lived operator signaling URL; video is sent peer-to-peer or through TURN. Requires the optional webrtc agent module and ffmpeg on the target. |
| `screen_webrtc_stop` | WebRTC Screen Stop | no | — | — | Stop the active WebRTC screen stream and release the capture/encoder pipeline. |
| `slui` | Slui UAC | no | — | — | Slui UAC bypass |
| `token_list_procs` | Token List Procs | no | — | — | List processes for token theft |
| `token_make` | Token Make | no | — | command:string* | Create access token for user |
| `token_revert` | Token Revert | no | — | — | Revert to original token |
| `token_steal` | Token Steal | no | — | command:int* | Steal access token from process |
| `uac_bypass` | UAC Bypass | no | — | — | Bypass UAC |

---

Regenerate after editing `pkg/protocol/taskspec_data.go` or the
`dangerousTaskTypes` map in `internal/server/tasktypes.go`.

```bash
node scripts/gen-command-reference.mjs
node scripts/gen-command-reference.mjs --check  # CI freshness gate
```
