package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/forgec2/forgec2/pkg/protocol"
	"github.com/gin-gonic/gin"
)

// BuildJob tracks an asynchronous binary build.
type BuildJob struct {
	ID     string
	Status string // "building", "completed", "failed"
	Phase  string // "queued", "building", "completed", "failed"
	Output string // file path on success
	Error  string // error message on failure
	// ErrorHint is the machine-readable next step for the same failure, so a client can offer an action without parsing the message.
	ErrorHint   BuildFailureHint
	Platform    string // "windows", "linux", "macos"
	Format      string // "exe", "dll", "elf", "binary"
	C2URL       string
	ListenerID  uint
	Filename    string
	ProfileName string
	ProfileHash string
	CreatedAt   time.Time
	StartedAt   time.Time
	CompletedAt time.Time

	// Authenticode classification for a direct Windows EXE/DLL output. Empty
	// when the build is not a direct Windows binary (ZIP/LNK bundles and other
	// platforms are deliberately not inspected).
	// TenantID scopes the job to the tenant that started it: the status and
	// download endpoints looked jobs up by id alone, so an id that leaked into a
	// log or a screenshot let another tenant fetch the binary (which embeds the
	// listener URL and profile).
	TenantID        uint
	SignatureStatus string
	SignatureSigner string
	SignatureIssuer string
	SignatureError  string
}

// startBuildJob creates a build job, stores it, and returns its ID.
func (s *Server) startBuildJob(platform, format, c2URL string, listenerID uint, filename string) *BuildJob {
	return s.startBuildJobWithProfile(platform, format, c2URL, listenerID, filename, "", "", 0)
}

// startBuildJobWithProfile additionally records the malleable profile for audit.
func (s *Server) startBuildJobWithProfile(platform, format, c2URL string, listenerID uint, filename, profileName, profileHash string, tenantID uint) *BuildJob {
	job := &BuildJob{
		ID:          protocol.UUIDv7(),
		TenantID:    tenantID,
		Status:      "building",
		Phase:       "queued",
		Platform:    platform,
		Format:      format,
		C2URL:       c2URL,
		ListenerID:  listenerID,
		Filename:    filename,
		ProfileName: profileName,
		ProfileHash: profileHash,
		CreatedAt:   time.Now(),
	}
	s.buildJobsMu.Lock()
	// NewServer initializes the map, but a server assembled another way (tests,
	// partial startup) must not panic on the first build.
	if s.buildJobs == nil {
		s.buildJobs = make(map[string]*BuildJob)
	}
	s.buildJobs[job.ID] = job
	s.buildJobsMu.Unlock()
	return job
}

// completeBuildJob marks a build job as completed or failed. Signature
// inspection runs before taking the jobs lock because it reads the artifact;
// only the small result is copied into the job.
func (s *Server) completeBuildJob(job *BuildJob, outputPath string, err error) {
	var sig buildSignatureInfo
	var haveSig bool
	if err == nil {
		sig, haveSig = inspectBuildSignature(job.Platform, job.Format, outputPath)
	}
	s.buildJobsMu.Lock()
	defer s.buildJobsMu.Unlock()
	job.CompletedAt = time.Now()
	if err != nil {
		job.Status = "failed"
		job.Phase = "failed"
		// NOT sanitizeError: that helper replaces an unrecognised error with a fallback sentence, and a build error IS the diagnosis.
		// The old call turned a compiler failure into the literal text "Build failed", which the UI then showed beneath its own
		// "Build failed" title - a report that names nothing and cannot be acted on.
		job.Error = buildFailureText(err)
		job.ErrorHint = hintOrEmpty(buildFailureHint(err))
	} else {
		job.Status = "completed"
		job.Phase = "completed"
		job.Output = outputPath
		if haveSig {
			job.SignatureStatus = sig.Status
			job.SignatureSigner = sig.Signer
			job.SignatureIssuer = sig.Issuer
			job.SignatureError = sig.Error
		}
	}
}

