package server

import (
	"os"
	"sort"
	"strings"
	"testing"
)

// TestRetryDocMatchesTheAllowlist keeps docs/TASK_RETRIES.md true to the code.
//
// The page tells an operator which task types can be re-issued automatically, and that list is a code constant. A document that
// drifts from it is worse than none: it is read while deciding whether a command is safe to run, and being wrong in either
// direction is expensive - either "I thought that would be retried" or "I did not know that would be".
func TestRetryDocMatchesTheAllowlist(t *testing.T) {
	raw, err := os.ReadFile("../../docs/TASK_RETRIES.md")
	if err != nil {
		t.Fatalf("the retry document is missing: %v", err)
	}
	doc := string(raw)

	// 1. Every retryable type is named in the document.
	missing := make([]string, 0, 4)
	for taskType := range autoRetryableTaskTypes {
		if !strings.Contains(doc, "`"+taskType+"`") {
			missing = append(missing, taskType)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("docs/TASK_RETRIES.md does not name: %s", strings.Join(missing, ", "))
	}

	// 2. The document does not promise a retry for a type the code refuses - the expensive direction.
	for _, taskType := range []string{"shell", "powershell", "persist", "credential_dump", "upload", "screenshot"} {
		if autoRetryable(taskType) {
			t.Fatalf("%q became retryable; update the document deliberately", taskType)
		}
		// The prohibition is stated in prose; the type must appear only in the "never" sense. A cheap check: the document
		// must not list it as a table entry.
		if strings.Contains(doc, "| `"+taskType+"` |") {
			t.Fatalf("the document lists %q as qualifying", taskType)
		}
	}

	// 3. The two properties an operator relies on are stated, not implied.
	for _, needle := range []string{"once", "changes state"} {
		if !strings.Contains(doc, needle) {
			t.Fatalf("the document does not state %q", needle)
		}
	}

	// 4. And the places a retry is visible are named, so the page is a map rather than a rule.
	for _, needle := range []string{"retry_count", "task_auto_retry", "retried"} {
		if !strings.Contains(doc, needle) {
			t.Fatalf("the document does not mention %q", needle)
		}
	}
}
