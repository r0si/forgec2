package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupNotificationRouteTestServer(t *testing.T) (*Server, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.AutoMigrate(&db.NotificationRoute{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	s := &Server{db: database}
	r := gin.New()
	r.GET("/routes", s.handleListNotificationRoutes)
	r.POST("/routes", s.handleCreateNotificationRoute)
	r.PUT("/routes/:id", s.handleUpdateNotificationRoute)
	r.POST("/routes/:id/test", s.handleTestNotificationRoute)
	return s, r
}

func performNotificationJSON(r *gin.Engine, method, path string, body interface{}) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestCreateNotificationRouteDoesNotEchoSecret(t *testing.T) {
	s, r := setupNotificationRouteTestServer(t)
	w := performNotificationJSON(r, http.MethodPost, "/routes", map[string]interface{}{
		"name": "soc-telegram", "channel": "telegram", "target": "-100123",
		"secret": "123456:bot-token", "min_severity": "warning",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("create returned %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Route db.NotificationRoute `json:"route"`
	}
	unmarshalData(t, w.Body.Bytes(), &response)
	if response.Route.Secret != notificationRouteMask {
		t.Fatalf("response exposed secret: %q", response.Route.Secret)
	}

	var stored db.NotificationRoute
	if err := s.db.First(&stored, response.Route.ID).Error; err != nil {
		t.Fatalf("load stored route: %v", err)
	}
	if stored.Secret != "123456:bot-token" {
		t.Fatalf("stored secret = %q", stored.Secret)
	}
}

func TestListNotificationRoutesRedactsWebhookTarget(t *testing.T) {
	s, r := setupNotificationRouteTestServer(t)
	route := db.NotificationRoute{
		Name: "soc-discord", Channel: "discord",
		Target: "https://discord.example/hooks/sensitive-token", Enabled: true,
	}
	if err := s.db.Create(&route).Error; err != nil {
		t.Fatalf("seed route: %v", err)
	}
	w := performNotificationJSON(r, http.MethodGet, "/routes", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list returned %d", w.Code)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("sensitive-token")) {
		t.Fatalf("list response exposed webhook credential: %s", w.Body.String())
	}
}

func TestCreateNotificationRouteRejectsPrivateTarget(t *testing.T) {
	_, r := setupNotificationRouteTestServer(t)
	w := performNotificationJSON(r, http.MethodPost, "/routes", map[string]interface{}{
		"name": "internal", "channel": "webhook", "target": "http://127.0.0.1:9000/hook",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("private target returned %d, want 400", w.Code)
	}
}

func TestUpdateNotificationRouteKeepsMaskedCredentials(t *testing.T) {
	s, r := setupNotificationRouteTestServer(t)
	route := db.NotificationRoute{
		Name: "existing", Channel: "discord", Target: "https://1.1.1.1/hooks/token",
		Secret: "stored-secret", Enabled: true, MinSeverity: "info",
	}
	if err := s.db.Create(&route).Error; err != nil {
		t.Fatalf("seed route: %v", err)
	}
	w := performNotificationJSON(r, http.MethodPut, "/routes/1", map[string]interface{}{
		"name": "renamed", "target": notificationRouteMask, "secret": notificationRouteMask,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("update returned %d: %s", w.Code, w.Body.String())
	}
	var stored db.NotificationRoute
	if err := s.db.First(&stored, route.ID).Error; err != nil {
		t.Fatalf("load route: %v", err)
	}
	if stored.Target != route.Target || stored.Secret != route.Secret {
		t.Fatalf("masked update changed credentials: target=%q secret=%q", stored.Target, stored.Secret)
	}
}

func TestNotificationRouteTestReportsDeliveryFailure(t *testing.T) {
	s, r := setupNotificationRouteTestServer(t)
	route := db.NotificationRoute{Name: "legacy", Channel: "webhook", Target: "http://127.0.0.1:1/hook"}
	if err := s.db.Create(&route).Error; err != nil {
		t.Fatalf("seed route: %v", err)
	}
	w := performNotificationJSON(r, http.MethodPost, "/routes/1/test", map[string]interface{}{})
	if w.Code != http.StatusBadGateway {
		t.Fatalf("failed delivery returned %d, want 502", w.Code)
	}
}

func TestBuildRoutePayloadChannels(t *testing.T) {
	n := &db.Notification{Severity: "critical", Type: "agent", Title: "Agent online", Message: "host-01 checked in", AgentID: "a1"}
	for _, ch := range []string{"discord", "telegram", "webhook", "dingtalk", "wecom", "feishu", "slack"} {
		payload, target, err := buildRoutePayload(ch, "https://hooks.example/x", "s3cr3t", n)
		if err != nil {
			t.Fatalf("channel %s: %v", ch, err)
		}
		if len(payload) == 0 || target == "" {
			t.Fatalf("channel %s returned empty payload/target", ch)
		}
		var decoded map[string]interface{}
		if err := json.Unmarshal(payload, &decoded); err != nil {
			t.Fatalf("channel %s payload is not JSON: %v", ch, err)
		}
	}
	if _, _, err := buildRoutePayload("nope", "https://x", "", n); err == nil {
		t.Fatal("unknown channel must error")
	}
	// DingTalk unsigned: target untouched; signed: timestamp+sign appended.
	if _, target, _ := buildRoutePayload("dingtalk", "https://oapi.dingtalk.com/robot/send?access_token=T", "", n); target != "https://oapi.dingtalk.com/robot/send?access_token=T" {
		t.Fatalf("unsigned dingtalk target rewritten: %q", target)
	}
	if _, target, _ := buildRoutePayload("dingtalk", "https://oapi.dingtalk.com/robot/send?access_token=T", "s3cr3t", n); !bytes.Contains([]byte(target), []byte("timestamp=")) || !bytes.Contains([]byte(target), []byte("sign=")) {
		t.Fatalf("signed dingtalk target missing query: %q", target)
	}
	// Telegram routes through the bot API, never the raw target.
	if _, target, _ := buildRoutePayload("telegram", "-100123", "tok", n); !bytes.Contains([]byte(target), []byte("api.telegram.org/bottok/sendMessage")) {
		t.Fatalf("telegram target wrong: %q", target)
	}
}

func TestValidNotificationChannelExtended(t *testing.T) {
	for _, ch := range []string{"discord", "telegram", "webhook", "dingtalk", "wecom", "feishu", "slack"} {
		if !validNotificationChannel(ch) {
			t.Fatalf("channel %s must be valid", ch)
		}
	}
	if validNotificationChannel("irc") {
		t.Fatal("irc must be invalid")
	}
	if err := validateNotificationRoute("dingtalk", "https://oapi.dingtalk.com/robot/send?access_token=T", ""); err != nil {
		t.Fatalf("dingtalk without secret must validate (unsigned mode): %v", err)
	}
}
