package server

import (
	"path/filepath"
)

// certsRootDir resolves <data_dir>/certs and refuses to guess.
//
// Same rule as the other write targets (payloads, uploads, build output): a directory written
// by the server must never be relative to the process working directory. The ACME handler
// stores a TLS PRIVATE KEY under this path, so a misconfigured data_dir would have left a key
// somewhere nobody backs up, protects or cleans - and filepath.Join("", "certs", domain) is a
// valid relative path, so the traversal guard in the handler passed it happily.
func (s *Server) certsRootDir() (string, error) {
	dir := s.rawDataDir()
	if dir == "" {
		return "", errNoDataDir
	}
	return filepath.Join(dir, "certs"), nil
}
