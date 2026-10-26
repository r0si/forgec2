package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestWriteSSE(t *testing.T) {
	s := mustTenantServer(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var flusher http.Flusher = w
	s.writeSSE(c, flusher, "delta", "line one\nline two")
	got := w.Body.String()
	want := "event: delta\ndata: line one\ndata: line two\n\n"
	if got != want {
		t.Fatalf("multi-line frame = %q, want %q", got, want)
	}

	// A nil flusher must not panic (the SSE loop passes one when available).
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	s.writeSSE(c2, nil, "done", "{}")
	if !strings.HasPrefix(w2.Body.String(), "event: done\n") {
		t.Fatalf("nil flusher frame = %q", w2.Body.String())
	}
}

func TestEnsureDirWritable(t *testing.T) {
	dir := t.TempDir()
	if err := ensureDirWritable(dir); err != nil {
		t.Fatalf("writable dir = %v", err)
	}
	// The probe file must not be left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".probe-") {
			t.Fatalf("probe file left behind: %s", e.Name())
		}
	}

	// A missing directory is an error, not a silent success.
	missing := filepath.Join(dir, "nope", "deeper")
	if err := ensureDirWritable(missing); err == nil || !strings.Contains(err.Error(), "not writable") {
		t.Fatalf("missing dir = %v", err)
	}

	// A regular file is not a directory and must be reported the same way.
	file := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureDirWritable(file); err == nil {
		t.Fatal("file path reported as a writable directory")
	}
}

func TestInitUpdateCheckerStopsOnCancel(t *testing.T) {
	s := mustTenantServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx
	s.initUpdateChecker()
	// Cancelling must let the poller exit instead of parking on the initial
	// delay; wg.Wait returning is the observable proof.
	cancel()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("update checker did not stop after cancel")
	}
}
