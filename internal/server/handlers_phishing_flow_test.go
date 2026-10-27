package server

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	forgecrypto "github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// fakeSMTPServer accepts a minimal SMTP conversation and records every
// message, which lets the phishing sender be exercised without a real relay.
type fakeSMTPServer struct {
	addr      string
	mu        sync.Mutex
	messages  []string
	envelopes []string
}

func newFakeSMTPServer(t *testing.T) *fakeSMTPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv := &fakeSMTPServer{addr: ln.Addr().String()}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go srv.serve(conn)
		}
	}()
	return srv
}

func (s *fakeSMTPServer) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := func(line string) { _, _ = fmt.Fprintf(conn, "%s\r\n", line) }
	w("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			w("250-fake")
			w("250 OK")
		case strings.HasPrefix(cmd, "MAIL FROM"):
			s.mu.Lock()
			s.envelopes = append(s.envelopes, strings.TrimSpace(line))
			s.mu.Unlock()
			w("250 OK")
		case strings.HasPrefix(cmd, "RCPT TO"):
			s.mu.Lock()
			s.envelopes = append(s.envelopes, strings.TrimSpace(line))
			s.mu.Unlock()
			w("250 OK")
		case strings.HasPrefix(cmd, "DATA"):
			w("354 go ahead")
			var body strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(l, "\r\n") == "." {
					break
				}
				body.WriteString(l)
			}
			s.mu.Lock()
			s.messages = append(s.messages, body.String())
			s.mu.Unlock()
			w("250 queued")
		case strings.HasPrefix(cmd, "QUIT"):
			w("221 bye")
			return
		case strings.HasPrefix(cmd, "RSET"), strings.HasPrefix(cmd, "NOOP"):
			w("250 OK")
		default:
			w("250 OK")
		}
	}
}

func (s *fakeSMTPServer) hostPort() (string, int) {
	h, p, _ := net.SplitHostPort(s.addr)
	fmt.Sscanf(p, "%d", new(int))
	var port int
	fmt.Sscanf(p, "%d", &port)
	return h, port
}

func (s *fakeSMTPServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.messages)
}

func TestSendPhishingMailSanitizesHeaders(t *testing.T) {
	srv := newFakeSMTPServer(t)
	host, port := srv.hostPort()

	// CRLF in the subject would inject arbitrary headers; it must be flattened.
	err := sendPhishingMail(host, port, "", "", "sender@example.test", "victim@example.test",
		"Hello\r\nBcc: attacker@evil.test", "Body text", false)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if srv.count() != 1 {
		t.Fatalf("server received %d messages", srv.count())
	}
	srv.mu.Lock()
	msg := srv.messages[0]
	envs := strings.Join(srv.envelopes, "\n")
	srv.mu.Unlock()
	if strings.Contains(msg, "\r\nBcc:") {
		t.Fatalf("header injection survived: %q", msg)
	}
	if !strings.Contains(msg, "To: victim@example.test") || !strings.Contains(msg, "Body text") {
		t.Fatalf("message malformed: %q", msg)
	}
	if !strings.Contains(envs, "sender@example.test") || !strings.Contains(envs, "victim@example.test") {
		t.Fatalf("envelope = %q", envs)
	}
	// A zero port falls back to 587 rather than dialling ":0".
	if err := sendPhishingMail(host, 0, "", "", "a@b.test", "c@d.test", "s", "b", false); err == nil {
		t.Fatal("send to the default port unexpectedly succeeded")
	}
}

