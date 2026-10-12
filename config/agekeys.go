package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"filippo.io/age"
	"filippo.io/age/plugin"
)

// The master of `audit.keys` in a device, as age has one (#52): a plugin.
//
// age gives a key it does not hold to a program, `age-plugin-<name>` on PATH --
// age-plugin-yubikey for a YubiKey's PIV slot, age-plugin-tpm for a TPM,
// age-plugin-se for a Mac's Secure Enclave. Its recipient, `age1yubikey1…`, is
// the device's public half, and the plugin seals to it without the device;
// its identity, `AGE-PLUGIN-YUBIKEY-1…`, names the device, and opening with it
// takes the device and whatever it asks for -- a PIN, a touch. So the serving
// process, which only seals, runs the plugin and never needs the device, and
// the master's private half is where no file is.
//
// What is age's own is read as it was, and a plugin's only when age's reading
// refuses it: `age1pq1…` is age's post-quantum recipient, and would read as a
// plugin's called `pq`.

// recipientsOf reads one entry of `audit.keys.master`.
func recipientsOf(v string) ([]age.Recipient, error) {
	out := []age.Recipient{}
	err := lines(v, func(line string) error {
		rs, err := age.ParseRecipients(strings.NewReader(line))
		if err == nil {
			out = append(out, rs...)
			return nil
		}

		name, _, perr := plugin.ParseRecipient(line)
		if perr != nil {
			return err
		}
		if err := onPath(name); err != nil {
			return err
		}
		r, err := plugin.NewRecipient(line, sealingUI)
		if err != nil {
			return err
		}

		out = append(out, r)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("no recipient")
	}

	return out, nil
}

// identitiesOf reads `audit.keys.identity`'s document.
func identitiesOf(doc string) ([]age.Identity, error) {
	out := []age.Identity{}
	err := lines(doc, func(line string) error {
		if !strings.HasPrefix(line, "AGE-PLUGIN-") {
			ids, err := age.ParseIdentities(strings.NewReader(line))
			if err != nil {
				return err
			}

			out = append(out, ids...)
			return nil
		}

		name, _, err := plugin.ParseIdentity(line)
		if err != nil {
			// Not the line: it is a private key's encoding.
			return errors.New("an AGE-PLUGIN- identity that does not read as one")
		}
		if err := onPath(name); err != nil {
			return err
		}
		id, err := plugin.NewIdentity(line, plugin.NewTerminalUI(
			func(format string, v ...any) { fmt.Fprintf(os.Stderr, format+"\n", v...) },
			func(format string, v ...any) { fmt.Fprintf(os.Stderr, "warning: "+format+"\n", v...) },
		))
		if err != nil {
			return err
		}

		out = append(out, id)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("no identity")
	}

	return out, nil
}

// sealable seals to the master once, where the process comes up: a plugin
// that will not run, or recipients age will not seal to together, is a
// process that does not start rather than a tenant's first row that cannot be
// sealed.
func sealable(rs []age.Recipient) error {
	w, err := age.Encrypt(io.Discard, rs...)
	if err != nil {
		return err
	}

	return w.Close()
}

// onPath refuses a plugin that is not there to run.
func onPath(name string) error {
	if _, err := exec.LookPath("age-plugin-" + name); err != nil {
		return fmt.Errorf("age-plugin-%s is not on PATH, and it is what this key is used through", name)
	}

	return nil
}

// sealingUI is what a plugin may ask of a process that seals: nothing, since
// nobody is there to answer. A plugin that seals asks for nothing anyway --
// the device is for opening -- and one that does is refused out loud rather
// than left waiting.
var sealingUI = &plugin.ClientUI{
	DisplayMessage: func(name, message string) error {
		fmt.Fprintf(os.Stderr, "age-plugin-%s: %s\n", name, message)
		return nil
	},
	RequestValue: func(name, prompt string, _ bool) (string, error) {
		return "", fmt.Errorf("age-plugin-%s asks %q, and a process that seals has nobody to ask", name, prompt)
	},
	Confirm: func(name, prompt, _, _ string) (bool, error) {
		return false, fmt.Errorf("age-plugin-%s asks %q, and a process that seals has nobody to ask", name, prompt)
	},
}

// lines hands over each line that says something: not empty, and not a
// comment, as age's files are written.
func lines(v string, fn func(line string) error) error {
	s := bufio.NewScanner(strings.NewReader(v))
	s.Buffer(make([]byte, 0, 64*1024), 1<<24)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := fn(line); err != nil {
			return err
		}
	}

	return s.Err()
}
