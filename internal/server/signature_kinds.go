package server

// The four things this system calls a "signature", named so a response can say which one it means.
//
// The word is overloaded: an audit chain entry, an Ed25519 signature over a handover bundle, an Ed25519 signature over a plugin
// package, and a normalised error shape that involves no cryptography at all. A reader who takes the last one for a keyed
// signature believes the wrong thing about what was proven, and a review that confuses the first with the second concludes the
// chain is authenticated when it is only tamper-evident.
//
// Each response that reports one now carries its kind, so the distinction travels with the data instead of living only in
// docs/TERMINOLOGY.md.
const (
	// auditSignatureKind is the unkeyed hash chain. It detects partial edits and does not resist a full rewrite.
	auditSignatureKind = "audit hash chain entry: unkeyed, detects partial edits, does not resist a full rewrite"

	// bundleSignatureKind is the handover manifest signature.
	bundleSignatureKind = "ed25519 signature over the bundle manifest: keyed, proves the signer held the private key"

	// pluginSignatureKind is the plugin package signature.
	pluginSignatureKind = "ed25519 signature over the plugin package digest: keyed, checked against plugins.trusted_keys"

	// failureSignatureKind is the normalised error shape. It is not a cryptographic signature at all.
	failureSignatureKind = "failure shape: normalised error text with paths, ids and digits collapsed - not a cryptographic signature"
)