// abandonBuildJob removes a job that was never submitted (e.g. queue full), so
// it does not linger as a phantom failed build.
func (s *Server) abandonBuildJob(job *BuildJob) {
	s.buildJobsMu.Lock()
	delete(s.buildJobs, job.ID)
	s.buildJobsMu.Unlock()
}

// buildJobSweepInterval is how often cleanupBuildJobs sweeps. A variable so a test can
// prove the loop actually reaps without waiting fifteen minutes.
var buildJobSweepInterval = 15 * time.Minute

// cleanupBuildJobs runs for the life of the server: every buildJobSweepInterval it removes
// completed jobs older than an hour and one-shot artifacts whose expiry has passed.
func (s *Server) cleanupBuildJobs() {
	ticker := time.NewTicker(buildJobSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.sweepBuildJobsOnce()
		case <-s.ctx.Done():
			return
		}
	}
}

// sweepBuildJobsOnce runs one cleanup pass: finished build jobs older than an
// hour are removed with their output files, then expired one-shot artifacts are
// reaped. Split out of the ticker loop so a test can drive a pass without
// waiting fifteen minutes.
func (s *Server) sweepBuildJobsOnce() {
	s.buildJobsMu.Lock()
	cutoff := time.Now().Add(-1 * time.Hour)
	for id, job := range s.buildJobs {
		if job.Status != "building" && job.CompletedAt.Before(cutoff) {
			if job.Output != "" {
				if err := os.Remove(job.Output); err != nil {
					slog.Error("Build jobs: failed to remove output file", "job_id", id, "err", err)
				}
			}
			delete(s.buildJobs, id)
		}
	}
	s.buildJobsMu.Unlock()

	// Reap one-shot generated files (stager/one-liner outputs) that are not
	// tracked as BuildJobs.
	s.transientArtifactsMu.Lock()
	for p, exp := range s.transientArtifacts {
		if time.Now().After(exp) {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				slog.Error("transient artifact cleanup failed", "path", p, "err", err)
			}
			delete(s.transientArtifacts, p)
		}
	}
	s.transientArtifactsMu.Unlock()
}

// handleBuildStatus returns the current status of an async build.
// buildJobVisibleTo reports whether the caller may read this build job. A build
// belongs to the tenant that started it; an unscoped caller (tid 0) keeps the
// single-tenant view, and a job with no tenant (created by a path that predates
// this field) stays readable so existing builds do not become invisible.
func (s *Server) buildJobVisibleTo(c *gin.Context, job *BuildJob) bool {
	if job == nil {
		return false
	}
	tid := s.currentTenantID(c)
	if tid == 0 || job.TenantID == 0 {
		return true
	}
	return job.TenantID == tid
}

func (s *Server) handleBuildStatus(c *gin.Context) {
	buildID := c.Param("id")
	// Snapshot all fields under the lock: completeBuildJob mutates the job
	// from the build goroutine, so lock-free reads would race with it.
	s.buildJobsMu.RLock()
	job, ok := s.buildJobs[buildID]
	visible := s.buildJobVisibleTo(c, job)
	if !ok || !visible {
		s.buildJobsMu.RUnlock()
		// 404 rather than 403: another tenant's build must not even be confirmed
		// to exist.
		respondError(c, http.StatusNotFound, "build not found")
		return
	}
	resp := gin.H{
		"success":    true,
		"build_id":   job.ID,
		"status":     job.Status,
		"phase":      job.Phase,
		"platform":   job.Platform,
		"format":     job.Format,
		"created_at": job.CreatedAt,
	}
	if !job.StartedAt.IsZero() {
		resp["started_at"] = job.StartedAt
	}
	if job.Status == "completed" {
		resp["download_url"] = fmt.Sprintf("/generate/builds/%s/download", job.ID)
		resp["completed_at"] = job.CompletedAt
		if job.SignatureStatus != "" {
			resp["signature_status"] = job.SignatureStatus
			resp["signature_signer"] = job.SignatureSigner
			resp["signature_issuer"] = job.SignatureIssuer
			if job.SignatureError != "" {
				resp["signature_error"] = job.SignatureError
			}
		}
	} else if job.Status == "failed" {
		resp["error"] = job.Error
		// A code plus a sentence: the console can map the code to an action or a translation, and a client that does not care can ignore
		// it without the error text becoming unparseable.
		if job.ErrorHint.Code != "" {
			resp["error_hint"] = job.ErrorHint
		}
		resp["completed_at"] = job.CompletedAt
	}
	s.buildJobsMu.RUnlock()
	respond(c, resp)
}

