package server

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// Token-based revalidation has ONE implementation in this package.
//
// Rounds 3 and 4 added conditional requests to the audit export and the task CSV export, and each grew
// its own copy of the "parse If-None-Match and compare tags" loop - on top of `etagMatches`, which had
// been serving the static-asset middleware since the console got immutable asset caching. Three copies
// of a comparison is the same shape that produced `guardedClient`: rounds 232/233 fixed a zero-timeout
// hole in one of two copies of an HTTP client wrapper, and the second copy kept the bug. A comparison
// looks too small to be worth sharing, which is exactly why the copies appear.
//
// `etagMatches` is the canonical function (the static middleware already calls it); `ifNoneMatch` is the
// gin-context convenience used by the handlers.

// etagMatches reports whether an If-None-Match header selects etag.
//
// Handles weak validators on either side and the `*` wildcard (RFC 9110 section 13.1.2), and compares
// list members after trimming - never after morphing them, because `"a, b"` is two tags and `"a,b"` is
// one.
func etagMatches(header, etag string) bool {
	if header == "" || etag == "" {
		return false
	}
	want := strings.TrimPrefix(etag, "W/")
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "*" {
			return true
		}
		if strings.TrimPrefix(part, "W/") == want {
			return true
		}
	}
	return false
}

// ifNoneMatch answers the handler-side question using the canonical comparison.
func ifNoneMatch(c *gin.Context, etag string) bool {
	if c == nil {
		return false
	}
	return etagMatches(strings.TrimSpace(c.GetHeader("If-None-Match")), etag)
}

// weakETag renders an opaque tag value as a weak ETag, escaping anything that would break the syntax (a
// quote, a comma, a space) instead of emitting an invalid header.
func weakETag(body string) string {
	clean := strings.Map(func(r rune) rune {
		switch r {
		case '"', ',', ' ', '\\':
			return '_'
		}
		return r
	}, body)
	return `W/"` + clean + `"`
}
