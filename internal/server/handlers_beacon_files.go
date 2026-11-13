package server

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"

	"github.com/forgec2/forgec2/internal/db"
)

// persistFileChunk writes one upload/download chunk to disk and returns the
// operator-facing summary. It deliberately does not touch the task row or
// encrypt fields: the caller decides the terminal state only after the bytes
// are durable, so a completed chunk task cannot be missing its file.
func (s *Server) persistFileChunk(uuid string, task *db.Task, r taskResult, logPrefix, resultPrefix string) (string, error) {
	uploadsRoot, err := s.uploadsRootDir()
	if err != nil {
		return "", fmt.Errorf("server.data_dir is not configured; refusing to write the chunk relative to the working directory")
	}
	uploadBase := safeJoin(uploadsRoot, uuid)
	if uploadBase == "" {
		slog.Error("Invalid agent ID for upload path", "agent_id", uuid)
		return "", fmt.Errorf("invalid agent id")
	}
	if err := os.MkdirAll(uploadBase, 0700); err != nil {
		slog.Error("Failed to create uploads dir", "agent_id", uuid, "error", err)
		return "", fmt.Errorf("create uploads dir: %w", err)
	}
	filename := r.Filename
	if filename == "" {
		filename = fmt.Sprintf("file_%d", task.ID)
	}
	filePath := safeJoin(uploadBase, filename)
	if filePath == "" {
		return "", fmt.Errorf("invalid filename (path traversal blocked)")
	}
	decoded, err := base64.StdEncoding.DecodeString(r.Output)
	if err != nil {
		return "", fmt.Errorf("base64 decode failed: %w", err)
	}
	if len(decoded) > MaxTransferChunkSize {
		slog.Warn("Oversized exfil chunk rejected", "agent_id", uuid, "task_id", task.ID, "size", len(decoded))
		return "", fmt.Errorf("chunk too large (%d bytes, max %d)", len(decoded), MaxTransferChunkSize)
	}
	// Remember the link this chunk is about to replace: every local failure below
	// (invalid offset, open/seek/write error) rolls it back, so a transient disk problem
	// cannot advance the chain and poison the rest of the upload.
	chainPrev := s.chainLinkFor(task.ID)
	if err := s.verifyAndCommitChain(uuid, task.ID, r.MAC, decoded); err != nil {
		slog.Warn("File chunk integrity failure", "agent_id", uuid, "task_id", task.ID, "error", err)
		return "", fmt.Errorf("integrity check failed: %w", err)
	}
	off := r.Offset
	if off == 0 {
		off = task.Offset
	}
	// Offset validation (agent-controlled): a negative value skipped both the
	// O_TRUNC branch and the Seek, silently corrupting chunk 0; an astronomic
	// value made Seek+Write zero-fill terabytes of teamserver disk. Bound it
	// against the declared transfer size with a sane hard cap.
	const maxChunkOffset = int64(8) << 30 // 8 GiB
	if off < 0 || off > maxChunkOffset {
		s.rollbackChainLink(task.ID, chainPrev)
		slog.Warn("File chunk rejected: invalid offset", "agent_id", uuid, "task_id", task.ID, "offset", off)
		return "", fmt.Errorf("invalid chunk offset %d", off)
	}
	openFlags := os.O_CREATE | os.O_WRONLY
	if off == 0 {
		openFlags |= os.O_TRUNC
	}
	f, err := os.OpenFile(filePath, openFlags, 0600)
	if err != nil {
		s.rollbackChainLink(task.ID, chainPrev)
		return "", fmt.Errorf("open file failed: %w", err)
	}
	defer f.Close()
	if off > 0 {
		if _, err := f.Seek(off, 0); err != nil {
			s.rollbackChainLink(task.ID, chainPrev)
			return "", fmt.Errorf("seek failed: %w", err)
		}
	}
	if _, err := f.Write(decoded); err != nil {
		s.rollbackChainLink(task.ID, chainPrev)
		return "", fmt.Errorf("write failed: %w", err)
	}
	summary := fmt.Sprintf("%s: %s offset %d (%d bytes)", resultPrefix, filename, off, r.Size)
	if len(summary) > MaxResultSize {
		summary = truncateString(summary, MaxResultSize)
	}
	slog.Info("File chunk "+logPrefix, "agent_id", uuid, "file", filename, "offset", off, "size", r.Size)
	return summary, nil
}