// handleBuildDownload serves the completed build file.
func (s *Server) handleBuildDownload(c *gin.Context) {
	buildID := c.Param("id")
	s.buildJobsMu.RLock()
	job, ok := s.buildJobs[buildID]
	var status, output string
	if ok && s.buildJobVisibleTo(c, job) {
		status = job.Status
		output = job.Output
	} else {
		ok = false
	}
	s.buildJobsMu.RUnlock()
	if !ok {
		respondError(c, http.StatusNotFound, "build not found")
		return
	}
	if status != "completed" {
		respondError(c, http.StatusNotFound, "build not ready")
		return
	}
	cleanPath := filepath.Clean(output)
	if _, err := os.Stat(cleanPath); err != nil {
		respondError(c, http.StatusNotFound, "build output file not found")
		return
	}
	serveFileSafe(c, cleanPath, s.extractAgentsDir(), sanitizeFilename(filepath.Base(cleanPath)))
}

// handleBuildList returns all active build jobs for the caller.
func (s *Server) handleBuildList(c *gin.Context) {
	respond(c, gin.H{"builds": s.buildJobSnapshots()})
}

// buildJobSnapshots returns a lock-protected snapshot of every active build
// job in the same JSON shape used by handleBuildList. Copying every field
// under the lock guarantees the build goroutine's mutations cannot race reads.
func (s *Server) buildJobSnapshots() []gin.H {
	s.buildJobsMu.RLock()
	defer s.buildJobsMu.RUnlock()
	resp := make([]gin.H, 0, len(s.buildJobs))
	for _, j := range s.buildJobs {
		entry := gin.H{
			"id":           j.ID,
			"status":       j.Status,
			"phase":        j.Phase,
			"platform":     j.Platform,
			"format":       j.Format,
			"filename":     j.Filename,
			"listener_id":  j.ListenerID,
			"profile_name": j.ProfileName,
			"profile_hash": j.ProfileHash,
			"created_at":   j.CreatedAt,
		}
		if j.Error != "" {
			entry["error"] = j.Error
		}
		if j.SignatureStatus != "" {
			entry["signature_status"] = j.SignatureStatus
			entry["signature_signer"] = j.SignatureSigner
			entry["signature_issuer"] = j.SignatureIssuer
			if j.SignatureError != "" {
				entry["signature_error"] = j.SignatureError
			}
		}
		if !j.StartedAt.IsZero() {
			entry["started_at"] = j.StartedAt
		}
		if !j.CompletedAt.IsZero() {
			entry["completed_at"] = j.CompletedAt
		}
		resp = append(resp, entry)
	}
	return resp
}

