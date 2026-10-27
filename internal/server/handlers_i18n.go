package server

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

func (s *Server) handleSetLanguage(c *gin.Context) {
	lang := c.PostForm("lang")
	if lang == "" {
		lang = c.Query("lang")
	}
	if lang == "" {
		respondError(c, http.StatusBadRequest, "Language code is required")
		return
	}

	if !IsLanguageSupported(lang) {
		respondError(c, http.StatusBadRequest, "Unsupported language")
		return
	}

	middleware.SetCookieWithSameSite(c, "forgec2_lang", lang, LangCookieMaxAgeSec, "/", middleware.CookieSecure, true, http.SameSiteLaxMode)

	referer := c.GetHeader("Referer")
	if isSafeLocalRedirect(referer) {
		c.Redirect(http.StatusFound, referer)
		return
	}

	respond(c, gin.H{
		"language": lang,
		"message":  "Language updated",
	})
}

func (s *Server) handleTranslationsPage(c *gin.Context) {
	s.renderPageOrJSON(c, gin.H{
		"Title":     "Translation Management",
		"ActiveNav": "translations",
	})
}

func (s *Server) handleDocsPage(c *gin.Context) {
	c.Redirect(http.StatusFound, "/api/docs/")
}

func (s *Server) handleGetTranslations(c *gin.Context) {
	lang := c.Query("lang")
	if lang == "" {
		lang = detectLanguage(c)
	}

	translations, err := ExportTranslations(lang)
	if err != nil {
		respondError(c, http.StatusBadRequest, sanitizeError(err, "Settings save"))
		return
	}

	respond(c, gin.H{
		"language":     lang,
		"translations": translations,
		"count":        len(translations),
	})
}

func (s *Server) handleTranslationStats(c *gin.Context) {
	stats := GetTranslationStats()

	missing := make(map[string][]string)
	for lang := range SupportedLanguages {
		missing[lang] = GetMissingTranslations(lang)
	}

	allKeys := GetAllTranslationKeys()

	respond(c, gin.H{
		"languages":  SupportedLanguages,
		"stats":      stats,
		"total_keys": len(allKeys),
		"missing":    missing,
	})
}

func (s *Server) handleTranslationCheck(c *gin.Context) {
	lang := c.Query("lang")
	if lang == "" {
		lang = detectLanguage(c)
	}

	missing := GetMissingTranslations(lang)
	placeholderIssues := CheckPlaceholderConsistency(DefaultLanguage, lang)
	htmlIssues := CheckHTMLTags(lang)

	respond(c, gin.H{
		"language":             lang,
		"missing_translations": missing,
		"missing_count":        len(missing),
		"placeholder_issues":   placeholderIssues,
		"html_tag_issues":      htmlIssues,
	})
}

// isSafeLocalRedirect accepts only a path on this server.
//
// The previous check was "starts with / and not with //", which a backslash walks around:
// browsers normalise "\" to "/" in a Location, so "/\evil.com" is fetched as "//evil.com"
// and lands on another host. A control character would let the header be split. This runs on
// the unauthenticated /lang/set route, so it is reachable without a session.
func isSafeLocalRedirect(ref string) bool {
	if ref == "" || !strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "//") {
		return false
	}
	if strings.ContainsAny(ref, "\\\r\n\t") {
		return false
	}
	u, err := url.Parse(ref)
	if err != nil {
		return false
	}
	// A scheme or an authority means the reference is not a local path.
	return u.Scheme == "" && u.Host == ""
}
