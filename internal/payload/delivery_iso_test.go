package payload

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestISOBuildersValidateEntryNames pins the fix for malformed images: both builders upper-cased
// whatever they were handed and appended ";1" with no length or character check, so a 300-character
// name, a name containing a separator, or a non-ASCII one produced an ISO 9660 directory record that
// no client could read - the operator shipped a broken image to a target and only found out when it
// would not open.
func TestISOBuildersValidateEntryNames(t *testing.T) {
	bad := []string{
		"a/../../etc/passwd",
		"na me.txt",
		"name;1;2",
		"résumé.pdf",
		"a\\b.exe",
		// 65 characters is over the cap.
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA.txt",
	}
	for _, name := range bad {
		if _, err := BuildISO9660(name, []byte("x")); err == nil {
			t.Fatalf("BuildISO9660(%q) accepted an invalid name", name)
		}
		if _, err := BuildISOWithLNK("Report.pdf.lnk", name, []byte("x")); err == nil {
			t.Fatalf("BuildISOWithLNK(%q) accepted an invalid exe name", name)
		}
	}

	// A valid name still builds, and the directory record carries it upper-cased with ";1".
	img, err := BuildISO9660("report.txt", []byte("hello"))
	if err != nil {
		t.Fatalf("valid name refused: %v", err)
	}
	if !bytes.Contains(img, []byte("REPORT.TXT;1")) {
		t.Fatal("the ISO does not contain the normalized entry name")
	}
	// The image must be a whole number of 2048-byte sectors, or a client rejects it.
	if len(img)%2048 != 0 {
		t.Fatalf("image size %d is not a multiple of the sector size", len(img))
	}
	// The volume descriptor must still declare the ISO 9660 magic at sector 16.
	pvd := img[16*2048 : 17*2048]
	if pvd[0] != 0x01 || string(pvd[1:6]) != "CD001" {
		t.Fatalf("bad primary volume descriptor: % X", pvd[:6])
	}
	if binary.LittleEndian.Uint16(pvd[128:130]) != 2048 {
		t.Fatal("volume descriptor lost the logical block size")
	}
}
