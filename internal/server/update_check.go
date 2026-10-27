package server

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// defaultUpdateCheckURLFmt is the GitHub releases API format for ForgeC2
const defaultUpdateCheckURLFmt = "https://api.github.com/repos/%s/releases/latest"
const defaultReleaseAssetsURLFmt = "https://api.github.com/repos/%s/releases/tags/%s"
const defaultReleaseRepoURLFmt = "https://api.github.com/repos/%s"

// Hot-update stages for progress reporting.
const (
	updateStageIdle         = "idle"
	updateStageChecking     = "checking"
	updateStageDownloading  = "downloading"
	updateStageVerifying    = "verifying"
	updateStageSnapshotting = "snapshotting"
	updateStageRestarting   = "restarting"
	updateStageFailed       = "failed"
	updateStageDone         = "done"
)

// updateCheckState holds the latest version check result plus the
// hot-update progress machine (all fields under mu).
type updateCheckState struct {
	mu            sync.RWMutex
	LatestVersion string
	CheckedAt     time.Time
	Available     bool
	Error         string
	// CheckOK records whether the last check completed. A failed check must
	// never read as "you are up to date": the previous implementation derived
	// that verdict from Available alone, so a network error or a wrong
	// repository silently hid every available update.
	CheckOK bool
	// InFlight marks a running hot update. A second request while one is in
	// flight is refused instead of starting a second download and a second
	// restarter against the same binary.
	InFlight bool
	// Hot-update progress machine (idle until POST /hot-update).
	Stage           string
	Progress        int
	DownloadedBytes int64
	TotalBytes      int64
	TargetVersion   string
	LastError       string
	UpdatedAt       time.Time
}

// setUpdateProgress records a progress step and broadcasts it. Broadcasts
// are throttled to 5% increments (plus terminal stages) to avoid WS spam.
func (s *Server) setUpdateProgress(stage string, progress int, downloaded, total int64, target, lastErr string) {
	s.updateState.mu.Lock()
	prevStage := s.updateState.Stage
	prevProgress := s.updateState.Progress
	s.updateState.Stage = stage
	s.updateState.Progress = progress
	s.updateState.DownloadedBytes = downloaded
	s.updateState.TotalBytes = total
	if target != "" {
		s.updateState.TargetVersion = target
	}
	s.updateState.LastError = lastErr
	s.updateState.UpdatedAt = time.Now()
	version := s.updateState.TargetVersion
	s.updateState.mu.Unlock()

	terminal := stage == updateStageFailed || stage == updateStageDone || stage == updateStageRestarting
	if !terminal && stage == prevStage && progress-prevProgress < 5 {
		return
	}
	payload := map[string]interface{}{
		"type":       "update_progress",
		"stage":      stage,
		"percent":    progress,
		"downloaded": downloaded,
		"total":      total,
		"version":    version,
	}
	if lastErr != "" {
		payload["error"] = lastErr
	}
	msg, err := json.Marshal(payload)
	if err != nil {
		return
	}
	s.broadcastToClients(msg)
}

// GitHubRelease is a minimal representation of a GitHub release
type GitHubRelease struct {
	TagName    string `json:"tag_name"`
	HTMLURL    string `json:"html_url"`
	Prerelease bool   `json:"prerelease"`
}

// initUpdateChecker starts the background version check goroutine
func (s *Server) initUpdateChecker() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("Update checker panicked", "recover", r)
			}
		}()
		initialDelay := time.NewTimer(UpdateCheckInitialDelay)
		select {
		case <-initialDelay.C:
			s.checkForUpdate()
		case <-s.ctx.Done():
			initialDelay.Stop()
			return
		}
		for {
			// Jitter the interval so a fleet of servers that booted together
			// does not hit api.github.com in lockstep.
			wait := UpdateCheckInterval + time.Duration(time.Now().UnixNano()%int64(UpdateCheckJitter))
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
				s.checkForUpdate()
			case <-s.ctx.Done():
				timer.Stop()
				return
			}
		}
	}()
}

