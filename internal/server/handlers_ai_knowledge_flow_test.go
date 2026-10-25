package server

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// knowledgeTestServer is the run-storage harness plus an admin operator (user 1,
// tenant 1) and the FTS index the knowledge endpoints expect.
func knowledgeTestServer(t *testing.T) *Server {
	t.Helper()
	s := setupAIRunStorageTest(t)
	if err := s.db.Create(&db.User{Username: "alice", Role: db.RoleAdmin, TenantID: 1, IsActive: true}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	s.initializeAIKnowledgeIndex()
	return s
}

func seedChatSession(t *testing.T, s *Server, title string) db.AIChatSession {
	t.Helper()
	session := db.AIChatSession{TenantID: 1, OwnerID: 1, Owner: "alice", Title: title}
	if err := s.db.Create(&session).Error; err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return session
}

// knowledgeCtx builds a context the AI handlers accept: an authenticated admin
// operator plus whatever params/body the handler needs.
func knowledgeCtx(method, target, body string) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", "alice")
	c.Set("user_role", db.RoleAdmin)
	return c, w
}

func uploadBody(t *testing.T, names []string, content string) (*bytes.Buffer, string) {
	t.Helper()
	buf := new(bytes.Buffer)
	writer := multipart.NewWriter(buf)
	for _, name := range names {
		part, err := writer.CreateFormFile("files", name)
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			t.Fatalf("write part: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return buf, writer.FormDataContentType()
}

func uploadAttachment(t *testing.T, s *Server, sessionID uint, name, content string) db.AIAttachment {
	t.Helper()
	buf, ct := uploadBody(t, []string{name}, content)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/ai/sessions/"+jsonNumber(sessionID)+"/attachments", buf)
	c.Request.Header.Set("Content-Type", ct)
	c.Set("user", "alice")
	c.Set("user_role", db.RoleAdmin)
	c.Params = gin.Params{{Key: "id", Value: jsonNumber(sessionID)}}
	s.handleAIAttachmentsUpload(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("upload %s = %d %s", name, w.Code, w.Body.String())
	}
	var created []db.AIAttachment
	unmarshalData(t, w.Body.Bytes(), &created)
	if len(created) != 1 {
		t.Fatalf("upload returned %d attachments", len(created))
	}
	return created[0]
}

func TestAIAttachmentsUploadListDelete(t *testing.T) {
	s := knowledgeTestServer(t)
	session := seedChatSession(t, s, "attachments")

	first := uploadAttachment(t, s, session.ID, "notes.txt", "The domain admin password rotates on Fridays.")
	second := uploadAttachment(t, s, session.ID, "hosts.txt", "dc01.corp.example resolves to 10.0.0.5")
	if first.ContentHash == "" || second.ContentHash == first.ContentHash {
		t.Fatalf("hashes not distinct: %q / %q", first.ContentHash, second.ContentHash)
	}

	// The stored row keeps the bytes; the response must not echo them back.
	var stored db.AIAttachment
	if err := s.db.Where("id = ?", first.ID).First(&stored).Error; err != nil {
		t.Fatalf("reload attachment: %v", err)
	}
	if !strings.Contains(stored.Content, "domain admin") {
		t.Fatalf("content not persisted: %q", stored.Content)
	}

	c, w := knowledgeCtx(http.MethodGet, "/api/ai/sessions/x/attachments", "")
	c.Params = gin.Params{{Key: "id", Value: jsonNumber(session.ID)}}
	s.handleAIAttachmentsList(c)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d %s", w.Code, w.Body.String())
	}
	var listed []struct {
		Filename string `json:"filename"`
		Content  string `json:"content"`
	}
	unmarshalData(t, w.Body.Bytes(), &listed)
	if len(listed) != 2 {
		t.Fatalf("listed %d attachments, want 2", len(listed))
	}
	for _, a := range listed {
		if a.Content != "" {
			t.Fatalf("list leaked attachment bytes for %s", a.Filename)
		}
	}

	// Deleting is owner+tenant scoped and idempotent-safe (second call is 404).
	c, w = knowledgeCtx(http.MethodDelete, "/api/ai/attachments/x", "")
	c.Params = gin.Params{{Key: "attachmentID", Value: first.ID}}
	s.handleAIAttachmentDelete(c)
	if w.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", w.Code, w.Body.String())
	}
	c, w = knowledgeCtx(http.MethodDelete, "/api/ai/attachments/x", "")
	c.Params = gin.Params{{Key: "attachmentID", Value: first.ID}}
	s.handleAIAttachmentDelete(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d", w.Code)
	}

	// An unknown session is a 404, not a 500 or an empty list.
	c, w = knowledgeCtx(http.MethodGet, "/api/ai/sessions/999999/attachments", "")
	c.Params = gin.Params{{Key: "id", Value: "999999"}}
	s.handleAIAttachmentsList(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown session list = %d", w.Code)
	}

	// Upload limits: zero files, and more than the per-request cap.
	buf, ct := uploadBody(t, nil, "")
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/x", buf)
	c.Request.Header.Set("Content-Type", ct)
	c.Set("user", "alice")
	c.Params = gin.Params{{Key: "id", Value: jsonNumber(session.ID)}}
	s.handleAIAttachmentsUpload(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty upload = %d", w.Code)
	}

	tooMany := []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt", "f.txt"}
	buf, ct = uploadBody(t, tooMany, "x")
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/x", buf)
	c.Request.Header.Set("Content-Type", ct)
	c.Set("user", "alice")
	c.Params = gin.Params{{Key: "id", Value: jsonNumber(session.ID)}}
	s.handleAIAttachmentsUpload(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("too many files = %d %s", w.Code, w.Body.String())
	}
}

