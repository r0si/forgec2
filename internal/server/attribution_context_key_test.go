package server

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// TestBOFUploadGates pins the two upload gates that the argument-order bug had
// made untestable: a PE disguised as a BOF and a disallowed extension are both
// rejected, while a real COFF object is accepted.
func TestBOFUploadGates(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)

	upload := func(name string, content []byte) int {
		buf := new(bytes.Buffer)
		writer := multipart.NewWriter(buf)
		part, err := writer.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(content); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/bof/upload", buf)
		c.Request.Header.Set("Content-Type", writer.FormDataContentType())
		c.Set("user", "admin")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		s.handleBOFUpload(c)
		return w.Code
	}

	if code := upload("payload.exe", []byte("MZ\x90\x00\x03\x00\x00\x00")); code != http.StatusBadRequest {
		t.Fatalf("exe upload = %d, want 400", code)
	}
	if code := upload("payload.o", []byte("MZ\x90\x00\x03\x00\x00\x00")); code != http.StatusBadRequest {
		t.Fatalf("PE-as-BOF upload = %d, want 400", code)
	}
	if code := upload("payload.o", []byte("\x64\x86\x00\x00\x00\x00\x00\x00")); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("COFF upload = %d, want success", code)
	}
}

// TestBOFUploadRecordsUploader pins the attribution fix: the handlers used to
// read c.GetString("username"), a key no middleware sets, so CreatedBy was
// always empty.
func TestBOFUploadRecordsUploader(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)

	buf := new(bytes.Buffer)
	writer := multipart.NewWriter(buf)
	part, err := writer.CreateFormFile("file", "probe.o")
	if err != nil {
		t.Fatal(err)
	}
	// COFF object header (x64 machine), not MZ/PK: the magic-byte guard rejects
	// executables disguised as BOFs, which is the behaviour under test above.
	if _, err := part.Write([]byte("\x64\x86\x00\x00\x00\x00\x00\x00")); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("description", "uploaded by the test"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/bof/upload", buf)
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	c.Set("user", "admin")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set("tenant_id", uint(1))
	s.handleBOFUpload(c)
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("upload = %d %s", w.Code, w.Body.String())
	}

	var bof db.BOFFile
	if err := s.db.Where("name = ?", "probe.o").First(&bof).Error; err != nil {
		t.Fatalf("bof not persisted: %v", err)
	}
	if bof.CreatedBy != "admin" {
		t.Fatalf("CreatedBy = %q, want the uploader", bof.CreatedBy)
	}
}

// TestScriptSaveRecordsAuthor pins the same fix for the script editor.
func TestScriptSaveRecordsAuthor(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)

	c, w := missingAPICtx(http.MethodPost, "/api/scripts",
		`{"name":"attribution-probe","description":"d","code":"return 1;"}`, nil)
	s.handleAPISaveScript(c)
	if w.Code != http.StatusOK {
		t.Fatalf("save script = %d %s", w.Code, w.Body.String())
	}

	var row db.Script
	if err := s.db.Where("name = ?", "attribution-probe").First(&row).Error; err != nil {
		t.Fatalf("script not persisted: %v", err)
	}
	if row.CreatedBy != "admin" {
		t.Fatalf("CreatedBy = %q, want the author", row.CreatedBy)
	}
}