// checkForUpdate fetches the latest version and broadcasts if newer
func (s *Server) checkForUpdate() {
	s.configMu.RLock()
	repo := s.cfg.Server.UpdateCheckRepo
	s.configMu.RUnlock()
	latest, err := fetchLatestVersion(repo)
	s.updateState.mu.Lock()
	s.updateState.CheckedAt = time.Now()
	if err != nil {
		s.updateState.Error = err.Error()
		s.updateState.Available = false
		s.updateState.CheckOK = false
		slog.Debug("Update check failed", "err", err)
		s.updateState.mu.Unlock()
		return
	}
	s.updateState.LatestVersion = latest
	s.updateState.Error = ""
	s.updateState.CheckOK = true
	s.updateState.mu.Unlock()

	if latest == "" {
		return
	}

	if compareVersions(latest, ServerVersion) > 0 {
		slog.Info("New version available", "current", ServerVersion, "latest", latest)
		s.updateState.mu.Lock()
		s.updateState.Available = true
		s.updateState.mu.Unlock()
		s.broadcastUpdateNotification(latest)
	} else {
		s.updateState.mu.Lock()
		s.updateState.Available = false
		s.updateState.mu.Unlock()
	}
}

// fetchLatestVersion calls the GitHub API and returns the latest tag.
//
// A 404 from the releases endpoint is ambiguous: it means either "this
// repository has no releases yet" or "this repository does not exist". The
// second case used to be reported as "no new release", which is how a
// mistyped repository silently disabled every update check. The repository
// endpoint disambiguates the two.
func fetchLatestVersion(repo string) (string, error) {
	url := fmt.Sprintf(updateCheckURLFmt, repo)
	client := ssrfSafeClient(&http.Client{Timeout: GitHubAPITimeout})

	backoff := []time.Duration{time.Second, 3 * time.Second}
	var lastErr error
	for attempt := 0; attempt <= 2; attempt++ {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return "", fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "ForgeC2/"+ServerVersion)

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("github api: %w", err)
			if attempt < 2 {
				time.Sleep(backoff[attempt])
				continue
			}
			return "", lastErr
		}

		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		if err != nil {
			return "", fmt.Errorf("read response: %w", err)
		}

		if resp.StatusCode == http.StatusNotFound {
			// The releases endpoint answers 404 both for "no releases yet"
			// and for "no such repository". Probe the repository so a
			// mistyped update_check_repo is reported instead of silently
			// reading as "you are up to date".
			exists, probeErr := releaseRepoExists(repo)
			if probeErr != nil {
				return "", probeErr
			}
			if !exists {
				return "", fmt.Errorf("github api: repository %q does not exist (check server.update_check_repo)", repo)
			}
			return "", nil
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("github api status %d", resp.StatusCode)
			if attempt < 2 && resp.StatusCode >= 500 {
				time.Sleep(backoff[attempt])
				continue
			}
			return "", lastErr
		}

		var release GitHubRelease
		if err := json.Unmarshal(body, &release); err != nil {
			return "", fmt.Errorf("parse release: %w", err)
		}
		if release.TagName == "" {
			return "", fmt.Errorf("empty tag_name in response")
		}
		return release.TagName, nil
	}
	return "", lastErr
}

// versionParts splits a release tag into its numeric segments. ok is false when
// any segment is not a plain non-negative integer — a prerelease suffix
// ("2.7.0-rc1"), stray whitespace, or a "dev" build whose version stamp was
// never injected. The ordering of such a version against a release is not
// knowable, and guessing it is what used to let an unversioned build accept a
// downgrade.
func versionParts(v string) ([]int, bool) {
	fields := strings.Split(strings.TrimPrefix(v, "v"), ".")
	parts := make([]int, 0, len(fields))
	for _, field := range fields {
		n, err := strconv.Atoi(field)
		if err != nil || n < 0 {
			return nil, false
		}
		parts = append(parts, n)
	}
	return parts, true
}

// compareVersions compares two semver strings.
// Returns: 1 if v1 > v2, -1 if v1 < v2, 0 if equal or if either side is not
// a comparable release version (fail closed: callers must not derive an
// upgrade or a downgrade from a version they cannot read).
func compareVersions(v1, v2 string) int {
	parts1, ok1 := versionParts(v1)
	parts2, ok2 := versionParts(v2)
	if !ok1 || !ok2 {
		return 0
	}

	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}

	for i := 0; i < maxLen; i++ {
		n1, n2 := 0, 0
		if i < len(parts1) {
			n1 = parts1[i]
		}
		if i < len(parts2) {
			n2 = parts2[i]
		}
		if n1 > n2 {
			return 1
		}
		if n1 < n2 {
			return -1
		}
	}
	return 0
}

