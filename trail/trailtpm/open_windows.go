package trailtpm

import (
	"errors"

	"github.com/google/go-tpm/tpm2/transport"
)

// openDevice refuses: a TPM on Windows is TBS's, which [Open] does not reach.
func openDevice(string) (transport.TPMCloser, error) {
	return nil, errors.New("a TPM on Windows is reached through TBS, which tpm: does not open; hand trailtpm.New the TPM go-tpm/tpm2/transport/windowstpm opens")
}