// runBuildAndUpdateJob is a helper to run a build function and update the job.
func (s *Server) runBuildAndUpdateJob(job *BuildJob, buildFn func() (string, error), platform, format, c2URL string, listenerID uint, filename string) {
	// A panic in the toolchain must never kill the worker goroutine (that
	// would silently stop the whole queue). Recover, mark the job failed,
	// and let the worker continue with the next queued build.
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("panic in build: %v", r)
			slog.Error("Async build panicked", "build_id", job.ID, "platform", platform, "format", format, "panic", r)
			s.completeBuildJob(job, "", err)
			s.logBuildWithProfile(platform, format, c2URL, listenerID, filename, "failed", err.Error(), "", job.ProfileName, job.ProfileHash)
			s.broadcastOperatorEvent(map[string]interface{}{
				"type":         "build_update",
				"build_id":     job.ID,
				"status":       "failed",
				"platform":     platform,
				"format":       format,
				"error":        err.Error(),
				"completed_at": job.CompletedAt,
			})
		}
	}()
	// The job is registered as "building" already; tell dashboards it has
	// actually started executing (queue wait is over).
	s.buildJobsMu.Lock()
	job.Phase = "building"
	job.StartedAt = time.Now()
	s.buildJobsMu.Unlock()
	s.broadcastOperatorEvent(map[string]interface{}{
		"type":     "build_update",
		"build_id": job.ID,
		"status":   "building",
		"phase":    "building",
		"platform": platform,
		"format":   format,
	})
	outPath, err := buildFn()
	s.completeBuildJob(job, outPath, err)
	if err != nil {
		s.logBuildWithProfile(platform, format, c2URL, listenerID, filename, "failed", err.Error(), "", job.ProfileName, job.ProfileHash)
		slog.Error("Async build failed", "build_id", job.ID, "platform", platform, "format", format, "error", err)
	} else {
		s.logBuildWithProfile(platform, format, c2URL, listenerID, filename, "success", "", outPath, job.ProfileName, job.ProfileHash)
		slog.Info("Async build completed", "build_id", job.ID, "platform", platform, "format", format, "output", outPath)
	}
	event := map[string]interface{}{
		"type":     "build_update",
		"build_id": job.ID,
		"status":   job.Status,
		"platform": platform,
		"format":   format,
	}
	if job.Error != "" {
		event["error"] = job.Error
	}
	if job.SignatureStatus != "" {
		event["signature_status"] = job.SignatureStatus
		event["signature_signer"] = job.SignatureSigner
		event["signature_issuer"] = job.SignatureIssuer
		if job.SignatureError != "" {
			event["signature_error"] = job.SignatureError
		}
	}
	if !job.CompletedAt.IsZero() {
		event["completed_at"] = job.CompletedAt
	}
	s.broadcastOperatorEvent(event)

	s.emitBuildOutcomeEvent(job, err, platform, format, filename)
}

// emitBuildOutcomeEvent publishes the build outcome on the automation bus.
//
// Kept separate from the websocket broadcast so a test can drive it with a job and an error without a live build: the event is the part
// a rule depends on, and it should be verifiable without a toolchain.
func (s *Server) emitBuildOutcomeEvent(job *BuildJob, err error, platform, format, filename string) {
	if s.eventManager == nil {
		return
	}
	s.eventManager.Emit(buildOutcomeEvent(job, err, platform, format, filename))
}

// buildOutcomeEvent builds the event a rule receives.
//
// Pure, and separate from the emit, because this payload is the contract a rule writes conditions against: a test can pin the type, the
// hint code and the fields a condition may reference without a running event bus. Delivery stays one call.
func buildOutcomeEvent(job *BuildJob, err error, platform, format, filename string) Event {
	buildType := EventBuildSucceeded
	buildData := map[string]interface{}{
		"build_id": job.ID,
		"platform": platform,
		"format":   format,
		"filename": filename,
	}
	if err != nil {
		buildType = EventBuildFailed
		buildData["error"] = job.Error
		if job.ErrorHint.Code != "" {
			buildData["hint_code"] = job.ErrorHint.Code
		}
	}
	return Event{
		Type:     buildType,
		TenantID: job.TenantID,
		// BuildJob carries no operator field; the log helper records it separately, so leave the actor empty rather than invent one.
		Timestamp: time.Now(),
		Data:      buildData,
	}
}

// Build queue: binary generation runs the Go/garble toolchain, which is
// expensive and must not run unbounded goroutines per request. Jobs are
// enqueued and executed by a small fixed worker pool in FIFO order.
const (
	maxQueuedBuilds     = 32
	buildWorkers        = 2
	maxConcurrentBuilds = 2
)

// queuedBuild is a build job waiting for a worker slot.
type queuedBuild struct {
	job        *BuildJob
	buildFn    func() (string, error)
	platform   string
	format     string
	c2URL      string
	listenerID uint
	filename   string
}

