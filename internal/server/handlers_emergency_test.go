package server

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/forgec2/forgec2/internal/testutil"
	"github.com/gin-gonic/gin"
)

const killSwitchTestBeaconKey = "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"

func initKillSwitchServer(t *testing.T) *Server {
	t.Helper()
	ginSetTestMode(t)
	database := testutil.SetupTestDB(t)
	s := &Server{
		db:                  database,
		cfg:                 &config.Config{},
		eventManager:        NewEventManager(database),
		beaconDedupCache:    make(map[string]time.Time),
		agentStatusCooldown: make(map[string]time.Time),
		agentPendingTasks:   make(map[string]int),
		metrics:             NewMetricsCollector(nil),
	}
	s.configMu.Lock()
	s.cfg.Server.BeaconKey = killSwitchTestBeaconKey
	s.configMu.Unlock()
	s.reloadKillSwitchState()
	return s
}

// seedKillSwitchOperator creates an admin user and returns it.
func seedKillSwitchOperator(t *testing.T, s *Server) db.User {
	t.Helper()
	hash, err := middleware.HashPassword("correct-horse")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	u := db.User{Username: "admin", PasswordHash: hash, Role: db.RoleAdmin, IsActive: true}
	if err := s.db.Create(&u).Error; err != nil {
		t.Fatalf("create operator: %v", err)
	}
	return u
}

func TestEnforceKillSwitchUnarmed(t *testing.T) {
	s := initKillSwitchServer(t)
	s.setKillSwitch(false, "", "tester")

	resp := beaconResponse{}
	s.enforceKillSwitch(db.Implant{ID: "test-agent-1"}, &resp)
	if resp.KillSwitch != "" || resp.KillSwitchMAC != "" {
		t.Fatalf("kill switch must be empty when unarmed: %+v", resp)
	}
}

func TestEnforceKillSwitchArmedAuthenticated(t *testing.T) {
	s := initKillSwitchServer(t)
	implant := db.Implant{ID: "test-agent-1"}

	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	s.setKillSwitch(true, token, "tester")

	resp := beaconResponse{}
	s.enforceKillSwitch(implant, &resp)
	if resp.KillSwitch != token {
		t.Fatalf("broadcast token = %q, want %q", resp.KillSwitch, token)
	}
	regKey := s.deriveRegKey(implant.ID)
	if len(regKey) == 0 {
		t.Fatal("deriveRegKey returned nil")
	}
	want := hex.EncodeToString(crypto.KillSwitchHMAC(crypto.DeriveKillSwitchKey(regKey), []byte(token)))
	if resp.KillSwitchMAC != want {
		t.Fatalf("broadcast MAC = %q, want %q", resp.KillSwitchMAC, want)
	}

	// The MAC must be exactly what the agent-side verifier recomputes
	// (agent mirror is cross-checked in internal/payload/agent).
	derived := crypto.DeriveKillSwitchKey(crypto.DeriveRegistrationKeyFromHex(killSwitchTestBeaconKey, implant.ID))
	if hex.EncodeToString(crypto.KillSwitchHMAC(derived, []byte(token))) != resp.KillSwitchMAC {
		t.Fatal("kill-switch MAC not reproducible from the beacon key derivation")
	}

	// After disarm the same response must carry nothing again.
	s.setKillSwitch(false, "", "tester")
	resp2 := beaconResponse{}
	s.enforceKillSwitch(implant, &resp2)
	if resp2.KillSwitch != "" || resp2.KillSwitchMAC != "" {
		t.Fatalf("kill switch must vanish after disarm: %+v", resp2)
	}
}