// GitHubAsset represents a file in a GitHub release
type GitHubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// GitHubReleaseFull is a full release with assets
type GitHubReleaseFull struct {
	TagName string        `json:"tag_name"`
	HTMLURL string        `json:"html_url"`
	Assets  []GitHubAsset `json:"assets"`
}

// handleCheckVersion returns a JSON with current vs latest for the UI to display
func (s *Server) handleCheckVersion(c *gin.Context) {
	s.updateState.mu.RLock()
	latest := s.updateState.LatestVersion
	available := s.updateState.Available
	checkedAt := s.updateState.CheckedAt
	errStr := s.updateState.Error
	checkOK := s.updateState.CheckOK
	s.updateState.mu.RUnlock()

	// "is_latest" may only be claimed when a check actually ran and failed to
	// find anything newer. Without the CheckOK guard a failed check (wrong
	// repository, rate limit, no network) reported the server as up to date
	// and every available update stayed invisible.
	isLatest := false
	switch {
	case !checkOK:
		isLatest = false
	case latest == "":
		isLatest = true
	default:
		isLatest = compareVersions(ServerVersion, latest) >= 0
	}

	resp := gin.H{
		"current_version": ServerVersion,
		"is_latest":       isLatest,
		"checked_at":      checkedAt,
		"check_ok":        checkOK,
		"check_state":     updateCheckStateName(checkOK, errStr),
	}
	if latest != "" {
		resp["latest_version"] = latest
	}
	if available {
		resp["download_url"] = s.releaseURL(latest)
	}
	if errStr != "" {
		resp["check_error"] = errStr
	}
	respond(c, resp)
}

// updateCheckStateName describes the state of the last version check.
func updateCheckStateName(checkOK bool, errStr string) string {
	switch {
	case checkOK:
		return "ok"
	case errStr != "":
		return "error"
	default:
		return "unknown"
	}
}

// handleHotUpdate triggers a self-update: downloads the latest release binary and restarts
func (s *Server) handleHotUpdate(c *gin.Context) {
	s.updateState.mu.Lock()
	if s.updateState.InFlight {
		s.updateState.mu.Unlock()
		respondError(c, http.StatusConflict, "A hot update is already running")
		return
	}
	latest := s.updateState.LatestVersion
	available := s.updateState.Available
	s.updateState.InFlight = true
	s.updateState.mu.Unlock()

	if !available || latest == "" {
		s.clearUpdateInFlight()
		respondError(c, http.StatusBadRequest, "No updates available")
		return
	}

	if compareVersions(latest, ServerVersion) <= 0 {
		s.clearUpdateInFlight()
		respondError(c, http.StatusBadRequest, "Already up to date")
		return
	}

	s.LogAuditRecord(c, "hot_update", "server", "self", fmt.Sprintf("hot update to %s triggered", latest), true, nil)

	// Trigger async hot-update; progress follows via WS update_progress
	// and GET /api/update-progress (the process restarts on success).
	s.setUpdateProgress(updateStageDownloading, 0, 0, 0, latest, "")
	// The update goroutine is tracked by updateWg and not by wg: it ends by
	// shutting this process down, and a worker that waits for the waitgroup
	// it belongs to can never be released.
	s.updateWg.Add(1)
	go func() {
		defer s.updateWg.Done()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("recovered from panic", "err", r, "stack", string(debug.Stack()))
			}
		}()
		if err := s.performHotUpdate(latest); err != nil {
			slog.Error("Hot update failed", "err", err)
		}
	}()

	c.JSON(http.StatusAccepted, gin.H{
		"success":        true,
		"message":        "Downloading and applying update, server will restart...",
		"latest_version": latest,
		"progress_url":   "/api/update-progress",
	})
}

// clearUpdateInFlight releases the hot-update guard so a failed attempt can
// be retried.
func (s *Server) clearUpdateInFlight() {
	s.updateState.mu.Lock()
	s.updateState.InFlight = false
	s.updateState.mu.Unlock()
}

