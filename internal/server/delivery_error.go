package server

import (
	"errors"
	"net/url"
)

// deliveryErrorText renders an outbound-delivery error for a log line WITHOUT the URL.
//
// *url.Error embeds the request URL, and a notification or automation target carries its credential
// in that URL (https://api.telegram.org/bot<token>/sendMessage, a webhook token in the query string).
// Logging the wrapper therefore writes the secret somewhere it outlives the request. The cause is
// enough to diagnose a delivery failure.
func deliveryErrorText(err error) string {
	if err == nil {
		return ""
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return truncateString(urlErr.Err.Error(), 200)
	}
	return truncateString(err.Error(), 200)
}
