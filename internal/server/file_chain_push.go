package server

import (
	"encoding/hex"
	"errors"

	"github.com/forgec2/forgec2/internal/crypto"
)

var (
	errChainKeyUnavailable = errors.New("cannot derive file chain key")
	errChainMacUnavailable = errors.New("chunk MAC could not be computed")
)

// chainForPushSequence computes the link for ONE chunk of a multi-chunk push, given the
// previous chunk's link.
//
// The push path used chainForPush per chunk, which keys the chain by that chunk's own
// task id - so every chunk started from the zero seed. Each chunk was still
// integrity-protected against content tampering, but the chunks were NOT chained: a
// relay could reorder or replace chunk 2 onwards and every one of them still verified.
// This function is stateless so the caller can carry the link across the transfer.
func chainForPushSequence(chainKey, prev, chunkData []byte) (prevHex, macHex string, err error) {
	if chainKey == nil {
		return "", "", errChainKeyUnavailable
	}
	if prev == nil {
		prev = make([]byte, 32)
	}
	mac := crypto.FileChunkMAC(chainKey, prev, chunkData)
	if mac == nil {
		return "", "", errChainMacUnavailable
	}
	return hex.EncodeToString(prev), hex.EncodeToString(mac), nil
}