// handleUpdateProgress returns the hot-update state machine for polling
// fallback (WS disconnects, page refreshes mid-download).
func (s *Server) handleUpdateProgress(c *gin.Context) {
	s.updateState.mu.RLock()
	stage := s.updateState.Stage
	percent := s.updateState.Progress
	downloaded := s.updateState.DownloadedBytes
	total := s.updateState.TotalBytes
	version := s.updateState.TargetVersion
	lastErr := s.updateState.LastError
	updatedAt := s.updateState.UpdatedAt
	s.updateState.mu.RUnlock()

	resp := gin.H{
		"stage":      stage,
		"percent":    percent,
		"downloaded": downloaded,
		"total":      total,
		"version":    version,
		"error":      lastErr,
		"updated_at": updatedAt,
	}
	if last := s.lastUpdateResult(); last != nil {
		resp["last_result"] = last
	}
	respond(c, resp)
}

// lastUpdateResult returns the outcome of the last update attempt, if the
// restarter recorded one.
func (s *Server) lastUpdateResult() *updateLastResult {
	exePath, err := os.Executable()
	if err != nil {
		return nil
	}
	if exePath, err = filepath.Abs(exePath); err != nil {
		return nil
	}
	res, err := readUpdateLast(filepath.Join(updateStateDir(exePath), updateLastFile))
	if err != nil {
		return nil
	}
	return res
}

