package server

import (
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/natefinch/lumberjack.v2"
)

// fanoutWriter writes every record to each sink independently.
//
// io.MultiWriter - what this used to return - stops at the FIRST failing writer: once stdout
// is a closed console or a full disk, the rotating log file received nothing more, so the
// server kept running with its forensic record silently truncated. A C2's log is evidence;
// losing it because a terminal went away is the wrong trade.
type fanoutWriter struct {
	mu    sync.Mutex
	sinks []io.Writer
}

func newFanoutWriter(sinks ...io.Writer) *fanoutWriter {
	kept := make([]io.Writer, 0, len(sinks))
	for _, s := range sinks {
		if s != nil {
			kept = append(kept, s)
		}
	}
	return &fanoutWriter{sinks: kept}
}

func (f *fanoutWriter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var firstErr error
	wrote := 0
	for _, sink := range f.sinks {
		n, err := sink.Write(p)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if n > wrote {
			wrote = n
		}
	}
	if wrote == 0 {
		// Nothing accepted the record: this is the only case worth an error.
		return 0, firstErr
	}
	// A partial failure is still a written log line; reporting it as an error would make
	// slog drop the record even though one sink has it.
	return wrote, nil
}

// SetupLogRotation configures lumberjack-based log rotation and returns a writer that sends
// output to both stdout and the rotating log file. It also points the stdlib logger at the
// same writer: panics and dependency output used to reach stderr only, so the log file could
// be missing the most important line.
func SetupLogRotation(logDir string) io.Writer {
	if err := os.MkdirAll(logDir, 0750); err != nil {
		slog.Error("Failed to create log directory", "dir", logDir, "err", err)
		return os.Stdout
	}

	lumberjackLogger := &lumberjack.Logger{
		Filename:   filepath.Join(logDir, "forgec2.log"),
		MaxSize:    100, // MB
		MaxBackups: 5,
		MaxAge:     30, // days
		Compress:   true,
	}

	w := newFanoutWriter(os.Stdout, lumberjackLogger)
	log.SetOutput(w)
	return w
}
