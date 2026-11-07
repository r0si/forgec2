package payload

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/forgec2/forgec2/pkg/peimage"
)

// buildLdflags returns the linker flags for the agent build. The runtime config
// blob, its per-build AES key and the self-check placeholder are NOT passed
// here: embedding them on the go build command line would expose the secrets in
// the build process's argv (B2) and, for the placeholder, make Go record the
// value in the build info section as well (which would then contain a second
// copy that patchSelfCheckHash must not patch). Instead they are delivered
// through an ephemeral generated source file — see writeConfigInjectFile — so
// the returned values are (ldflags, configBlob, sConfigKey).
func buildLdflags(cfg ImplantConfig, profile MalleableProfile, goos string) (string, string, string) {
	blob, sConfigKey := buildConfigBlobKeyed(cfg, profile)

	flags := "-s -w -buildid="
	if goos == "windows" && !cfg.Debug {
		flags += " -H=windowsgui"
	}
	// Compile-time OTA trust root: pin the teamserver update-signing public key
	// so every implant can verify self_update signatures before any config_push.
	// Public material only (safe on argv). On failure leave the pin empty so
	// the agent stays fail-closed until a later config_push supplies the key.
	if pub, err := UpdateSigningPublicKeyHex(); err == nil && pub != "" {
		flags += ` -X "main.updatePinnedPubKeyHex=` + pub + `"`
	}
	return flags, blob, sConfigKey
}

// writeConfigInjectFile writes an ephemeral Go source file into the agent build
// directory that sets the per-build runtime config blob, its AES key and the
// self-check placeholder during package init(). Delivering these via a temp
// source file (instead of the go build argv) keeps secrets out of the build
// process command line (B2) and keeps the placeholder out of Go's embedded
// build info, so it occurs exactly once in the compiled binary. The file lives
// only inside the throwaway build directory and is removed with it.
//
// The filename is randomized per build with an "aa_" prefix: Go runs a
// package's init() functions in source-file name order, and agent.go's init()
// calls loadConfigBlob() which reads ConfigBlob/SConfigKey. Any "aa_*" name
// sorts before "agent.go" ("aa" < "ag"), so the blob is always set before
// loadConfigBlob() executes, while the exact filename varies per build.
func writeConfigInjectFile(workDir, configBlob, sConfigKey string, selfCheck bool) error {
	if configBlob == "" && sConfigKey == "" && !selfCheck {
		return nil
	}
	src := "package main\n\n// Code-generated at build time; do not edit.\nfunc init() {\n"
	if configBlob != "" {
		src += "\tConfigBlob = " + strconv.Quote(configBlob) + "\n"
	}
	if sConfigKey != "" {
		src += "\tSConfigKey = " + strconv.Quote(sConfigKey) + "\n"
	}
	if selfCheck {
		// Placeholder 64-char hex string, patched with the real binary SHA-256
		// after the build (see patchSelfCheckHash). The agent zeroes this exact
		// region before hashing so verification succeeds on the unmodified binary.
		src += "\tSelfCheckSHA256Str = " + strconv.Quote(selfCheckPlaceholder) + "\n"
	}
	src += "}\n"
	name := "aa_config_inject.go"
	if b := make([]byte, 3); func() bool { _, err := rand.Read(b); return err == nil }() {
		name = "aa_" + hex.EncodeToString(b) + "_inject.go"
	}
	return os.WriteFile(filepath.Join(workDir, name), []byte(src), 0600)
}

// patchSelfCheckHash computes the SHA-256 of the (already finalized) binary and
// overwrites the embedded selfCheckPlaceholder with that hash. The agent zeroes
// the embedded hash region before hashing, so a tampered binary fails the check.
// It must run AFTER every other post-build mutation (PE stripping, validation)
// and BEFORE Authenticode signing.
//
// PE images are padded to 8 bytes first. The signer appends the certificate
// table at an 8-byte-aligned offset; without this padding it would insert bytes
// the unsigned digest never covered. With the padding in place, signing only
// changes the checksum and security-directory fields (both omitted from the
// digest) and appends the excluded table. The placeholder is zeroed before
// hashing so the agent, which zeroes the patched hash at runtime, reproduces the
// same digest.
func patchSelfCheckHash(outPath string) error {
	if len(selfCheckPlaceholder) != 64 {
		return fmt.Errorf("internal: self-check placeholder is not 64 bytes")
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		return fmt.Errorf("read for self-check patch: %w", err)
	}
	data = peimage.PadTo8(data)
	needle := []byte(selfCheckPlaceholder)
	if count := bytes.Count(data, needle); count != 1 {
		return fmt.Errorf("self-check placeholder found %d times in built binary (want exactly 1)", count)
	}
	idx := bytes.Index(data, needle)
	for i := idx; i < idx+len(needle); i++ {
		data[i] = 0
	}
	sum, err := peimage.Digest(data)
	if err != nil {
		return fmt.Errorf("compute self-check digest: %w", err)
	}
	real := hex.EncodeToString(sum)
	if len(real) != 64 {
		return fmt.Errorf("internal: computed self-check hash is not 64 bytes")
	}
	copy(data[idx:idx+len(needle)], []byte(real))
	// #nosec G306 -- the patched artifact is a built binary the agent must
	// execute; it is operator-generated content, not a secret.
	if err := os.WriteFile(outPath, data, 0755); err != nil { // #nosec G306
		return fmt.Errorf("write self-check patch: %w", err)
	}
	return nil
}
