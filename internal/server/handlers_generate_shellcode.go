package server

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/forgec2/forgec2/internal/payload"
	"github.com/gin-gonic/gin"
)

var shellcodeNameRe = regexp.MustCompile(`^[a-zA-Z0-9._\[\]]*$`)

func (s *Server) handleGenerateDonut(c *gin.Context) {
	file, err := c.FormFile("assembly")
	if err != nil {
		respondError(c, http.StatusBadRequest, "assembly file required")
		return
	}
	f, err := file.Open()
	if err != nil {
		// An upload that could not be read is not a build failure and not an internal state leak: say which half failed, and keep the
		// raw error in the log where it can be correlated.
		slog.Error("Donut upload could not be opened", "err", err)
		respondError(c, http.StatusInternalServerError, "could not read the uploaded assembly file")
		return
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, MaxUploadSize))
	if err != nil {
		slog.Error("Donut upload could not be read", "err", err)
		respondError(c, http.StatusInternalServerError, "could not read the uploaded assembly file")
		return
	}

	className := c.PostForm("class")
	if className != "" && !shellcodeNameRe.MatchString(className) {
		respondError(c, http.StatusBadRequest, "Invalid class name")
		return
	}
	methodName := c.PostForm("method")
	if methodName != "" && !shellcodeNameRe.MatchString(methodName) {
		respondError(c, http.StatusBadRequest, "Invalid method name")
		return
	}
	args := strings.NewReplacer(
		";", "", "&", "", "|", "", "`", "",
	).Replace(c.PostForm("args"))

	cfg := payload.DonutConfig{
		Assembly:   data,
		ClassName:  className,
		MethodName: methodName,
		Args:       args,
		Arch:       c.DefaultPostForm("arch", "amd64"),
		Entropy:    3,
	}

	sc, err := s.withBuildSlot(func() (string, error) {
		out, err := payload.GenerateDonutShellcode(cfg)
		if err != nil {
			return "", err
		}
		return string(out), nil
	})
	if err != nil {
		// The toolchain output is the diagnosis; see build_failure.go for why the sanitizer is the wrong helper here.
		respondError(c, http.StatusInternalServerError, buildFailureText(err))
		return
	}

	outName := sanitizeFilename(c.DefaultPostForm("filename", "loader.bin"))
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, outName))
	c.Data(http.StatusOK, "application/octet-stream", []byte(sc))
}

func (s *Server) handleGenerateShellcode(c *gin.Context) {
	cmd := c.PostForm("command")
	if cmd == "" {
		respondError(c, http.StatusBadRequest, "command required")
		return
	}

	sc, err := payload.GenerateBasicShellcode(cmd)
	if err != nil {
		respondError(c, http.StatusInternalServerError, buildFailureText(err))
		return
	}

	outName := sanitizeFilename(c.DefaultPostForm("filename", "shellcode.bin"))
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, outName))
	c.Data(http.StatusOK, "application/octet-stream", sc)
}