// performHotUpdate downloads the latest binary, replaces itself, and restarts.
// Progress is reported through setUpdateProgress (WS + GET /api/update-progress).
//
// The swap itself is performed by a generated restarter (see update_restart.go):
// a running process cannot replace its own image on Windows, and the new
// build must be able to report back whether it came up. The restarter waits
// for this process to exit, installs the new binary, starts it with the
// original command line and rolls back when the new build never reports a
// healthy boot.
func (s *Server) performHotUpdate(latest string) error {
	fail := func(err error) error {
		s.setUpdateProgress(updateStageFailed, 0, 0, 0, latest, err.Error())
		s.clearUpdateInFlight()
		return err
	}
	s.setUpdateProgress(updateStageDownloading, 0, 0, 0, latest, "")

	assets, err := fetchReleaseAssets(s.cfg.Server.UpdateCheckRepo, latest)
	if err != nil {
		return fail(fmt.Errorf("fetch release assets: %w", err))
	}

	// Pick the binary that matches THIS platform. Matching on the asset name
	// is the only safe way: the release carries one binary per platform plus
	// bundles, SBOMs and checksum files, and the wrong pick is a binary that
	// cannot even start.
	binaryAsset, err := selectBinaryAsset(assets, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return fail(err)
	}
	// The checksum must belong to that binary, not to whichever ".sha256"
	// happens to be listed first.
	checksumAsset, ok := findAssetByName(assets, binaryAsset.Name+".sha256")
	if !ok {
		return fail(fmt.Errorf("release has no checksum file for %s", binaryAsset.Name))
	}
	slog.Info("Hot update: selected release asset", "asset", binaryAsset.Name, "platform", runtime.GOOS+"/"+runtime.GOARCH)

	exePath, err := os.Executable()
	if err != nil {
		return fail(fmt.Errorf("get executable path: %w", err))
	}
	exePath, err = filepath.Abs(exePath)
	if err != nil {
		return fail(fmt.Errorf("resolve absolute path: %w", err))
	}
	updateDir := updateStateDir(exePath)
	if err := os.MkdirAll(updateDir, 0o700); err != nil {
		return fail(fmt.Errorf("create update directory: %w", err))
	}
	if err := ensureDirWritable(updateDir); err != nil {
		return fail(err)
	}

	// A second server instance sharing this directory must not swap the
	// binary underneath the first one.
	releaseLock, err := acquireUpdateLock(updateDir)
	if err != nil {
		return fail(err)
	}
	defer releaseLock()

	newPath := filepath.Join(updateDir, updateNewFile)
	partPath := newPath + ".part"
	os.Remove(partPath)
	os.Remove(newPath)

	// Download to a staging file first: a half-written binary must never be
	// mistaken for a complete one.
	if err := s.downloadUpdateFile(binaryAsset.BrowserDownloadURL, partPath, latest); err != nil {
		os.Remove(partPath)
		return fail(fmt.Errorf("download binary: %w", err))
	}
	if fi, statErr := os.Stat(partPath); statErr != nil || fi.Size() == 0 {
		os.Remove(partPath)
		return fail(fmt.Errorf("downloaded binary is invalid"))
	}
	if err := os.Rename(partPath, newPath); err != nil {
		os.Remove(partPath)
		return fail(fmt.Errorf("stage downloaded binary: %w", err))
	}

	// Verify the downloaded file before it can replace the running binary.
	s.setUpdateProgress(updateStageVerifying, 100, 0, 0, latest, "")
	checksumData, _, err := fetchChecksumFile(checksumAsset.BrowserDownloadURL, binaryAsset.Name)
	if err != nil {
		os.Remove(newPath)
		return fail(fmt.Errorf("fetch checksum file: %w", err))
	}
	// Release signature (Ed25519 over the exact checksum-file bytes) when
	// crypto.update_signing_key is configured. Without a key: refuse when
	// crypto.require_release_signature is set (fail closed), otherwise warn
	// and fall back to same-origin checksums only (upgrade compatibility).
	s.configMu.RLock()
	signingKey := s.cfg.Crypto.UpdateSigningKey
	requireSig := s.cfg.Crypto.RequireReleaseSignature
	s.configMu.RUnlock()
	enforceSig, policyErr := updateSignatureEnforced(signingKey, requireSig)
	if policyErr != nil {
		os.Remove(newPath)
		return fail(policyErr)
	}
	if enforceSig {
		sig, err := fetchReleaseSignature(checksumAsset.BrowserDownloadURL)
		if err != nil {
			os.Remove(newPath)
			return fail(fmt.Errorf("release signature unavailable: %w", err))
		}
		if err := verifyReleaseSignature(checksumData, string(sig), signingKey); err != nil {
			os.Remove(newPath)
			return fail(fmt.Errorf("release signature rejected: %w", err))
		}
		slog.Info("Hot update: release signature verified")
	} else {
		slog.Warn("Hot update without release signature: set crypto.update_signing_key (and crypto.require_release_signature=true) to require signed releases")
	}
	if err := verifyChecksumBytes(newPath, checksumData, binaryAsset.Name); err != nil {
		os.Remove(newPath)
		return fail(fmt.Errorf("checksum verification failed: %w", err))
	}
	slog.Info("Hot update: checksum verified", "asset", binaryAsset.Name)

	// Snapshot the database so a rollback can also undo a schema migration.
	dbPath, dbBackup := s.snapshotDatabaseForUpdate(updateDir)
	if dbBackup != "" {
		s.setUpdateProgress(updateStageSnapshotting, 100, 0, 0, latest, "")
	}

	workDir, err := os.Getwd()
	if err != nil {
		workDir = filepath.Dir(exePath)
	}

	prevPath := filepath.Join(updateDir, updatePrevFile)
	pendingPath := filepath.Join(updateDir, updatePendingFile)
	okPath := filepath.Join(updateDir, updateOKFile)
	lastPath := filepath.Join(updateDir, updateLastFile)
	lockPath := filepath.Join(updateDir, updateLockFile)

	scriptName := updateScriptUnix
	if runtime.GOOS == "windows" {
		scriptName = updateScriptWin
	}
	scriptPath := filepath.Join(updateDir, scriptName)

	pending := &updatePending{
		From:      ServerVersion,
		To:        latest,
		PID:       os.Getpid(),
		StartedAt: time.Now(),
		Exe:       exePath,
		New:       newPath,
		Prev:      prevPath,
		WorkDir:   workDir,
		Args:      os.Args[1:],
		DBPath:    dbPath,
		DBBackup:  dbBackup,
		Script:    scriptPath,
	}
	if err := writeUpdatePending(pendingPath, pending); err != nil {
		os.Remove(newPath)
		return fail(err)
	}

	plan := restartPlan{
		Exe:      exePath,
		New:      newPath,
		Prev:     prevPath,
		Pending:  pendingPath,
		OK:       okPath,
		Last:     lastPath,
		Lock:     lockPath,
		WorkDir:  workDir,
		Args:     os.Args[1:],
		OldPID:   os.Getpid(),
		Target:   latest,
		Script:   scriptPath,
		DBPath:   dbPath,
		DBBackup: dbBackup,
	}
	script, err := renderRestartScript(runtime.GOOS, plan)
	if err != nil {
		os.Remove(newPath)
		os.Remove(pendingPath)
		return fail(err)
	}
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		os.Remove(newPath)
		os.Remove(pendingPath)
		return fail(fmt.Errorf("create restart script: %w", err))
	}

	payload := map[string]interface{}{
		"type":    "server_restarting",
		"message": "Server is hot-reloading and restarting...",
		"version": latest,
	}
	if msg, ok := marshalJSONSafe(payload); ok {
		s.broadcastToClients(msg)
	}

	slog.Info("Hot update: restarting into the new build", "version", latest, "script", scriptPath)
	s.setUpdateProgress(updateStageRestarting, 100, 0, 0, latest, "")

	cmd, err := restartCommand(scriptPath)
	if err != nil {
		os.Remove(newPath)
		os.Remove(pendingPath)
		return fail(err)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	detachCommand(cmd)
	if err := cmd.Start(); err != nil {
		os.Remove(newPath)
		os.Remove(pendingPath)
		return fail(fmt.Errorf("start restart script: %w", err))
	}

	// Replace this process. The restarter takes over as soon as the old
	// process is gone: it waits for this PID, installs the new binary and
	// starts it with the original command line.
	s.fastStop.Store(true)
	s.Shutdown()
	os.Exit(0)
	return nil
}

