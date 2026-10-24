package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (s *Server) registerDNSRoutes(auth *gin.RouterGroup) {
	auth.GET("/dns", s.handleDNSPage)
	auth.GET("/api/dns/status", s.handleDNSStatus)
	auth.POST("/api/dns/start", s.handleDNSStart)
	auth.POST("/api/dns/stop", s.handleDNSStop)
}

func (s *Server) handleDNSPage(c *gin.Context) {
	s.renderPageOrJSON(c, gin.H{
		"Title":     "DNS C2",
		"ActiveNav": "dns",
	})
}

type dnsStatusResponse struct {
	Running     bool   `json:"running"`
	Domain      string `json:"domain"`
	Addr        string `json:"addr"`
	AgentIP     string `json:"agent_ip"`
	BeaconCount int    `json:"beacon_count"`
}

func (s *Server) handleDNSStatus(c *gin.Context) {
	s.configMu.RLock()
	resp := dnsStatusResponse{
		Running: false,
		Domain:  s.cfg.Server.DNSDomain,
		Addr:    s.cfg.Server.DNSAddr,
		AgentIP: s.cfg.Server.Host,
	}
	s.configMu.RUnlock()

	s.dnsListenerMu.Lock()
	dl := s.dnsListener
	s.dnsListenerMu.Unlock()
	if dl != nil {
		resp.Running = dl.IsRunning()
		resp.Domain = dl.Domain
		resp.Addr = dl.Addr
		resp.AgentIP = dl.AgentIP
	}

	respond(c, resp)
}

func (s *Server) handleDNSStart(c *gin.Context) {
	// The whole check -> bind -> publish sequence runs under dnsListenerMu:
	// two concurrent starts used to both pass the running check and both
	// bind, leaking one socket (the second assignment overwrote the first).
	// Start() binds synchronously, so the hold is short and operator-only.
	// Lock order is dnsListenerMu -> configMu; no path takes them nested in
	// the other direction.
	s.dnsListenerMu.Lock()
	defer s.dnsListenerMu.Unlock()
	if s.dnsListener != nil && s.dnsListener.IsRunning() {
		respond(c, gin.H{"status": "already_running"})
		return
	}

	var req struct {
		Domain    string `json:"domain"`
		Addr      string `json:"addr"`
		Server    string `json:"server"`
		TxtPrefix string `json:"txt_prefix"`
	}
	_ = c.ShouldBindJSON(&req)

	s.configMu.Lock()
	domain := req.Domain
	if domain == "" {
		domain = s.cfg.Server.DNSDomain
	}
	addr := req.Addr
	if addr == "" {
		addr = s.cfg.Server.DNSAddr
	}
	if addr == "" {
		addr = ":53"
	}
	if domain != "" {
		s.cfg.Server.DNSDomain = domain
	}
	s.cfg.Server.DNSAddr = addr
	s.cfg.Server.DNSEnabled = true
	host, obscure := s.cfg.Server.Host, s.cfg.Server.DNSObscure
	s.configMu.Unlock()

	dl := NewDNSBeaconListener(domain, host, 0, addr)
	dl.SetObscure(obscure)
	dl.SetHandler(s.makeBeaconHandler("dns"))

	if err := dl.Start(); err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "DNS"))
		return
	}
	s.dnsListener = dl
	respond(c, gin.H{"status": "started"})
}

func (s *Server) handleDNSStop(c *gin.Context) {
	s.dnsListenerMu.Lock()
	defer s.dnsListenerMu.Unlock()
	dl := s.dnsListener
	if dl == nil || !dl.IsRunning() {
		respond(c, gin.H{"status": "not_running"})
		return
	}

	if err := dl.Stop(); err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "DNS"))
		return
	}
	respond(c, gin.H{"status": "stopped"})
}
