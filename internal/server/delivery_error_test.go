package server

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

// TestDeliveryErrorTextStripsTheURL keeps credentials out of delivery logs.
//
// A notification or automation target carries its credential in the URL - Telegram's
// /bot<token>/sendMessage, or a webhook token in the query string - and *url.Error embeds that URL.
// The automation webhook already had a comment saying "log cause only" while passing the wrapper, so
// the fix is not just a helper but using it at both call sites.
func TestDeliveryErrorTextStripsTheURL(t *testing.T) {
	secret := "123456:AAH-super-secret-token"
	urlErr := &url.Error{
		Op:  "Post",
		URL: "https://api.telegram.org/bot" + secret + "/sendMessage",
		Err: errors.New("dial tcp 1.2.3.4:443: connect: connection refused"),
	}

	got := deliveryErrorText(urlErr)
	if strings.Contains(got, secret) {
		t.Fatalf("the token leaked into the log text: %q", got)
	}
	if !strings.Contains(got, "connection refused") {
		t.Fatalf("the cause was lost, which makes the log useless: %q", got)
	}

	// A plain error is passed through (truncated, not discarded).
	if got := deliveryErrorText(errors.New("invalid chat_id")); got != "invalid chat_id" {
		t.Fatalf("plain error = %q", got)
	}
	if got := deliveryErrorText(nil); got != "" {
		t.Fatalf("nil error = %q", got)
	}
	// A very long error cannot flood a log line.
	long := strings.Repeat("x", 500)
	if got := deliveryErrorText(errors.New(long)); len(got) > 210 {
		t.Fatalf("long error was not truncated: %d bytes", len(got))
	}
}
