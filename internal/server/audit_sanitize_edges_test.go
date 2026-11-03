package server

import (
	"strings"
	"testing"
)

func TestSanitizeDetailsSecretBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, input, secret string
	}{
		{"uppercase JSON", `{"PASSWORD":"hidden-value"}`, "hidden-value"},
		{"escaped JSON quote", `{"token":"prefix\"secret-tail"}`, "secret-tail"},
		{"short plain secret", `password=x`, "=x"},
		{"PEM without delimiter", "-----BEGIN PRIVATE KEY-----\nhidden-key-body\n-----END PRIVATE KEY-----", "hidden-key-body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeDetails(tc.input)
			if strings.Contains(got, tc.secret) {
				t.Fatal("sensitive value survived redaction")
			}
			if !strings.Contains(got, "*****") {
				t.Fatal("redaction marker missing")
			}
			if twice := sanitizeDetails(got); twice != got {
				t.Fatal("redaction is not idempotent")
			}
		})
	}
	if got := sanitizeDetails("ordinary status message"); got != "ordinary status message" {
		t.Fatal("benign message changed")
	}
}
