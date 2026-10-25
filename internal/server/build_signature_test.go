package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestBuildStatusIncludesSignatureFields(t *testing.T) {
	s := newBuildTestServer(t)
	job := &BuildJob{
		ID:              "build-sig-1",
		Status:          "completed",
		Phase:           "completed",
		Platform:        "windows",
		Format:          "exe",
		CompletedAt:     time.Now(),
		SignatureStatus: "untrusted_root",
		SignatureSigner: "CN=Test Signer",
		SignatureIssuer: "CN=Test Signer",
		SignatureError:  "x509: certificate signed by unknown authority",
	}
	s.buildJobs[job.ID] = job

	router := gin.New()
	router.GET("/generate/builds/:id", s.handleBuildStatus)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/generate/builds/"+job.ID, nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	unmarshalData(t, rec.Body.Bytes(), &got)
	if got["signature_status"] != "untrusted_root" {
		t.Fatalf("signature_status = %v", got["signature_status"])
	}
	if got["signature_signer"] != "CN=Test Signer" {
		t.Fatalf("signature_signer = %v", got["signature_signer"])
	}
	if got["signature_error"] == "" {
		t.Fatal("signature_error missing")
	}

	snapshots := s.buildJobSnapshots()
	if len(snapshots) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(snapshots))
	}
	if snapshots[0]["signature_status"] != "untrusted_root" {
		t.Fatalf("snapshot signature_status = %v", snapshots[0]["signature_status"])
	}
}

func TestInspectBuildSignatureSkipsNonDirectOutputs(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "payload.zip")
	if err := os.WriteFile(zipPath, []byte("PK\x03\x04"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, ok := inspectBuildSignature("windows", "exe", zipPath); ok {
		t.Fatal("ZIP bundle must not be inspected as a PE")
	}
	if _, ok := inspectBuildSignature("linux", "elf", zipPath); ok {
		t.Fatal("non-Windows output must not be inspected")
	}

	exePath := filepath.Join(dir, "payload.exe")
	if err := os.WriteFile(exePath, []byte("not a PE"), 0644); err != nil {
		t.Fatal(err)
	}
	info, ok := inspectBuildSignature("windows", "exe", exePath)
	if !ok {
		t.Fatal("direct EXE output should be inspected")
	}
	if info.Status != "invalid" {
		t.Fatalf("malformed EXE status = %q, want invalid", info.Status)
	}
}
