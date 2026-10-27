package server

import (
	"errors"
	"strings"
	"testing"

	forgecrypto "github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
)

// TestEncryptTaskFieldsOrBlank pins the fail-closed contract for task output:
// when the vault is available the fields are encrypted; when it is not, the
// output is DROPPED and replaced with a marker. Storing command output as
// plaintext because encryption happened to be unavailable is the one outcome
// this must never allow.
func TestEncryptTaskFieldsOrBlank(t *testing.T) {
	s := mustTenantServer(t)

	// Vault available.
	forgecrypto.InitLootEncryption(testStorageKeyHex)
	task := &db.Task{ID: 1, AgentID: "a1", Status: "completed", Result: "lsass dump bytes", Error: "boom detail"}
	if err := s.encryptTaskFieldsOrBlank(task); err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !strings.HasPrefix(task.Result, "FC2ENC:") {
		t.Fatalf("result was not encrypted: %q", task.Result)
	}
	if !strings.HasPrefix(task.Error, "FC2ENC:") {
		t.Fatalf("error was not encrypted: %q", task.Error)
	}

	// Vault unavailable: output is dropped, the marker is set, the error is
	// returned so the caller fails closed too. The vault cannot be un-initialised
	// (InitLootEncryption keeps an existing key so stored loot stays readable),
	// so this swaps the single vault call instead.
	encryptTaskFieldsFn = func(*db.Task) error { return errors.New("vault encryption unavailable") }
	t.Cleanup(func() {
		encryptTaskFieldsFn = func(t *db.Task) error { return t.EncryptTaskFields() }
	})

	plain := &db.Task{ID: 2, AgentID: "a1", Status: "completed", Result: "super secret", Error: "secret error"}
	err := s.encryptTaskFieldsOrBlank(plain)
	if err == nil {
		t.Fatal("encryptTaskFieldsOrBlank reported success without a vault")
	}
	if plain.Result != "" {
		t.Fatalf("result kept without a vault: %q", plain.Result)
	}
	if !strings.Contains(plain.Error, "vault") {
		t.Fatalf("error marker missing: %q", plain.Error)
	}
	if strings.Contains(plain.Result, "super secret") || strings.Contains(plain.Error, "secret error") {
		t.Fatal("plaintext survived the fail-closed path")
	}

	// With the real vault restored, encryption works again.
	encryptTaskFieldsFn = func(t *db.Task) error { return t.EncryptTaskFields() }
	again := &db.Task{ID: 3, AgentID: "a1", Result: "second secret"}
	if err := s.encryptTaskFieldsOrBlank(again); err != nil {
		t.Fatalf("re-encrypt after restore: %v", err)
	}
	if !strings.HasPrefix(again.Result, "FC2ENC:") {
		t.Fatalf("restored vault did not encrypt: %q", again.Result)
	}
}

// TestVaultNoteError covers the metrics hook: it must not panic without a
// metrics registry, and it must always log.
func TestVaultNoteError(t *testing.T) {
	s := mustTenantServer(t)
	s.metrics = nil // the common case in tests and in minimal deployments
	s.vaultNoteError("encrypt-result", errors.New("no vault"), 7, "a1")
}

// TestAppendTaskResultTail pins the streaming tail cap: the newest bytes are
// kept and the buffer never exceeds the cap.
func TestAppendTaskResultTail(t *testing.T) {
	task := &db.Task{}
	appendTaskResultTail(task, "abc")
	if task.Result != "abc" {
		t.Fatalf("result = %q", task.Result)
	}
	appendTaskResultTail(task, "def")
	if task.Result != "abcdef" {
		t.Fatalf("result = %q", task.Result)
	}

	// Push past the cap: the tail is kept verbatim, the head dropped.
	big := strings.Repeat("x", taskResultTailCap)
	appendTaskResultTail(task, big)
	if len(task.Result) != taskResultTailCap {
		t.Fatalf("tail length = %d, want %d", len(task.Result), taskResultTailCap)
	}
	if !strings.HasSuffix(task.Result, "xxxx") {
		t.Fatal("the newest bytes were not kept")
	}
}