// submitBuild enqueues a build on the serialized worker queue. The job is
// registered with startBuildJob BEFORE submission so the frontend can poll its
// status immediately. Returns false when the queue is full.
func (s *Server) submitBuild(job *BuildJob, fn func() (string, error), platform, format, c2URL string, listenerID uint, filename string) bool {
	s.ensureBuildQueue()
	select {
	case s.buildQueue <- &queuedBuild{job, fn, platform, format, c2URL, listenerID, filename}:
		return true
	default:
		return false
	}
}

// ensureBuildQueue lazily starts the worker pool (safe for Server values
// constructed literally in tests, which never submit builds). Workers are
// wg-tracked and exit on s.ctx so shutdown never hangs on a wedged build.
func (s *Server) ensureBuildQueue() {
	s.buildQueueOnce.Do(func() {
		s.buildQueue = make(chan *queuedBuild, maxQueuedBuilds)
		s.buildSem = make(chan struct{}, maxConcurrentBuilds)
		ctx := s.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		for i := 0; i < buildWorkers; i++ {
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				for {
					select {
					case <-ctx.Done():
						return
					case q, ok := <-s.buildQueue:
						if !ok {
							return
						}
						s.buildSem <- struct{}{}
						s.runBuildAndUpdateJob(q.job, q.buildFn, q.platform, q.format, q.c2URL, q.listenerID, q.filename)
						<-s.buildSem
					}
				}
			}()
		}
	})
}

// withBuildSlot runs fn while holding a slot from the shared build semaphore,
// bounding concurrent toolchain invocations across BOTH the async queue worker
// and synchronous stager/one-liner/shellcode handlers.
func (s *Server) withBuildSlot(fn func() (string, error)) (string, error) {
	s.ensureBuildQueue()
	s.buildSem <- struct{}{}
	defer func() { <-s.buildSem }()
	return fn()
}

// registerTransientArtifact marks a generated file for automatic cleanup after
// one hour. Used for stager/one-liner outputs that are served once and are not
// tracked as BuildJobs (which cleanupBuildJobs already reaps).
func (s *Server) registerTransientArtifact(path string) {
	if path == "" {
		return
	}
	s.transientArtifactsMu.Lock()
	if s.transientArtifacts == nil {
		s.transientArtifacts = make(map[string]time.Time)
	}
	s.transientArtifacts[path] = time.Now().Add(time.Hour)
	s.transientArtifactsMu.Unlock()
}

// extractAgentsDir returns the absolute path to the agents output directory.
func (s *Server) extractAgentsDir() string {
	s.configMu.RLock()
	dataDir := s.cfg.Server.DataDir
	s.configMu.RUnlock()
	// Default to "data" the way writeScreenshotFile does, instead of resolving to
	// <cwd>/agents: an empty data_dir means "misconfigured", and build output belongs in
	// the conventional tree rather than beside whatever directory the process started in.
	if dataDir == "" {
		dataDir = "data"
	}
	agentsDir := filepath.Join(dataDir, "agents")
	if !filepath.IsAbs(agentsDir) {
		if abs, err := filepath.Abs(agentsDir); err == nil {
			agentsDir = abs
		}
	}
	return agentsDir
}

// clampIntervalJitter normalizes interval and jitter values.
func clampIntervalJitter(interval, jitter, beaconTime int) (int, int) {
	if beaconTime > 0 {
		interval = beaconTime
	}
	if interval < 1 {
		interval = 5
	}
	if interval > 86400 {
		interval = 86400
	}
	if jitter < 0 {
		jitter = 0
	}
	if jitter > 100 {
		jitter = 100
	}
	return interval, jitter
}

// parseArchitecture returns the normalized architecture string.
func parseArchitecture(arch string) string {
	arch = strings.TrimSpace(arch)
	if arch == "" {
		return "amd64"
	}
	// Normalize common aliases
	switch arch {
	case "x86_64":
		return "amd64"
	case "aarch64":
		return "arm64"
	case "i386", "x86":
		return "386"
	}
	return arch
}
