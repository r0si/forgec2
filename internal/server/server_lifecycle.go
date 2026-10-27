package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
)

// ── Shutdown + Run (background loops, transports, HTTP serve) ─────────────

func (s *Server) Shutdown() {
	s.shutdownOnce.Do(func() { s.shutdown() })
}

func (s *Server) shutdown() {
	slog.Info("Shutting down server...")

	// Stop accepting new connections
	s.stopPprof()
	s.extraListenersMu.Lock()
	for key, srv := range s.extraListeners {
		slog.Info("Shutting down extra listener", "key", key)
		if srv != nil {
			srv.Close()
		}
	}
	clear(s.extraListeners)
	s.extraListenersMu.Unlock()
	if s.tcpLn != nil {
		s.tcpLn.Close()
	}
	if s.smbLn != nil {
		s.smbLn.Close()
	}
	if s.udpConn != nil {
		s.udpConn.Close()
	}
	if s.icmpListener != nil {
		s.icmpListener.Close()
	}
	s.dnsListenerMu.Lock()
	dnsLn := s.dnsListener
	s.dnsListenerMu.Unlock()
	if dnsLn != nil {
		slog.Info("Shutting down DNS listener")
		dnsLn.Close()
	}
	if s.grpcListener != nil {
		slog.Info("Shutting down gRPC listener")
		s.grpcListener.Stop()
	}
	if s.sshListener != nil {
		slog.Info("Shutting down SSH listener")
		s.sshListener.Close()
	}
	if s.h2cListener != nil {
		slog.Info("Shutting down H2C listener")
		s.h2cListener.Close()
	}
	if s.quicListener != nil {
		slog.Info("Shutting down QUIC listener")
		s.quicListener.Close()
	}
	// Tunnel helpers own FDs + pump goroutines outside the listener set:
	// SOCKS/rportfwd/lportfwd/TUN must drain here, not just transports.
	if s.tunEngine != nil {
		slog.Info("Shutting down TUN helpers")
		s.tunEngine.stopAll()
	}
	if s.socksEngine != nil {
		slog.Info("Shutting down SOCKS relays")
		s.socksEngine.stopAll()
	}
	s.stopAllRPortFwd()
	s.closeAllLPortFwd()
	if s.httpServer != nil {
		if s.fastStop.Load() {
			// The hot update replaces this process: cut the connections
			// instead of draining them, a graceful drain would only add
			// downtime to an already planned restart.
			if err := s.httpServer.Close(); err != nil {
				slog.Error("HTTP server close error", "err", err)
			}
		} else {
			// Wait briefly for in-flight requests to drain before forced shutdown
			done := make(chan struct{})
			if s.inFlight == nil {
				// A partially initialized server can own an HTTP server before the
				// request tracker is installed. Treat that state as having no
				// tracked requests instead of panicking during shutdown.
				close(done)
			} else {
				s.wg.Add(1)
				go func() {
					defer s.wg.Done()
					s.inFlight.Wait()
					close(done)
				}()
			}
			select {
			case <-done:
				slog.Info("All in-flight requests completed")
			case <-time.After(InFlightDrainTimeout):
				slog.Warn("Timed out waiting for in-flight requests, proceeding with shutdown")
			}
			shutdownCtx, cancel := context.WithTimeout(context.Background(), GracefulShutdownTimeout)
			defer cancel()
			if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
				slog.Error("HTTP server shutdown error", "err", err)
			}
		}
	}

	// Close external C2 WebSocket channels
	s.extC2ChannelsMu.Lock()
	for _, ch := range s.extC2Channels {
		if ch.Conn != nil {
			ch.Conn.Close()
		}
	}
	clear(s.extC2Channels)
	// Stop every registered channel poller (Discord/Slack/Telegram tickers).
	// Closing the conns above does not stop the poll loops, which would keep
	// reconnecting with deleted tokens until process exit.
	for key, r := range s.extC2Runners {
		delete(s.extC2Runners, key)
		if r != nil {
			r.Stop()
		}
	}
	s.extC2ChannelsMu.Unlock()

	// Stop subsystems
	if s.circuitBreaker != nil {
		s.circuitBreaker.Stop()
	}
	if s.backupManager != nil {
		s.backupManager.Stop()
	}
	if s.configReloader != nil {
		s.configReloader.Stop()
	}
	if s.marketplace != nil {
		s.marketplace.StopUpdateChecker()
	}
	if s.siem != nil {
		s.siem.Stop()
	}
	if s.opsecAdaptive != nil {
		s.opsecAdaptive.Stop()
	}
	if s.eventManager != nil {
		s.eventManager.Shutdown()
	}

	// Signal all goroutines to stop
	if s.ctxCancel != nil {
		s.ctxCancel()
	}

	// Tear down live WebRTC sessions and the embedded TURN listener before
	// waiting on background workers.
	s.webrtcInitMu.Lock()
	hub := s.webrtc
	s.webrtcInitMu.Unlock()
	if hub != nil {
		hub.close()
	}

	// Wait for tracked goroutines to finish, bounded so a wedged worker
	// cannot hang shutdown forever (the orchestrator would SIGKILL mid-WAL).
	waitDone := make(chan struct{})
	go func() {
		defer close(waitDone)
		s.wg.Wait()
	}()
	select {
	case <-waitDone:
		slog.Info("All background workers stopped")
	case <-time.After(WorkerDrainTimeout):
		slog.Error("Timed out waiting for background workers; proceeding to close DB",
			"timeout", WorkerDrainTimeout, "stack", string(debug.Stack()))
	}

	// Close database connection
	if s.db != nil {
		if sqlDB, err := s.db.DB(); err == nil {
			slog.Info("Closing database connection")
			sqlDB.Close()
		}
	}
}

