package main

import "testing"

// TestStampScreenshotTask pins the wire association: every chunk of a capture
// must carry the task id, otherwise the server cannot reassemble the image and
// silently drops the chunk (the old TaskID 0 behaviour).
func TestStampScreenshotTask(t *testing.T) {
	results := []TaskResult{{}, {TaskID: 9, Type: "screenshot_chunk"}}
	stampScreenshotTask(7, results)
	for i := range results {
		if results[i].TaskID != 7 {
			t.Fatalf("chunk %d TaskID = %d, want 7", i, results[i].TaskID)
		}
	}
}
