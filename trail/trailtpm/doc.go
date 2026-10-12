// Package trailtpm signs the trail's checkpoints with a key in a TPM 2.0 (#52):
// the machine's own, or a virtual machine's vTPM, which every cloud gives a
// VM that asks for one (shielded, trusted launch, Nitro).
//
// It is named in `audit.checkpoints.key` by the key's persistent handle, once
// `config/signertpm` is imported:
//
//	tpm:0x81000100
//	tpm:0x81000100?device=/dev/tpmrm0&auth-source=/run/secrets/tpm-key-auth
//
// `device` is `/dev/tpmrm0` unless said, the kernel's resource manager, which
// shares the TPM with whatever else uses it; `/dev/tpm0` is the TPM itself, one
// user at a time. A Unix socket is an emulator's -- swtpm's, `swtpm socket
// --server type=unixio,path=…` -- which keeps its keys in a file and is for
// trying, not for keeping a key from anybody. `auth-source` is the file the
// key's authorization value is in, for a key made with one; none is an empty
// one. A key whose use is a policy rather than a value is not read here yet.
//
// # Making one
//
// With tpm2-tools, a signing key made in the TPM from the owner hierarchy's
// seed, and kept at a handle:
//
//	tpm2_createprimary -C o -G ecc256:ecdsa-sha256 -c checkpoints.ctx \
//	    -a 'fixedtpm|fixedparent|sensitivedataorigin|userwithauth|sign'
//	tpm2_evictcontrol -C o -c checkpoints.ctx 0x81000100
//	tpm2_readpublic -c 0x81000100 -f pem -o checkpoints.pub   # for audit.checkpoints.trust elsewhere
//
// Clearing the TPM (`tpm2_clear`, or a firmware reset) destroys it, as it is
// meant to: what it signed stays verifiable by its public half, kept in
// `audit.checkpoints.trust`.
//
// # What signs
//
//   - ECDSA on P-256, and P-384 or P-521 where the TPM has them, over a
//     SHA-256 digest, answered in ASN.1;
//   - RSA of 2048 bits, and 3072 or more where the TPM has them, PKCS #1 v1.5
//     over SHA-256 -- or PSS, for a key made to sign nothing else, which a
//     checkpoint's verifier reads as well.
//
// Not Ed25519, which TPMs do not have. Whatever the TPM answers is verified
// against the public half it gave at Open before it is handed over.
//
// # What it cannot give away
//
// A key here is refused unless it is `fixedTPM` and `fixedParent`: it cannot
// be duplicated out of this TPM, by anybody, so its private half exists
// nowhere else and whoever holds the process can sign with it while they hold
// the process, and not after. A restricted key -- an attestation key, which
// signs only what the TPM hashed itself -- is refused too, since a checkpoint
// is hashed here.
package trailtpm