func TestAIKnowledgeCollectionsCRUD(t *testing.T) {
	s := knowledgeTestServer(t)
	if err := s.db.Create(&db.User{Username: "bob", Role: "analyst", TenantID: 1, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}

	c, w := knowledgeCtx(http.MethodGet, "/api/ai/knowledge/collections", "")
	s.handleAIKnowledgeCollectionsList(c)
	if w.Code != http.StatusOK {
		t.Fatalf("empty list = %d %s", w.Code, w.Body.String())
	}
	if strings.TrimSpace(w.Body.String()) == "" {
		t.Fatal("empty list returned no body")
	}

	c, w = knowledgeCtx(http.MethodPost, "/api/ai/knowledge/collections", `{"name":"  Engagement Alpha  "}`)
	s.handleAIKnowledgeCollectionCreate(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	var created db.AIKnowledgeCollection
	unmarshalData(t, w.Body.Bytes(), &created)
	if created.Name != "Engagement Alpha" || created.OwnerID != 1 || created.Shared {
		t.Fatalf("created = %+v", created)
	}

	// Blank and over-long names are refused.
	for _, body := range []string{`{"name":"   "}`, `{"name":"` + strings.Repeat("n", 161) + `"}`} {
		c, w = knowledgeCtx(http.MethodPost, "/api/ai/knowledge/collections", body)
		s.handleAIKnowledgeCollectionCreate(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid name = %d", w.Code)
		}
	}

	// A shared collection needs knowledge-manage permission; a plain analyst
	// asking for one is refused.
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"name":"team","shared":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", "bob")
	s.handleAIKnowledgeCollectionCreate(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("shared without permission = %d %s", w.Code, w.Body.String())
	}

	// The owner sees it; another tenant's collection stays invisible.
	if err := s.db.Create(&db.AIKnowledgeCollection{TenantID: 2, OwnerID: 9, Name: "foreign"}).Error; err != nil {
		t.Fatal(err)
	}
	c, w = knowledgeCtx(http.MethodGet, "/api/ai/knowledge/collections", "")
	s.handleAIKnowledgeCollectionsList(c)
	if strings.Contains(w.Body.String(), "foreign") {
		t.Fatal("cross-tenant collection leaked into the list")
	}
	if !strings.Contains(w.Body.String(), "Engagement Alpha") {
		t.Fatalf("own collection missing: %s", w.Body.String())
	}

	c, w = knowledgeCtx(http.MethodDelete, "/api/ai/knowledge/collections/x", "")
	c.Params = gin.Params{{Key: "collectionID", Value: jsonNumber(created.ID)}}
	s.handleAIKnowledgeCollectionDelete(c)
	if w.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", w.Code, w.Body.String())
	}
}

func TestAIKnowledgePromoteSourcesAndSearch(t *testing.T) {
	s := knowledgeTestServer(t)
	session := seedChatSession(t, s, "promote")
	attachment := uploadAttachment(t, s, session.ID,
		"engagement.txt",
		"Domain admin uses the password from the vault. DC01 runs Windows Server. "+
			"Operator notes: the VPN gateway is reachable from the DMZ jump host.")

	collection := db.AIKnowledgeCollection{TenantID: 1, OwnerID: 1, Name: "engagement"}
	if err := s.db.Create(&collection).Error; err != nil {
		t.Fatal(err)
	}

	c, w := knowledgeCtx(http.MethodPost, "/api/ai/knowledge/collections/x/sources", "")
	c.Params = gin.Params{
		{Key: "collectionID", Value: jsonNumber(collection.ID)},
		{Key: "attachmentID", Value: attachment.ID},
	}
	s.handleAIKnowledgePromoteAttachment(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("promote = %d %s", w.Code, w.Body.String())
	}
	var source db.AIKnowledgeSource
	unmarshalData(t, w.Body.Bytes(), &source)
	if source.ChunkCount == 0 || source.ContentHash != attachment.ContentHash {
		t.Fatalf("source = %+v", source)
	}

	// Promoting the same bytes twice is a conflict, not a duplicate row.
	c, w = knowledgeCtx(http.MethodPost, "/api/ai/knowledge/collections/x/sources", "")
	c.Params = gin.Params{
		{Key: "collectionID", Value: jsonNumber(collection.ID)},
		{Key: "attachmentID", Value: attachment.ID},
	}
	s.handleAIKnowledgePromoteAttachment(c)
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate promote = %d %s", w.Code, w.Body.String())
	}

	// Sources list and delete.
	c, w = knowledgeCtx(http.MethodGet, "/api/ai/knowledge/collections/x/sources", "")
	c.Params = gin.Params{{Key: "collectionID", Value: jsonNumber(collection.ID)}}
	s.handleAIKnowledgeSourcesList(c)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "engagement.txt") {
		t.Fatalf("sources = %d %s", w.Code, w.Body.String())
	}

	// Search requires an explicit collection selection (nothing is searchable
	// unless the operator picked it), then finds the promoted text and returns
	// the chunk, not the whole file.
	searchBody := `{"query":"vault password","collection_ids":[` + jsonNumber(collection.ID) + `],"limit":5}`
	c, w = knowledgeCtx(http.MethodPost, "/api/ai/knowledge/search", searchBody)
	s.handleAIKnowledgeSearch(c)
	if w.Code != http.StatusOK {
		t.Fatalf("search = %d %s", w.Code, w.Body.String())
	}
	var results []struct {
		Source  string `json:"source"`
		Content string `json:"content"`
	}
	unmarshalData(t, w.Body.Bytes(), &results)
	if len(results) == 0 || results[0].Source != "engagement.txt" {
		t.Fatalf("search results = %+v", results)
	}
	// No selection ⇒ no results, even with a perfect query.
	c, w = knowledgeCtx(http.MethodPost, "/api/ai/knowledge/search", `{"query":"vault password"}`)
	s.handleAIKnowledgeSearch(c)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "engagement.txt") {
		t.Fatalf("unscoped search leaked results: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(results[0].Content, "\x00") {
		t.Fatal("search result content is corrupted")
	}

	// The tool variant filters by the run's selected collections and refuses a
	// missing principal.
	ctx := &aiReqCtx{
		Principal:              aiPrincipal{UserID: 1, Username: "alice", TenantID: 1, Role: db.RoleAdmin},
		KnowledgeCollectionIDs: []uint{collection.ID},
	}
	if out := s.executeAIKnowledgeSearchTool(nil, `{"query":"vault"}`); !strings.Contains(out, "principal required") {
		t.Fatalf("tool without principal = %s", out)
	}
	out := s.executeAIKnowledgeSearchTool(ctx, `{"query":"vault","collection_ids":[`+jsonNumber(collection.ID)+`]}`)
	if strings.Contains(out, "error") {
		t.Fatalf("tool search = %s", out)
	}
	if !strings.Contains(out, "engagement.txt") {
		t.Fatalf("tool search found nothing: %s", out)
	}
	// A run scoped to a different collection filters this one out.
	ctx.KnowledgeCollectionIDs = []uint{collection.ID + 100}
	out = s.executeAIKnowledgeSearchTool(ctx, `{"query":"vault","collection_ids":[`+jsonNumber(collection.ID)+`]}`)
	if strings.Contains(out, "engagement.txt") {
		t.Fatalf("collection scope was not applied: %s", out)
	}

	c, w = knowledgeCtx(http.MethodDelete, "/api/ai/knowledge/collections/x/sources/y", "")
	c.Params = gin.Params{
		{Key: "collectionID", Value: jsonNumber(collection.ID)},
		{Key: "sourceID", Value: jsonNumber(source.ID)},
	}
	s.handleAIKnowledgeSourceDelete(c)
	if w.Code != http.StatusOK {
		t.Fatalf("source delete = %d %s", w.Code, w.Body.String())
	}
}

func TestTruncateAIRunes(t *testing.T) {
	if got := truncateAIRunes("short", 10); got != "short" {
		t.Fatalf("short value changed: %q", got)
	}
	got := truncateAIRunes("abcdefghij", 4)
	if !strings.HasPrefix(got, "abcd") || !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated value = %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncation produced invalid UTF-8: %q", got)
	}
}