func TestHandleKillSwitchArmDisarm(t *testing.T) {
	s := initKillSwitchServer(t)
	user := seedKillSwitchOperator(t, s)

	// Seed a small fleet.
	for _, id := range []string{"agent-1", "agent-2", "agent-3"} {
		if err := s.db.Create(&db.Implant{ID: id, Registered: true}).Error; err != nil {
			t.Fatalf("seed agent: %v", err)
		}
	}

	// Wrong password must be rejected.
	postKillSwitch(t, s, user.ID, "arm", "wrong-password", http.StatusUnauthorized)

	// Correct password arms the fleet and dispatches uninstall tasks.
	postKillSwitch(t, s, user.ID, "arm", "correct-horse", http.StatusOK)

	var ks db.KillSwitch
	if err := s.db.First(&ks, 1).Error; err != nil {
		t.Fatalf("kill switch row missing: %v", err)
	}
	if !ks.Armed || len(ks.Token) != 64 || ks.TriggeredBy != "admin" || ks.TriggeredAt == nil {
		t.Fatalf("persisted kill switch wrong: %+v", ks)
	}
	armed, token := s.killSwitchState()
	if !armed || token != ks.Token {
		t.Fatalf("cache state mismatch: armed=%v token=%q", armed, token)
	}
	var dispatched int64
	if err := s.db.Model(&db.Task{}).Where("type = ?", "uninstall").Count(&dispatched).Error; err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if dispatched != 3 {
		t.Fatalf("expected 3 uninstall tasks, got %d", dispatched)
	}

	// Disarm clears everything.
	postKillSwitch(t, s, user.ID, "disarm", "correct-horse", http.StatusOK)
	var ks2 db.KillSwitch
	if err := s.db.First(&ks2, 1).Error; err != nil {
		t.Fatalf("kill switch row missing after disarm: %v", err)
	}
	if ks2.Armed || ks2.DisarmedBy != "admin" {
		t.Fatalf("kill switch not disarmed: %+v", ks2)
	}
	armed, _ = s.killSwitchState()
	if armed {
		t.Fatal("cache still armed after disarm")
	}

	// Invalid action.
	postKillSwitch(t, s, user.ID, "napalm", "correct-horse", http.StatusBadRequest)
}

func TestHandleKillSwitchStatus(t *testing.T) {
	s := initKillSwitchServer(t)
	user := seedKillSwitchOperator(t, s)

	// readArmed calls the status endpoint and returns the data.armed it reports.
	// The response is the single envelope {"success":true,"data":{"armed":…}}.
	// The first version of this test decoded "armed" off the TOP level, where it
	// never is, so body.Armed was always the zero false and the "must show
	// unarmed" check passed without proving the field was readable at all — and
	// the nested envelope was what had buried it one level down. Decoding
	// data.armed and exercising BOTH states makes a silently-false decode fail
	// loudly instead of looking green.
	readArmed := func() bool {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/admin/killswitch/status", nil)
		c.Set("user_id", user.ID)
		s.handleKillSwitchStatus(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status: expected 200 got %d", w.Code)
		}
		var body struct {
			Success bool `json:"success"`
			Data    struct {
				Armed bool `json:"armed"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("status json: %v", err)
		}
		if !body.Success {
			t.Fatal("the status response lost its outer envelope")
		}
		return body.Data.Armed
	}

	if readArmed() {
		t.Fatal("status must show unarmed by default")
	}

	// The positive half: after arming the fleet, the endpoint must report
	// armed=true. This is the assertion that makes the negative one meaningful —
	// without it, a handler that always returned unarmed would still pass.
	postKillSwitch(t, s, user.ID, "arm", "correct-horse", http.StatusOK)
	if !readArmed() {
		t.Fatal("status must show armed after arming the kill switch")
	}
}

func postKillSwitch(t *testing.T, s *Server, userID uint, action, password string, wantCode int) {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"action": action, "password": password})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/admin/killswitch", bytes.NewReader(payload))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user_id", userID)
	s.handleKillSwitch(c)
	if w.Code != wantCode {
		t.Fatalf("killswitch %s: expected %d got %d; body=%s", action, wantCode, w.Code, w.Body.String())
	}
}