// ensureDirWritable proves the update directory accepts writes before the
// download starts: a read-only install directory must fail early, not after
// a full download.
func ensureDirWritable(dir string) error {
	probe, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return fmt.Errorf("update directory %s is not writable: %w", dir, err)
	}
	name := probe.Name()
	probe.Close()
	os.Remove(name)
	return nil
}

// acquireUpdateLock takes the cross-process update lock. A lock left behind
// by a crashed run is reclaimed once it is older than the staleness window.
func acquireUpdateLock(dir string) (func(), error) {
	lockPath := filepath.Join(dir, updateLockFile)
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "pid=%d\nat=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
			f.Close()
			return func() { os.Remove(lockPath) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("create update lock: %w", err)
		}
		fi, statErr := os.Stat(lockPath)
		if statErr != nil {
			continue // vanished between the two calls: retry
		}
		if time.Since(fi.ModTime()) > updateLockStaleAfter {
			os.Remove(lockPath)
			continue
		}
		return nil, fmt.Errorf("another hot update is already running (lock: %s)", lockPath)
	}
	return nil, fmt.Errorf("could not acquire the update lock: %s", lockPath)
}

// snapshotDatabaseForUpdate copies the SQLite database next to the update
// markers so a rollback can also undo a schema migration. It returns the
// database path and the snapshot path; an empty snapshot means the database
// was not snapshotted (unsupported driver or a failed copy).
func (s *Server) snapshotDatabaseForUpdate(dir string) (string, string) {
	if s.cfg == nil || s.db == nil {
		return "", ""
	}
	s.configMu.RLock()
	driver := strings.ToLower(strings.TrimSpace(s.cfg.Database.Driver))
	dbPath := strings.TrimSpace(s.cfg.Database.Path)
	s.configMu.RUnlock()
	if driver != "" && driver != "sqlite" {
		return "", ""
	}
	if dbPath == "" {
		return "", ""
	}
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return "", ""
	}
	backup := filepath.Join(dir, "prev.db")
	os.Remove(backup)
	if err := s.db.Exec("VACUUM INTO ?", backup).Error; err != nil {
		slog.Warn("Hot update: database snapshot failed; a rollback keeps the current database", "err", err)
		os.Remove(backup)
		return "", ""
	}
	slog.Info("Hot update: database snapshot taken", "path", backup)
	return abs, backup
}

// quoteCmd wraps a path in double quotes with cmd.exe escaping (`"` -> `""`).
func quoteCmd(p string) string {
	return `"` + strings.ReplaceAll(p, `"`, `""`) + `"`
}

// quoteSh wraps a path in single quotes with POSIX escaping (`'` -> `'\”`).
func quoteSh(p string) string {
	return "'" + strings.ReplaceAll(p, `'`, `'\''`) + "'"
}

// releaseAssetsURLFmt builds the GitHub release-assets URL. It is a var, not a
// const, so tests can point it at a local httptest server; the hot-update path
// is otherwise a live network call that made the suite both slow and flaky.
var releaseAssetsURLFmt = defaultReleaseAssetsURLFmt

// updateCheckURLFmt and releaseRepoURLFmt are vars for the same reason: the
// version check and the repository probe must be testable without touching
// api.github.com.
var (
	updateCheckURLFmt = defaultUpdateCheckURLFmt
	releaseRepoURLFmt = defaultReleaseRepoURLFmt
)

