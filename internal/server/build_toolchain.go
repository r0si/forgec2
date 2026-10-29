package server

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Build preflight: what the SERVER would need in order to build, checked from the server's own process.
//
// Why this exists: the most common payload-build failure is not a code problem, it is an environment one - the service account has no
// `go` on its PATH, its GOCACHE is unwritable, or the embedded agent tree is missing. All three surfaced only as a failed build, which
// meant an operator learned the shape of their deployment from an error message after waiting for a compile.
//
// The check runs `exec.LookPath` and `go version` as the server's own user: checking from a shell - where the operator's PATH is
// longer and their cache is writable - is exactly how this class of failure hides.

// toolchainCheckTimeout bounds each probe: a hung `go version` must not hang the request.
const toolchainCheckTimeout = 5 * time.Second

// handleBuildToolchain reports the environment the builder will actually see.
func (s *Server) handleBuildToolchain(c *gin.Context) {
	problems := make([]string, 0, 3)

	goPath, goErr := exec.LookPath("go")
	goReport := gin.H{"available": goErr == nil}
	if goErr == nil {
		goReport["path"] = goPath
		if version, versionErr := commandOutput(goPath, "version"); versionErr == nil {
			goReport["version"] = strings.TrimSpace(version)
		} else {
			// Present but not runnable is its own state: a truncated install or a wrong architecture looks like "go is there" to a file
			// check and fails every build after.
			goReport["error"] = "found but could not run `go version`"
			problems = append(problems, "`go` was found at "+goPath+" but could not run: the binary is damaged or for another architecture")
		}
	} else {
		problems = append(problems, "`go` is not on the server process's PATH: payload builds will fail with \"executable file not found\"")
	}

	// GOCACHE: the toolchain wants a writable cache, and a service account commonly inherits a read-only or absent one.
	cachePath := strings.TrimSpace(os.Getenv("GOCACHE"))
	cacheSource := "GOCACHE"
	if cachePath == "" {
		if dir, err := os.UserCacheDir(); err == nil {
			cachePath = dir + string(os.PathSeparator) + "go-build"
			cacheSource = "default (UserCacheDir/go-build)"
		}
	}
	cacheReport := gin.H{"path": cachePath, "source": cacheSource}
	if cachePath == "" {
		cacheReport["writable"] = false
		problems = append(problems, "no GOCACHE could be determined: set GOCACHE for the server's account")
	} else if writable, reason := directoryWritable(cachePath); !writable {
		cacheReport["writable"] = false
		cacheReport["error"] = reason
		problems = append(problems, "the build cache at "+cachePath+" is not writable: "+reason)
	} else {
		cacheReport["writable"] = true
	}

	// Optional: obfuscated builds need garble, and its absence is only a problem if the operator asks for it.
	garblePath, garbleErr := exec.LookPath("garble")
	garbleReport := gin.H{"available": garbleErr == nil}
	if garbleErr == nil {
		garbleReport["path"] = garblePath
	}

	// The embedded agent tree: present in a release archive, absent in a partial deployment.
	agentsDir := s.extractAgentsDir()
	agentsReport := gin.H{"path": agentsDir, "present": false}
	if agentsDir != "" {
		if info, err := os.Stat(agentsDir); err == nil && info.IsDir() {
			agentsReport["present"] = true
		} else {
			problems = append(problems, "the agent source tree is missing at "+agentsDir+": builds cannot start")
		}
	}

	respond(c, gin.H{
		"ok":         len(problems) == 0,
		"go":         goReport,
		"gocache":    cacheReport,
		"garble":     garbleReport,
		"agents_dir": agentsReport,
		"problems":   problems,
		"note":       "checked as the SERVER's own user: a shell where `go` works proves nothing about the account this process runs as",
	})
}

// commandOutput runs one short command and returns its combined output.
//
// A timeout is not politeness: `go version` on a network-mounted install can block for a long time, and this endpoint is called while an
// operator waits.
func commandOutput(binary string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), toolchainCheckTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", context.DeadlineExceeded
	}
	return string(output), err
}

// directoryWritable reports whether this process can create a file in dir, and why not when it cannot.
//
// Checking the mode bits would be wrong: they do not describe the account the process runs as.
func directoryWritable(dir string) (bool, string) {
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// A cache directory is created by the toolchain; what matters is whether its PARENT is writable.
			parent := dir
			if index := strings.LastIndexAny(parent, `/\`); index > 0 {
				parent = parent[:index]
			}
			if parentInfo, parentErr := os.Stat(parent); parentErr == nil && parentInfo.IsDir() {
				return directoryWritable(parent)
			}
			return false, "does not exist and its parent does not either"
		}
		return false, "cannot be inspected"
	}
	if !info.IsDir() {
		return false, "is a file, not a directory"
	}
	probe, err := os.CreateTemp(dir, "forgec2-cache-probe-*")
	if err != nil {
		return false, "the account cannot create files here"
	}
	probeName := probe.Name()
	_ = probe.Close()
	_ = os.Remove(probeName)
	return true, ""
}
