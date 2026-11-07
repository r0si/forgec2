package server

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

type DeployManifest struct {
	Version         string    `json:"version"`
	GoVersion       string    `json:"go_version"`
	BuildOS         string    `json:"build_os"`
	BuildArch       string    `json:"build_arch"`
	ConfigHash      string    `json:"config_hash"`
	StartTime       time.Time `json:"start_time"`
	Hostname        string    `json:"hostname"`
	ProtocolVersion uint      `json:"protocol_version"`
}

var (
	deployManifest     *DeployManifest
	deployManifestOnce sync.Once
)

func GenerateDeployManifest(configPath string) *DeployManifest {
	deployManifestOnce.Do(func() {
		hostname, _ := os.Hostname()
		configHash := hashFile(configPath)

		deployManifest = &DeployManifest{
			// Single version source: the ServerVersion ldflag (dev by
			// default). The old hardcoded literal drifted from releases.
			Version:         ServerVersion,
			GoVersion:       runtime.Version(),
			BuildOS:         runtime.GOOS,
			BuildArch:       runtime.GOARCH,
			ConfigHash:      configHash,
			StartTime:       time.Now(),
			Hostname:        hostname,
			ProtocolVersion: 1,
		}

		manifestPath := filepath.Join(filepath.Dir(configPath), "deploy-manifest.json")
		data, err := json.MarshalIndent(deployManifest, "", "  ")
		if err != nil {
			slog.Warn("Failed to marshal deploy manifest", "error", err)
			return
		}
		if err := os.WriteFile(manifestPath, data, 0600); err != nil {
			slog.Warn("Failed to write deploy manifest", "path", manifestPath, "error", err)
			return
		}
		slog.Info("Deploy manifest written", "path", manifestPath, "version", deployManifest.Version, "config_hash", configHash)
	})
	return deployManifest
}

func hashFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "unreadable"
	}
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h[:8])
}