// releaseRepoExists reports whether the configured repository is reachable.
// It is only consulted when the releases endpoint answers 404, so it costs
// one request per failed check and never runs on the happy path.
func releaseRepoExists(repo string) (bool, error) {
	url := fmt.Sprintf(releaseRepoURLFmt, repo)
	client := ssrfSafeClient(&http.Client{Timeout: GitHubAPITimeout})
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false, fmt.Errorf("create repository request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ForgeC2/"+ServerVersion)
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("github api: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4*1024))
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("github api status %d while probing repository %q", resp.StatusCode, repo)
	}
}

// fetchReleaseAssets gets the asset list for a given tag
func fetchReleaseAssets(repo, tag string) ([]GitHubAsset, error) {
	url := fmt.Sprintf(releaseAssetsURLFmt, repo, tag)
	client := ssrfSafeClient(&http.Client{Timeout: GitHubAPITimeout})

	backoff := []time.Duration{time.Second, 3 * time.Second}
	var lastErr error
	for attempt := 0; attempt <= 2; attempt++ {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "ForgeC2/"+ServerVersion)

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("github api: %w", err)
			if attempt < 2 {
				time.Sleep(backoff[attempt])
				continue
			}
			return nil, lastErr
		}

		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
		if err != nil {
			return nil, fmt.Errorf("read response: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("github api status %d", resp.StatusCode)
			if attempt < 2 && resp.StatusCode >= 500 {
				time.Sleep(backoff[attempt])
				continue
			}
			return nil, lastErr
		}

		var release GitHubReleaseFull
		if err := json.Unmarshal(body, &release); err != nil {
			return nil, fmt.Errorf("parse release: %w", err)
		}
		return release.Assets, nil
	}
	return nil, lastErr
}

// progressWriter reports download progress for hot-update broadcasting.
type progressWriter struct {
	total      int64
	downloaded int64
	onProgress func(downloaded, total int64)
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.downloaded += int64(len(p))
	if w.onProgress != nil {
		w.onProgress(w.downloaded, w.total)
	}
	return len(p), nil
}

// downloadUpdateFile downloads with progress callbacks into setUpdateProgress.
func (s *Server) downloadUpdateFile(url, dest, version string) error {
	slog.Info("Downloading update binary", "url", url, "dest", dest)
	client := ssrfSafeClient(&http.Client{Timeout: UpdateDownloadTimeout})
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ForgeC2/"+ServerVersion)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	pw := &progressWriter{total: resp.ContentLength}
	lastBroadcast := -1
	pw.onProgress = func(downloaded, total int64) {
		percent := -1
		if total > 0 {
			percent = int(downloaded * 100 / total)
			if percent == lastBroadcast || (percent-lastBroadcast < 5 && percent < 100) {
				return
			}
			lastBroadcast = percent
		}
		s.setUpdateProgress(updateStageDownloading, percent, downloaded, total, version, "")
	}
	written, err := io.Copy(out, io.TeeReader(resp.Body, pw))
	if err != nil {
		os.Remove(dest)
		return err
	}
	slog.Info("Downloaded new binary", "size", written)
	s.setUpdateProgress(updateStageDownloading, 100, written, written, version, "")
	return nil
}

// releaseURL returns the GitHub release page URL for a given version
func (s *Server) releaseURL(version string) string {
	s.configMu.RLock()
	repo := s.cfg.Server.UpdateCheckRepo
	s.configMu.RUnlock()
	return fmt.Sprintf("https://github.com/%s/releases/tag/%s", repo, version)
}

// broadcastUpdateNotification sends a new-version alert via WebSocket
func (s *Server) broadcastUpdateNotification(latest string) {
	payload := map[string]interface{}{
		"type":         "update_available",
		"current":      ServerVersion,
		"latest":       latest,
		"download_url": s.releaseURL(latest),
	}
	msg, err := json.Marshal(payload)
	if err != nil {
		slog.Error("Failed to marshal update notification", "err", err)
		return
	}
	s.broadcastToClients(msg)
}

// handleRefreshUpdateCheck forces a fresh update check and returns the result
func (s *Server) handleRefreshUpdateCheck(c *gin.Context) {
	s.checkForUpdate()
	respond(c, s.updateCheckResponse())
}

// handleUpdateCheck returns the current update check status as JSON
func (s *Server) handleUpdateCheck(c *gin.Context) {
	respond(c, s.updateCheckResponse())
}

