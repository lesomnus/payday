//go:build !windows

package trailtpm

import (
	"fmt"
	"os"

	"github.com/google/go-tpm/tpm2/transport"
	"github.com/google/go-tpm/tpm2/transport/linuxtpm"
	"github.com/google/go-tpm/tpm2/transport/linuxudstpm"
)

// openDevice opens a TPM's character device, or an emulator's Unix socket.
func openDevice(path string) (transport.TPMCloser, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	switch {
	case fi.Mode()&os.ModeDevice != 0:
		return linuxtpm.Open(path)
	case fi.Mode()&os.ModeSocket != 0:
		return linuxudstpm.Open(path)
	}

	return nil, fmt.Errorf("%s is neither a TPM's device nor an emulator's socket", path)
}
