package server

import "testing"

// TestValidateIconBytes pins the icon magic check. It used to be written as
// `len(data) >= 4 && !(ico) && !(png)`, so a file shorter than four bytes skipped
// the test entirely and a one-byte "icon" was accepted into the build.
func TestValidateIconBytes(t *testing.T) {
	ico := []byte{0x00, 0x00, 0x01, 0x00, 0x01, 0x00}
	png := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}

	if err := validateIconBytes(ico); err != nil {
		t.Fatalf("a valid ICO was rejected: %v", err)
	}
	if err := validateIconBytes(png); err != nil {
		t.Fatalf("a valid PNG was rejected: %v", err)
	}

	// The short-file hole: anything under four bytes cannot carry a magic number.
	for _, short := range [][]byte{nil, {}, {0x00}, {0x00, 0x00}, {0x89, 0x50, 0x4E}} {
		if err := validateIconBytes(short); err == nil {
			t.Fatalf("a %d-byte icon was accepted", len(short))
		}
	}

	// Wrong magic, and a PNG/ICO signature that is present but not at offset 0.
	for _, bad := range [][]byte{
		[]byte("GIF8"),
		[]byte("MZ\x90\x00"),
		{0x01, 0x00, 0x00, 0x00},
		{0x00, 0x89, 0x50, 0x4E},
	} {
		if err := validateIconBytes(bad); err == nil {
			t.Fatalf("icon with magic % x was accepted", bad)
		}
	}
}
