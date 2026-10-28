package server

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestListenerCreateReportsTheRealStatus is a GUARD on a property I expected to be broken and found
// intact: when the bind fails, the create path answers with status "stopped" AND the row says the
// same. I believed gorm's Model(&l).Update wrote the row but not the Go struct, so the response would
// claim "running" - it does not; the struct is updated too, and the debug print confirmed
// "status":"stopped" on the unmodified code. The test stays because the property is load-bearing
// (an operator must not be told a dead listener is live) and nothing else pinned it.
func TestListenerCreateReportsTheRealStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	// Failure injection through the SAME code path: the first listener binds the port, so the
	// second one on that address cannot. (Occupying the port from the test was not reliable:
	// Windows happily binds a wildcard address next to a specific one.)
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	create := func(name string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]interface{}{
			"name":     name,
			"scheme":   "tcp",
			"host":     "127.0.0.1",
			"port":     port,
			"protocol": "tcp",
		})
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/listeners", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(3))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleCreateListener(c)
		return w
	}

	first := create("binds-fine")
	if first.Code != http.StatusOK && first.Code != http.StatusCreated {
		t.Fatalf("the first listener did not create: %d %s", first.Code, first.Body.String())
	}

	// The second one cannot bind the same address.
	w := create("never-binds")
	t.Cleanup(func() {
		var l db.Listener
		if err := s.db.Where("name = ?", "binds-fine").First(&l).Error; err == nil {
			s.stopExtraListener(listenerKey(&l))
		}
	})

	// Whatever status code it chooses, the reported status must not be "running".
	var resp struct {
		Data struct {
			Listener db.Listener `json:"listener"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Logf("body: %s", w.Body.String())
	}
	if resp.Data.Listener.Name != "never-binds" {
		// A vacuous pass is worse than a failure: the handler must have answered with the
		// listener for the status assertion to mean anything.
		t.Fatalf("the response carried no listener to check: %s", w.Body.String())
	}
	if resp.Data.Listener.Status == "running" {
		t.Fatalf("create reported a listener as running that never bound: %s", w.Body.String())
	}

	// And the persisted row agrees with the response.
	var row db.Listener
	if err := s.db.Where("name = ?", "never-binds").First(&row).Error; err != nil {
		t.Fatalf("listener row missing: %v (body %s)", err, w.Body.String())
	}
	if row.Status == "running" {
		t.Fatalf("the row says running for a listener that never bound: %+v", row)
	}
}
