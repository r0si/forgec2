package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	forgecrypto "github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func attachmentCtx(sessionID uint, userID uint, username string, tenantID uint) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	idStr := uintString(sessionID)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/ai/sessions/"+idStr+"/attachments", nil)
	c.Params = gin.Params{{Key: "id", Value: idStr}}
	c.Set("user", username)
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", userID)
	c.Set("tenant_id", tenantID)
	return c, w
}

// TestAIAttachmentsAreOwnerScoped pins an IDOR the handler prevents only implicitly: the
// list query filters by session_id alone, and the session itself is authorized by
// (owner_id, tenant_id). A same-tenant colleague must still not read another operator's
// chat attachments - a chat session is private, and the attachments may contain anything the
// operator pasted.
func TestAIAttachmentsAreOwnerScoped(t *testing.T) {
	s := mustTenantServer(t)
	forgecrypto.InitLootEncryption(testStorageKeyHex)
	owner := seedAIUser(t, s, "owner1", 1)
	colleague := seedAIUser(t, s, "colleague1", 1)

	session := db.AIChatSession{
		OwnerID: owner.ID, Owner: "owner1", TenantID: 1,
		Title: "private chat", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := s.db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	attachment := db.AIAttachment{
		ID: "att-1", TenantID: 1, OwnerID: owner.ID,
		Filename: "secret.txt", MediaType: "text/plain", Size: 5,
		Content: "secret", ContentHash: "deadbeef", CreatedAt: time.Now(),
	}
	attachment.SessionID = session.ID
	if err := s.db.Create(&attachment).Error; err != nil {
		t.Fatal(err)
	}

	// The owner sees it.
	c, w := attachmentCtx(session.ID, owner.ID, "owner1", 1)
	s.handleAIAttachmentsList(c)
	if w.Code != http.StatusOK {
		t.Fatalf("owner list = %d (%s)", w.Code, w.Body.String())
	}
	if !containsAny(w.Body.String(), "secret.txt") {
		t.Fatalf("the owner cannot see their own attachment: %s", w.Body.String())
	}

	// A colleague in the same tenant must not.
	c, w = attachmentCtx(session.ID, colleague.ID, "colleague1", 1)
	s.handleAIAttachmentsList(c)
	if w.Code == http.StatusOK && containsAny(w.Body.String(), "secret.txt") {
		t.Fatalf("a colleague read another operator's attachment: %s", w.Body.String())
	}

	// Nor must someone from another tenant.
	outsider := seedAIUser(t, s, "outsider1", 2)
	c, w = attachmentCtx(session.ID, outsider.ID, "outsider1", 2)
	s.handleAIAttachmentsList(c)
	if w.Code == http.StatusOK && containsAny(w.Body.String(), "secret.txt") {
		t.Fatalf("another tenant read the attachment: %s", w.Body.String())
	}
}
