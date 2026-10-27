package server

import (
	"errors"
	"path/filepath"
)

// errNoDataDir is returned instead of writing somewhere relative to the process working
// directory.
var errNoDataDir = errors.New("server.data_dir is not configured")

// hostedPayloadsDir resolves <data_dir>/payloads and refuses to guess.
//
// rawDataDir() returns "" when server.data_dir is empty, and filepath.Join("", "payloads")
// is the RELATIVE path "payloads" - so hosted payloads, and the sweep that expires them,
// landed next to whatever directory the process happened to start in. In this repository
// that showed up as 372 empty directories inside internal/server/ from tests that build a
// Server with an empty config; on a deployment started from an arbitrary cwd it would put
// payloads somewhere nobody looks and leave cleanupOldPayloads scanning the wrong tree.
func (s *Server) hostedPayloadsDir() (string, error) {
	dir := s.rawDataDir()
	if dir == "" {
		return "", errNoDataDir
	}
	return filepath.Join(dir, "payloads"), nil
}