// updateCheckResponse builds the status payload the update-check endpoints
// share.
func (s *Server) updateCheckResponse() gin.H {
	s.updateState.mu.RLock()
	defer s.updateState.mu.RUnlock()

	resp := gin.H{
		"current_version":  ServerVersion,
		"checked_at":       s.updateState.CheckedAt,
		"update_available": s.updateState.Available,
		"check_ok":         s.updateState.CheckOK,
		"check_state":      updateCheckStateName(s.updateState.CheckOK, s.updateState.Error),
	}
	if s.updateState.Available {
		resp["latest_version"] = s.updateState.LatestVersion
		resp["download_url"] = s.releaseURL(s.updateState.LatestVersion)
	}
	if s.updateState.Error != "" {
		resp["check_error"] = s.updateState.Error
	}
	return resp
}

// fetchChecksumFile downloads the checksum file (capped) and derives the
// expected asset filename from the downloaded binary path.
func fetchChecksumFile(checksumURL, binaryPath string) ([]byte, string, error) {
	client := ssrfSafeClient(&http.Client{Timeout: ChecksumDownloadTimeout})
	req, err := http.NewRequest("GET", checksumURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("create checksum request: %w", err)
	}
	req.Header.Set("User-Agent", "ForgeC2/"+ServerVersion)
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download checksum: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download checksum: status %d", resp.StatusCode)
	}
	checksumData, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return nil, "", fmt.Errorf("read checksum: %w", err)
	}
	return checksumData, filepath.Base(binaryPath), nil
}

// verifyChecksumBytes checks the binary against the checksum file bytes.
// Multi-line sha256sum files are matched by FILENAME (first-line-only
// matching verified against the wrong asset when a release lists several);
// a single bare hash retains the legacy accept path.
func verifyChecksumBytes(binaryPath string, checksumData []byte, binName string) error {

	// Compute hash of the downloaded binary
	f, err := os.Open(binaryPath)
	if err != nil {
		return fmt.Errorf("open binary: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash binary: %w", err)
	}
	actualHash := hex.EncodeToString(h.Sum(nil))

	// Parse expected hash: prefer the line naming our asset (sha256sum
	// "<hash>  <filename>" format); a lone bare hash keeps working.
	expectedHash, err := matchChecksumLine(string(checksumData), binName)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actualHash, expectedHash) {
		return fmt.Errorf("hash mismatch: expected %s, got %s", expectedHash, actualHash)
	}
	return nil
}

// matchChecksumLine extracts the expected hex digest for binName from
// sha256sum-style content. Lines are "<hash><spaces><[* ]filename>"; a
// single bare hash (no filename) is accepted as the legacy format.
func matchChecksumLine(content, binName string) (string, error) {
	var fallback string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 1 {
			if fallback == "" {
				fallback = fields[0]
			}
			continue
		}
		name := strings.TrimLeft(fields[1], "*")
		if name == binName || filepath.Base(name) == filepath.Base(binName) {
			return fields[0], nil
		}
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", fmt.Errorf("checksum file has no entry for %s", binName)
}

// verifyReleaseSignature checks the detached Ed25519 signature (hex) over
// the exact checksum-file bytes against the configured release signing key
// (crypto.update_signing_key, 64 hex chars = 32-byte pubkey).
func verifyReleaseSignature(checksumData []byte, sigHex, pubHex string) error {
	sig, err := hex.DecodeString(strings.TrimSpace(sigHex))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("malformed release signature (want %d-byte hex)", ed25519.SignatureSize)
	}
	pub, err := hex.DecodeString(strings.TrimSpace(pubHex))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("malformed update signing key (want %d-byte hex)", ed25519.PublicKeySize)
	}
	if !ed25519.Verify(pub, checksumData, sig) {
		return fmt.Errorf("release signature verification failed")
	}
	return nil
}

// fetchReleaseSignature downloads the detached signature asset. Convention:
// "<checksumURL>.sig" (release pipeline signs each "<bin>.sha256" file).
func fetchReleaseSignature(checksumURL string) ([]byte, error) {
	client := ssrfSafeClient(&http.Client{Timeout: ChecksumDownloadTimeout})
	req, err := http.NewRequest("GET", checksumURL+".sig", nil)
	if err != nil {
		return nil, fmt.Errorf("create signature request: %w", err)
	}
	req.Header.Set("User-Agent", "ForgeC2/"+ServerVersion)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download signature: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("signature asset missing (status %d)", resp.StatusCode)
	}
	sig, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		return nil, fmt.Errorf("read signature: %w", err)
	}
	return sig, nil
}
