package server

import (
	"testing"

	"github.com/forgec2/forgec2/internal/db"
)

// TestNormalizeListenerProtocol pins the derivation of all three protocol
// fields from whichever one the operator supplied.
func TestNormalizeListenerProtocol(t *testing.T) {
	cases := []struct {
		in           db.Listener
		wantScheme   string
		wantProtocol string
		wantType     string
	}{
		// Scheme wins when present.
		{db.Listener{Scheme: "https"}, "https", "https", "http"},
		{db.Listener{Scheme: "dns", DNSDomain: "c2.example"}, "dns", "dns", "dns"},
		{db.Listener{Scheme: "icmp"}, "icmp", "icmp", "icmp"},
		{db.Listener{Scheme: "ssh"}, "ssh", "ssh", "ssh"},
		{db.Listener{Scheme: "h2c"}, "h2c", "h2c", "h2c"},
		{db.Listener{Scheme: "udp"}, "udp", "udp", "udp"},
		{db.Listener{Scheme: "quic"}, "quic", "quic", "quic"},
		// An unknown scheme is a raw TCP listener.
		{db.Listener{Scheme: "smb"}, "smb", "smb", "tcp"},
		// Protocol is used when the scheme is empty.
		{db.Listener{Protocol: "http"}, "http", "http", "http"},
		{db.Listener{Protocol: "tls"}, "tls", "tls", "tcp"},
		// Type alone derives both.
		{db.Listener{Type: "http"}, "http", "http", "http"},
		{db.Listener{Type: "udp"}, "udp", "udp", "udp"},
	}
	for _, tc := range cases {
		l := tc.in
		normalizeListenerProtocol(&l)
		if l.Scheme != tc.wantScheme || l.Protocol != tc.wantProtocol || l.Type != tc.wantType {
			t.Fatalf("normalize(%+v) = scheme=%q protocol=%q type=%q, want %q/%q/%q",
				tc.in, l.Scheme, l.Protocol, l.Type, tc.wantScheme, tc.wantProtocol, tc.wantType)
		}
	}

	// A listener with no protocol information at all stays untouched.
	empty := db.Listener{}
	normalizeListenerProtocol(&empty)
	if empty.Scheme != "" || empty.Protocol != "" || empty.Type != "" {
		t.Fatalf("empty listener gained fields: %+v", empty)
	}
}

// TestListenerKey pins the map key used to deduplicate running listeners.
func TestListenerKey(t *testing.T) {
	cases := []struct {
		in   db.Listener
		want string
	}{
		{db.Listener{Scheme: "http", Host: "10.0.0.1", Port: 8080}, "http://10.0.0.1:8080"},
		{db.Listener{Type: "tcp", Host: "0.0.0.0", Port: 9000}, "tcp://0.0.0.0:9000"},
		{db.Listener{Scheme: "dns", DNSDomain: "c2.example"}, "dns://c2.example"},
		{db.Listener{Scheme: "icmp", ICMPAddr: "0.0.0.0"}, "icmp://0.0.0.0"},
	}
	for _, tc := range cases {
		if got := listenerKey(&tc.in); got != tc.want {
			t.Fatalf("listenerKey(%+v) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// Two listeners on the same host/port produce the same key (how a duplicate
	// bind is detected), while a different port does not.
	a := db.Listener{Scheme: "http", Host: "10.0.0.1", Port: 8080}
	b := db.Listener{Scheme: "http", Host: "10.0.0.1", Port: 8080}
	c := db.Listener{Scheme: "http", Host: "10.0.0.1", Port: 8081}
	if listenerKey(&a) != listenerKey(&b) {
		t.Fatal("identical listeners produced different keys")
	}
	if listenerKey(&a) == listenerKey(&c) {
		t.Fatal("listeners on different ports produced the same key")
	}
}
