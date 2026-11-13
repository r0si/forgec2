package server

import (
	"path/filepath"
)

// uploadsRootDir resolves <data_dir>/uploads and refuses to guess.
//
// Same defect class as the hosted payloads: rawDataDir() returns "" when
// server.data_dir is empty, and filepath.Join("", "uploads") is the RELATIVE path
// "uploads" - so agent-exfiltrated files, which are writes, landed next to whatever
// directory the process started in. A misconfigured deployment would then have its
// exfiltrated loot scattered in a working directory nobody backs up or cleans.
func (s *Server) uploadsRootDir() (string, error) {
	dir := s.rawDataDir()
	if dir == "" {
		return "", errNoDataDir
	}
	return filepath.Join(dir, "uploads"), nil
}
