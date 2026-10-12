// Package trailpkcs11 signs the trail's checkpoints with a key in a PKCS #11
// token (#52): an HSM on the network or in the machine, a cloud's HSM through
// the library its vendor ships (AWS CloudHSM, Google's kmsp11, Thales Luna,
// YubiHSM), a smart card, or SoftHSM to try one.
//
// It is named in `audit.checkpoints.key` by an RFC 7512 URI, once
// `config/signerpkcs11` is imported:
//
//	pkcs11:token=payday;object=checkpoints?module-path=/usr/lib/softhsm/libsofthsm2.so&pin-source=/run/secrets/hsm-pin
//
// See [URI] for what it may say. The PIN is a file's (`pin-source`), as a
// secret is mounted; `pin-value` is read, and puts a PIN in a configuration.
//
// # What signs
//
//   - Ed25519 (`CKK_EC_EDWARDS`, `CKM_EDDSA`), over the checkpoint itself: the
//     token is sent the whole of it, which a token that limits what it signs
//     refuses as the archive grows;
//   - ECDSA on P-256, P-384 or P-521 (`CKM_ECDSA`), over a SHA-256 digest,
//     answered in ASN.1;
//   - RSA of 2048 bits or more (`CKM_RSA_PKCS`), PKCS #1 v1.5 over SHA-256, or
//     PSS when asked.
//
// The public half is the RSA private key's own, or the public key object -- or
// failing that the X.509 certificate -- with the key's `CKA_ID`. Whatever the
// token answers is verified against it before it is handed over, so a token
// whose objects disagree fails to sign a checkpoint rather than signing one
// nobody can verify.
//
// # What it cannot give away
//
// A key here is refused unless the token says it is `CKA_SENSITIVE` and not
// `CKA_EXTRACTABLE`: the token gives its value out to nobody, in the clear or
// wrapped. So whoever holds the process holds a session that can sign for as
// long as they hold it, and not the key: a compromise cleaned up is over,
// rather than a key to rotate and a trail to doubt. Whether the key was ever
// anywhere else -- made outside and imported -- the token says in
// `CKA_NEVER_EXTRACTABLE` and `CKA_LOCAL`, and that is the operator's to
// decide; make it on the token.
//
// # Built with cgo
//
// A module is a C library, loaded at run time, so this needs cgo. Built
// without -- `CGO_ENABLED=0`, or for js/wasm -- [Open] refuses rather than the
// package not building, so an app imports it either way. A library is loaded
// once a process and never let go; a key holds one session and signs one thing
// at a time, and a session the token dropped is opened again.
package trailpkcs11
