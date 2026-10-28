package server

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"

	"archive/zip"
)

// bundleWriter is what a bundle member writes through.
//
// It exists so the archive can HASH each member while it is being written: the digests end up in MANIFEST.json, which
// lets a receiver prove that what they unzip is what the server produced, after the bundle has been emailed, copied
// and re-copied. *zip.Writer satisfies this interface, and hashingBundleWriter wraps it.
type bundleWriter interface {
	Create(name string) (io.Writer, error)
}

// hashingBundleWriter records a SHA-256 and a byte count for every member created through it.
type hashingBundleWriter struct {
	inner   *zip.Writer
	digests map[string]string
	bytes   map[string]int
}

func (w *hashingBundleWriter) Create(name string) (io.Writer, error) {
	file, err := w.inner.Create(name)
	if err != nil {
		return nil, err
	}
	h := &memberHasher{inner: file, hash: sha256.New(), name: name, owner: w}
	return h, nil
}

// memberHasher feeds the bytes to both the archive and the digest.
type memberHasher struct {
	inner io.Writer
	hash  hash.Hash
	name  string
	owner *hashingBundleWriter
	bytes int
}

func (h *memberHasher) Write(p []byte) (int, error) {
	written, err := h.inner.Write(p)
	if written > 0 {
		h.hash.Write(p[:written])
		h.bytes += written
	}
	if err == nil {
		h.owner.digests[h.name] = hex.EncodeToString(h.hash.Sum(nil))
		h.owner.bytes[h.name] = h.bytes
	}
	return written, err
}

// digestOf returns the recorded digest and size for a member.
func (w *hashingBundleWriter) digestOf(name string) (string, int) {
	return w.digests[name], w.bytes[name]
}
