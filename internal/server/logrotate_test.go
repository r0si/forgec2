package server

import (
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failingWriter struct{ err error }

func (f failingWriter) Write(p []byte) (int, error) { return 0, f.err }

type recordingWriter struct{ lines []string }

func (r *recordingWriter) Write(p []byte) (int, error) {
	r.lines = append(r.lines, string(p))
	return len(p), nil
}

// TestFanoutWriterSurvivesOneFailingSink pins the reason this type exists: io.MultiWriter
// stops at the first failing writer, so a closed stdout - a detached console, a rotated
// redirect target, a full disk - silently ended the rotating log file too. The log is the
// forensic record; one broken sink must not take the others with it.
func TestFanoutWriterSurvivesOneFailingSink(t *testing.T) {
	rec := &recordingWriter{}
	w := newFanoutWriter(failingWriter{err: errors.New("stdout gone")}, rec)

	n, err := w.Write([]byte("line one\n"))
	if err != nil {
		t.Fatalf("a single failing sink made the write fail: %v", err)
	}
	if n != len("line one\n") {
		t.Fatalf("wrote %d bytes, want %d", n, len("line one\n"))
	}
	if len(rec.lines) != 1 || rec.lines[0] != "line one\n" {
		t.Fatalf("the healthy sink did not receive the record: %#v", rec.lines)
	}

	// The bad sink coming first must not matter: order is not priority.
	rec2 := &recordingWriter{}
	w2 := newFanoutWriter(rec2, failingWriter{err: errors.New("disk full")})
	if _, err := w2.Write([]byte("line two\n")); err != nil {
		t.Fatalf("write with a trailing failing sink: %v", err)
	}
	if len(rec2.lines) != 1 {
		t.Fatalf("the healthy sink missed the record: %#v", rec2.lines)
	}
}

// TestFanoutWriterReportsTotalFailure keeps the error path honest: when NOTHING accepts the
// record the caller has to know.
func TestFanoutWriterReportsTotalFailure(t *testing.T) {
	boom := errors.New("boom")
	w := newFanoutWriter(failingWriter{err: boom}, failingWriter{err: boom})
	if _, err := w.Write([]byte("x")); err == nil {
		t.Fatal("a write no sink accepted reported success")
	}
}

// TestSetupLogRotationWritesAFile is the end-to-end check: the rotating file really receives
// what the logger writes, so "logs go to logs/forgec2.log" is verified rather than assumed.
//
// os.MkdirTemp instead of t.TempDir: lumberjack holds the file open, so TempDir's cleanup
// cannot delete it on Windows. The writer is closed before cleanup, and the stdlib logger is
// restored so this test does not redirect anyone else's output.
func TestSetupLogRotationWritesAFile(t *testing.T) {
	dir, err := os.MkdirTemp("", "logrotate-*")
	if err != nil {
		t.Fatal(err)
	}

	prev := log.Writer()
	w := SetupLogRotation(dir)
	if w == nil {
		t.Fatal("SetupLogRotation returned nil")
	}
	t.Cleanup(func() {
		log.SetOutput(prev)
		if fan, ok := w.(*fanoutWriter); ok {
			for _, sink := range fan.sinks {
				// Close only sinks this test OWNS. os.Stdout implements io.Closer, so the first
				// version of this cleanup closed the test binary's stdout: the framework's final
				// PASS line could never be written, and go test reported FAIL with no failing test
				// named and output ending mid-run - a flake that looked like a production bug for
				// many rounds and only bit when this test ran last.
				if sink == io.Writer(os.Stdout) || sink == io.Writer(os.Stderr) {
					continue
				}
				if c, ok := sink.(io.Closer); ok {
					_ = c.Close()
				}
			}
		}
		_ = os.RemoveAll(dir)
	})

	if _, err := w.Write([]byte("hello-from-the-test\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	path := filepath.Join(dir, "forgec2.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("log file not written: %v", err)
	}
	if !strings.Contains(string(data), "hello-from-the-test") {
		t.Fatalf("log file does not contain the record: %q", string(data))
	}
}