func (s *Server) Run() error {
	// Embedded TURN for WebRTC remote desktop. Start it before serving so the
	// first webrtc/start request sees a ready relay; a bind failure is logged
	// and surfaced as 503 by the handler, not fatal to the C2 server.
	if s.cfg != nil && s.cfg.WebRTC.Enabled {
		if err := s.webRTCHub().ensureTURN(); err != nil {
			slog.Error("Embedded TURN failed to start", "err", err)
		}
	}
	certPath := s.cfg.Server.CertFile
	keyPath := s.cfg.Server.KeyFile

	if s.cfg.Server.TLSEnabled {
		if err := crypto.GenerateSelfSignedCert(certPath, keyPath); err != nil {
			slog.Error("Failed to generate self-signed cert", "err", err)
			return err
		}
		slog.Info("TLS certificate ready", "cert", certPath)
	}

	// async audit writer: request paths enqueue, single worker persists
	s.initAuditAnchor()
	s.startAuditWorker()

	// vault re-encrypt: normalize previous-key loot rows after rotation
	s.startVaultReencryptLoop()

	// TLS expiry monitoring is meaningful only when the HTTPS listener is
	// enabled. Skipping it for plain-HTTP development deployments avoids a
	// misleading missing-certificate warning and an unnecessary goroutine.
	if s.cfg.Server.TLSEnabled {
		// Regenerate/upload take effect for new handshakes without restart via
		// the certificate loader.
		s.checkTLSCertExpiry("startup")
		s.startTLSCertMonitor()
	}

	// start periodic cleanup
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runPeriodicCleanup()
	}()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.cleanupStaleSocks()
	}()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.periodicRPortFwdCleanup()
	}()
	s.startMetricAlertLoop()
	// Rebuild the in-memory pending counters from the DB before serving:
	// otherwise every restart under-counts until the first 5-minute tick,
	// wrongly admitting tasks past MaxPendingTasksPerAgent.
	s.reconcilePendingTaskCounts()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(StaleTaskRequeueInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				// Panic guard: sibling maintenance loops have one; a panic
				// here would otherwise take the process down mid-ticker.
				func() {
					defer func() {
						if r := recover(); r != nil {
							slog.Error("Panic in stale-task maintenance loop", "recover", r, "stack", string(debug.Stack()))
						}
					}()
					s.requeueStaleTasks()
					s.failStaleAcknowledgedTasks()
					s.rejectExpiredApprovals()
					s.reconcilePendingTaskCounts()
					s.updateTaskBacklogMetrics()
					s.reapOrphanedRelayChildren()
				}()
			}
		}
	}()

	// periodic metrics refresh (same interval as nav stats cache). The fast
	// tick covers the predicate-indexed gauges; the unfiltered COUNT(*)
	// walks (total agents/credentials) run on a separate 60s ticker so they
	// stop competing with beacons for the single SQLite writer every 5s.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("recovered from panic", "err", r, "stack", string(debug.Stack()))
			}
		}()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		slow := time.NewTicker(60 * time.Second)
		defer slow.Stop()
		// Seed the slow gauges at boot so a scrape before the first slow
		// tick still sees real totals instead of zero.
		s.updateSlowMetricsFromDB()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				s.updateMetricsFromDB()
			case <-slow.C:
				s.updateSlowMetricsFromDB()
			}
		}
	}()

	// schedule-driven automation rules (event_type="schedule")
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("recovered from panic", "err", r, "stack", string(debug.Stack()))
			}
		}()
		s.schedulerLoop()
	}()

	// One-shot scheduled task dispatcher ("at 02:00 run X on agent Y")
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("recovered from panic", "err", r, "stack", string(debug.Stack()))
			}
		}()
		s.recoverClaimedOneShotTasks()
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				func() {
					defer func() {
						if r := recover(); r != nil {
							slog.Error("Panic in one-shot dispatcher tick", "recover", r, "stack", string(debug.Stack()))
						}
					}()
					s.dispatchDueOneShotTasks()
				}()
			}
		}
	}()

	// Listener self-check heartbeat: probes every enabled listener so a dead
	// transport flips status within minutes instead of missing beacons.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.startListenerHealthLoop()
	}()

	// Initialize Circuit Breaker
	s.circuitBreaker = NewCircuitBreaker(s.cfg)
	s.circuitBreaker.SetOnBurnedHandler(func(targetID string) {
		if strings.HasPrefix(targetID, redirectorTargetPrefix) {
			slog.Warn("Circuit breaker triggered: redirector BURNED", "target_id", targetID)
			rdID := strings.TrimPrefix(targetID, redirectorTargetPrefix)
			if err := s.db.Model(&db.Redirector{}).Where("id = ?", rdID).Update("status", "down").Error; err != nil {
				slog.Error("Failed to mark redirector down after burn", "redirector_id", rdID, "error", err)
			}
			s.broadcastOperatorEvent(map[string]interface{}{
				"type":          "redirector_update",
				"action":        "burned",
				"redirector_id": rdID,
			})
			return
		}
		slog.Warn("Circuit breaker triggered: listener BURNED", "listener_id", targetID)
		// Automatically push profile rotation to agents on this listener
		s.rotateAgentsOnBurnedListener(targetID)
	})
	s.circuitBreaker.Start()
	s.registerExistingProbeTargets()

	// start update checker (opt-in; phones home to GitHub releases)
	if s.cfg.Server.UpdateCheckEnabled {
		s.initUpdateChecker()
	} else {
		slog.Info("Update check disabled (server.update_check_enabled=false); no outbound GitHub traffic")
	}

	// periodic cleanup of hosted one-liner payloads
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("Payload cleanup panicked", "recover", r)
			}
		}()
		ticker := time.NewTicker(PayloadCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				s.cleanupOldPayloads()
				s.cleanupOldThumbs()
			}
		}
	}()

	// Start TCP transport layer if enabled (high priority feature)
	if s.cfg.Server.TCPEnabled && s.cfg.Server.TCPAddr != "" {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.startTCPListener()
		}()
	}
	if s.cfg.Server.SMBEnabled && s.cfg.Server.SMBPipe != "" {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.startSMBListener()
		}()
	}

	// Start UDP datagram transport listener if enabled (P2: low-overhead
	// connectionless beacon channel that mirrors the raw TCP envelope framing).
	if s.cfg.Server.UDPEnabled && s.cfg.Server.UDPAddr != "" {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.startUDPListener()
		}()
	}

	// Start ICMP C2 listener if enabled
	if s.cfg.Server.ICMPEnabled {
		il := NewICMPBeaconListener(s.cfg.Server.ICMPAddr)
		il.SetHandler(s.makeBeaconHandler("icmp"))
		if err := il.Start(); err != nil {
			slog.Error("Failed to start ICMP listener", "err", err)
		} else {
			s.icmpListener = il
		}
	}

	// Start DNS C2 listener if enabled
	s.configMu.RLock()
	dnsEnabled, dnsDomain, dnsAddr, dnsHost, dnsObscure := s.cfg.Server.DNSEnabled, s.cfg.Server.DNSDomain, s.cfg.Server.DNSAddr, s.cfg.Server.Host, s.cfg.Server.DNSObscure
	s.configMu.RUnlock()
	if dnsEnabled && dnsDomain != "" {
		dl := NewDNSBeaconListener(dnsDomain, dnsHost, 0, dnsAddr)
		dl.SetObscure(dnsObscure)
		dl.SetHandler(s.makeBeaconHandler("dns"))
		s.dnsListenerMu.Lock()
		s.dnsListener = dl
		s.dnsListenerMu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			// Start() binds synchronously before returning; surface bind
			// failures instead of logging "starting" with a dead listener.
			if err := dl.Start(); err != nil {
				slog.Error("DNS C2 listener failed to start", "domain", dnsDomain, "addr", dnsAddr, "err", err)
			}
		}()
	}

	// Start gRPC transport layer if enabled
	if s.cfg.Server.GRPCEnabled && s.cfg.Server.GRPCAddr != "" {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.startGRPCListener()
		}()
	}

	// Start SSH beacon listener if enabled (config-driven; DB listeners are
	// restored separately via startExtraListenersFromDB)
	if s.cfg.Server.SSHEnabled {
		sshAddr := s.cfg.Server.SSHAddr
		if sshAddr == "" {
			sshAddr = ":" + itoa(s.cfg.Server.SSHPort)
		}
		cfg, cfgErr := s.newSSHListenerConfig(sshAddr)
		if cfgErr != nil {
			slog.Error("Failed to prepare SSH listener", "addr", sshAddr, "err", cfgErr)
		} else {
			sl := NewSSHBeaconListener(cfg)
			sl.SetHandler(s.makeBeaconHandler("ssh"))
			if err := sl.Start(); err != nil {
				slog.Error("Failed to start SSH listener", "addr", sshAddr, "err", err)
			} else {
				s.sshListener = sl
			}
		}
	}

	if s.cfg.Server.QUICEnabled && s.cfg.Server.QUICAddr != "" {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.startQUICListener()
		}()
	}

	// Start H2C (cleartext HTTP/2) beacon listener if enabled
	if s.cfg.Listeners.H2C.Enabled && s.cfg.Listeners.H2C.Addr != "" {
		hl := NewH2CBeaconListener(s.cfg.Listeners.H2C.Addr, s.router)
		if err := hl.Start(); err != nil {
			slog.Error("Failed to start H2C listener", "addr", s.cfg.Listeners.H2C.Addr, "err", err)
		} else {
			s.h2cListener = hl
		}
	}

	// Auto-generate ExtC2 token if empty
	if s.cfg.RateLimit.ExtC2.APIToken == "" {
		tokenBytes := make([]byte, 32)
		if _, err := rand.Read(tokenBytes); err == nil {
			s.cfg.RateLimit.ExtC2.APIToken = hex.EncodeToString(tokenBytes)
			if err := s.cfg.Save(s.configPath); err == nil {
				slog.Info("Auto-generated ExtC2 API token")
			}
		} else {
			slog.Error("Failed to generate ExtC2 API token", "err", err)
		}
	}

	// Restore External C2 channels from DB
	s.restoreExtC2Channels()

	// Restore the persisted domain-fronting list (memory-only before, so it
	// was lost on every restart).
	s.restoreDomainFrontConfig()

	// Start async build job cleanup goroutine
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.cleanupBuildJobs()
	}()

	// Start extra listeners from DB (created via the UI in previous sessions)
	s.startExtraListenersFromDB()

	// Opt-in profiling endpoints (loopback only, disabled by default). Started
	// before the main listener so a misconfigured address fails the boot
	// instead of surfacing after the server claims to be up.
	if err := s.startPprof(); err != nil {
		return err
	}

	// Check main server port availability before attempting to bind
	addr := s.cfg.Server.Host + ":" + itoa(s.cfg.Server.Port)
	if !isPortAvailable(s.cfg.Server.Host, s.cfg.Server.Port) {
		return fmt.Errorf("port %s is already in use — check for another server instance or change server.port in config.yaml", addr)
	}

	slog.Info("Starting ForgeC2 server", "addr", addr, "tls", s.cfg.Server.TLSEnabled)

	// The console listens on every interface by default. A browser on another
	// host then has to be allowed explicitly (server.allowed_origins), or the
	// API calls are refused as a cross-origin request - a confusing first
	// symptom for an operator who just changed the bind address.
	if isWildcardHost(s.cfg.Server.Host) && len(s.cfg.Server.AllowedOrigins) == 0 {
		slog.Warn("The console listens on all interfaces and server.allowed_origins is empty: only browsers on localhost can use the API. Add the hostnames you operate from to server.allowed_origins.",
			"addr", addr)
	}

	if s.cfg.Server.GeoIPEnabled {
		slog.Warn("GeoIP lookups are ENABLED: the public IP of every beaconing host will be sent to the third-party service ip-api.com. Disable server.geoip_enabled unless this exfiltration is acceptable for the engagement.")
	}

	s.httpServer = s.newHTTPServer(addr)
	// Bind explicitly so a failed bind is reported here, and so the boot
	// marker below is written only once the port is actually ours. ServeTLS
	// (not a hand-built tls.Listener) is required: it installs the HTTP/2
	// handler the TLS profile advertises through ALPN, and a listener that
	// advertises h2 but speaks HTTP/1.1 fails every browser and curl request.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	if s.cfg.Server.TLSEnabled {
		if err := s.configureTLS(s.httpServer); err != nil {
			_ = ln.Close()
			return err
		}
	}
	// The port is bound and the routes are up: a hot update that installed
	// this build is serving now. Tell the restarter, otherwise it would
	// roll back a perfectly healthy boot.
	s.markUpdateBootOK()
	return s.serve(s.httpServer, ln)
}

// serve accepts connections on the already-bound listener. TLS goes through
// ServeTLS, never a hand-built tls.Listener: ServeTLS is what installs the
// HTTP/2 handler, and the rotating TLS profile advertises h2 in ALPN. A
// listener that advertises h2 while speaking HTTP/1.1 accepts the handshake
// and then fails every request, which looks like a dead server to a browser.
func (s *Server) serve(srv *http.Server, ln net.Listener) error { // config-lock: boot, decides TLS before the first request
	if s.cfg.Server.TLSEnabled {
		// Empty cert/key paths: the certificate comes from GetCertificate
		// (hot-reloadable loader), not from files read once at startup.
		return srv.ServeTLS(ln, "", "")
	}
	return srv.Serve(ln)
}
