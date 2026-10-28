package server

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
)

// The embedded frontend cannot lean on Last-Modified: embed.FS carries no
// mtimes (http.FileServer sees the zero time), so without the precomputed
// metadata below every reload re-downloads the whole shell. Vite
// content-addresses its own bundle under /assets/, but index.html, the
// per-route HTML shells and the hand-vendored /js and /css files keep their
// names across upgrades — those need a content-hash ETag to revalidate
// cheaply, and their text bodies are worth gzipping on the way out.

// staticAssetMeta is the precomputed serving metadata for one embedded file.
type staticAssetMeta struct {
	etag         string
	size         int64
	compressible bool
}

// minCompressBytes skips files where gzip framing costs more than it saves.
const minCompressBytes = 1024

// compressibleStaticAsset reports whether a body is worth gzipping: text
// formats, not the already-compressed woff2/ico/png family.
func compressibleStaticAsset(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".js", ".css", ".html", ".svg", ".json", ".txt", ".xml", ".map":
		return true
	default:
		return false
	}
}

// buildStaticAssetMeta walks the embedded dist once at boot and hashes every
// file. The dist is a few megabytes, so this costs a few milliseconds of
// startup and removes the per-request hashing an ETag would otherwise need.
// A file that cannot be read is simply served without an ETag.
func buildStaticAssetMeta(staticFS fs.FS) map[string]staticAssetMeta {
	meta := make(map[string]staticAssetMeta)
	if staticFS == nil {
		return meta
	}
	_ = fs.WalkDir(staticFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		f, err := staticFS.Open(p)
		if err != nil {
			return nil
		}
		h := sha256.New()
		n, copyErr := io.Copy(h, f)
		_ = f.Close()
		if copyErr != nil {
			return nil
		}
		meta[p] = staticAssetMeta{
			etag:         `"` + hex.EncodeToString(h.Sum(nil)[:16]) + `"`,
			size:         n,
			compressible: compressibleStaticAsset(p),
		}
		return nil
	})
	return meta
}

// resolveStaticAsset mirrors what spaFS.Open will actually serve for a
// request path — the exact file, else the `<dir>.html` sibling, else the SPA
// fallback index.html — so the caching headers describe the bytes on the
// wire, not the URL.
func (s *Server) resolveStaticAsset(p string) (string, staticAssetMeta, bool) {
	name := strings.TrimPrefix(p, "/")
	if name == "" {
		name = "index.html"
	}
	// A malformed path is answered with a 400 by http.FileServer; do not
	// stamp the shell's validators onto that response.
	if strings.Contains(name, "..") || strings.ContainsRune(name, '\\') {
		return "", staticAssetMeta{}, false
	}
	if m, ok := s.staticMeta[name]; ok {
		return name, m, true
	}
	if m, ok := s.staticMeta[name+".html"]; ok {
		return name + ".html", m, true
	}
	if m, ok := s.staticMeta["index.html"]; ok {
		return "index.html", m, true
	}
	return "", staticAssetMeta{}, false
}

// acceptsGzip parses Accept-Encoding well enough to honour an explicit
// `gzip;q=0` refusal (RFC 9110 section 12.5.3) without adding a content
// negotiation dependency for a single token. Anything that is not an
// explicit zero counts as acceptance.
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		token := strings.ToLower(strings.TrimSpace(fields[0]))
		if token != "gzip" && token != "*" {
			continue
		}
		refused := false
		for _, param := range fields[1:] {
			param = strings.TrimSpace(param)
			if !strings.HasPrefix(strings.ToLower(param), "q=") {
				continue
			}
			if q, err := strconv.ParseFloat(strings.TrimSpace(param[2:]), 64); err == nil && q == 0 {
				refused = true
			}
		}
		if !refused {
			return true
		}
	}
	return false
}

// gzipWriterPool recycles gzip writers across requests; each carries a 32KiB
// deflate window plus internal buffers that would otherwise be re-allocated
// on every hit.
var gzipWriterPool = sync.Pool{
	New: func() any { return gzip.NewWriter(io.Discard) },
}

// gzipResponseWriter streams a static body through a pooled gzip writer. The
// identity Content-Length http.ServeContent computed is dropped when the
// stream is compressed (the compressed size is unknown until the trailer is
// written) and Content-Encoding is announced only for a 200 that will
// actually carry a body — never on 304/204/1xx.
type gzipResponseWriter struct {
	http.ResponseWriter
	gz          *gzip.Writer
	wroteHeader bool
	compressing bool
}

func (w *gzipResponseWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	if code == http.StatusOK {
		h := w.Header()
		h.Del("Content-Length")
		h.Set("Content-Encoding", "gzip")
		w.compressing = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.compressing {
		return w.gz.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

// newGzipResponseWriter takes a pooled writer, points it at w and wraps it.
func newGzipResponseWriter(w http.ResponseWriter) *gzipResponseWriter {
	gz := gzipWriterPool.Get().(*gzip.Writer)
	gz.Reset(w)
	return &gzipResponseWriter{ResponseWriter: w, gz: gz}
}

// Close flushes the gzip stream when one was announced and returns the writer
// to the pool. Only a wrapper that announced gzip has bytes pending; closing
// an unused writer would emit a stray empty gzip member.
func (w *gzipResponseWriter) Close() {
	if w.compressing {
		_ = w.gz.Close()
	}
	gzipWriterPool.Put(w.gz)
}