func TestRunPhishingCampaignSendsAndRecordsEvents(t *testing.T) {
	s := mustTenantServer(t)
	// PhishingEvent.Payload is encrypted at rest; without a loot key the insert
	// is refused rather than stored in plaintext.
	forgecrypto.InitLootEncryption(strings.Repeat("26", 32))
	srv := newFakeSMTPServer(t)
	host, port := srv.hostPort()

	camp := db.PhishingCampaign{Name: "camp", Status: "running", SMTPHost: host, SMTPPort: port, TargetList: "a@example.test\nb@example.test"}
	if err := s.db.Create(&camp).Error; err != nil {
		t.Fatal(err)
	}
	tpl := db.PhishingTemplate{Name: "tpl", Subject: "Hi {{email}}", Body: "Click {{landing_url}}", FromEmail: "sender@example.test", Type: "html"}

	done := make(chan struct{})
	go func() {
		s.runPhishingCampaign(camp.ID, tpl, []string{"a@example.test", "b@example.test"}, host, port, "", "", tpl.FromEmail, true, "https://c2.test")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("campaign runner did not finish")
	}

	var stored db.PhishingCampaign
	if err := s.db.First(&stored, camp.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.SentCount != 2 || stored.FailedCount != 0 {
		t.Fatalf("campaign counts = sent %d failed %d", stored.SentCount, stored.FailedCount)
	}
	if stored.Status != "completed" {
		t.Fatalf("campaign status = %q, want completed", stored.Status)
	}
	var events []db.PhishingEvent
	if err := s.db.Where("campaign_id = ?", camp.ID).Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	for _, e := range events {
		if e.EventType != "sent" || e.Token == "" || e.Email == "" {
			t.Fatalf("event = %+v", e)
		}
	}
	if srv.count() != 2 {
		t.Fatalf("SMTP server saw %d messages, want 2", srv.count())
	}
	// The rendered body must carry the per-recipient landing link.
	if body := srv.messages[0]; !strings.Contains(body, "https://c2.test/phishing/l/") {
		t.Fatalf("landing link missing from body: %q", body)
	}
}

func TestRunPhishingCampaignRecordsFailures(t *testing.T) {
	s := mustTenantServer(t)
	forgecrypto.InitLootEncryption(strings.Repeat("26", 32))
	camp := db.PhishingCampaign{Name: "failing", Status: "running", SMTPHost: "127.0.0.1", SMTPPort: 1, TargetList: "a@example.test"}
	if err := s.db.Create(&camp).Error; err != nil {
		t.Fatal(err)
	}
	tpl := db.PhishingTemplate{Subject: "s", Body: "b", FromEmail: "sender@example.test", Type: "text"}

	done := make(chan struct{})
	go func() {
		s.runPhishingCampaign(camp.ID, tpl, []string{"a@example.test"}, "127.0.0.1", 1, "", "", tpl.FromEmail, false, "https://c2.test")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("campaign runner did not finish")
	}
	var stored db.PhishingCampaign
	if err := s.db.First(&stored, camp.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.FailedCount != 1 || stored.SentCount != 0 {
		t.Fatalf("counts = sent %d failed %d", stored.SentCount, stored.FailedCount)
	}
	var evt db.PhishingEvent
	if err := s.db.Where("campaign_id = ?", camp.ID).First(&evt).Error; err != nil {
		t.Fatal(err)
	}
	if evt.EventType != "send_failed" || !strings.Contains(evt.Payload, "error") {
		t.Fatalf("failure event = %+v", evt)
	}
}

func TestRunPhishingCampaignStopsWhenCancelled(t *testing.T) {
	s := mustTenantServer(t)
	forgecrypto.InitLootEncryption(strings.Repeat("26", 32))
	camp := db.PhishingCampaign{Name: "stopped", Status: "stopped", SMTPHost: "127.0.0.1", SMTPPort: 1, TargetList: "a@example.test"}
	if err := s.db.Create(&camp).Error; err != nil {
		t.Fatal(err)
	}
	tpl := db.PhishingTemplate{Subject: "s", Body: "b", FromEmail: "sender@example.test", Type: "text"}
	done := make(chan struct{})
	go func() {
		s.runPhishingCampaign(camp.ID, tpl, []string{"a@example.test", "b@example.test"}, "127.0.0.1", 1, "", "", tpl.FromEmail, false, "https://c2.test")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stopped campaign did not return")
	}
	var count int64
	s.db.Model(&db.PhishingEvent{}).Where("campaign_id = ?", camp.ID).Count(&count)
	if count != 0 {
		t.Fatalf("stopped campaign logged %d events", count)
	}
}

func TestLaunchPhishingCampaignValidationDoesNotWedge(t *testing.T) {
	s := mustTenantServer(t)
	admin := &db.User{Username: "admin", Role: db.RoleAdmin, TenantID: 1, IsActive: true}
	if err := s.db.Create(admin).Error; err != nil {
		t.Fatal(err)
	}

	// No template: refused, and the campaign must stay launchable.
	// Owned by the admin: the launch now resolves the campaign through its creator, so a row
	// with no owner is invisible to a tenant-scoped caller (and "admin" IS in tenant 1).
	camp := db.PhishingCampaign{Name: "no-template", Status: "draft", SMTPHost: "127.0.0.1", SMTPPort: 25, TargetList: "a@example.test", CreatedBy: "admin"}
	if err := s.db.Create(&camp).Error; err != nil {
		t.Fatal(err)
	}
	c, w := missingAPICtx(http.MethodPost, "/phishing/campaigns/x/launch", "",
		gin.Params{{Key: "id", Value: jsonNumber(camp.ID)}})
	s.handleAPILaunchPhishingCampaign(c)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "template not found") {
		t.Fatalf("no template = %d %s", w.Code, w.Body.String())
	}
	var after db.PhishingCampaign
	if err := s.db.First(&after, camp.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Status == "running" {
		t.Fatal("a failed launch left the campaign wedged in running state")
	}

	// Targets missing.
	tpl := db.PhishingTemplate{Name: "tpl", Subject: "s", Body: "b", FromEmail: "s@example.test", Type: "text"}
	if err := s.db.Create(&tpl).Error; err != nil {
		t.Fatal(err)
	}
	noTargets := db.PhishingCampaign{Name: "no-targets", Status: "draft", TemplateID: tpl.ID, SMTPHost: "127.0.0.1", TargetList: "not-an-email", CreatedBy: "admin"}
	if err := s.db.Create(&noTargets).Error; err != nil {
		t.Fatal(err)
	}
	c, w = missingAPICtx(http.MethodPost, "/phishing/campaigns/x/launch", "",
		gin.Params{{Key: "id", Value: jsonNumber(noTargets.ID)}})
	s.handleAPILaunchPhishingCampaign(c)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "no valid targets") {
		t.Fatalf("no targets = %d %s", w.Code, w.Body.String())
	}

	// SMTP host missing.
	noSMTP := db.PhishingCampaign{Name: "no-smtp", Status: "draft", TemplateID: tpl.ID, TargetList: "a@example.test", CreatedBy: "admin"}
	if err := s.db.Create(&noSMTP).Error; err != nil {
		t.Fatal(err)
	}
	c, w = missingAPICtx(http.MethodPost, "/phishing/campaigns/x/launch", "",
		gin.Params{{Key: "id", Value: jsonNumber(noSMTP.ID)}})
	s.handleAPILaunchPhishingCampaign(c)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "smtp_host is required") {
		t.Fatalf("no smtp = %d %s", w.Code, w.Body.String())
	}

	// from_email missing on the template.
	noFrom := db.PhishingTemplate{Name: "no-from", Subject: "s", Body: "b", Type: "text", CreatedBy: "admin"}
	if err := s.db.Create(&noFrom).Error; err != nil {
		t.Fatal(err)
	}
	campNoFrom := db.PhishingCampaign{Name: "no-from-camp", Status: "draft", TemplateID: noFrom.ID, SMTPHost: "127.0.0.1", TargetList: "a@example.test", CreatedBy: "admin"}
	if err := s.db.Create(&campNoFrom).Error; err != nil {
		t.Fatal(err)
	}
	c, w = missingAPICtx(http.MethodPost, "/phishing/campaigns/x/launch", "",
		gin.Params{{Key: "id", Value: jsonNumber(campNoFrom.ID)}})
	s.handleAPILaunchPhishingCampaign(c)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "from_email is required") {
		t.Fatalf("no from = %d %s", w.Code, w.Body.String())
	}

	// Unknown campaign.
	c, w = missingAPICtx(http.MethodPost, "/phishing/campaigns/x/launch", "",
		gin.Params{{Key: "id", Value: "999999"}})
	s.handleAPILaunchPhishingCampaign(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown campaign = %d", w.Code)
	}
}
