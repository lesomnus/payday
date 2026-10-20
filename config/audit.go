package config

import (
	"crypto"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lesomnus/flob"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"
)

// AuditConfig is how long the trail is kept, and where what leaves it goes.
//
// `schema/payday/audit.proto` asks for this in as many words -- *an app with an
// obligation to destroy data has to reckon with the trail, and the answer is a
// retention policy rather than an empty column* -- and until there was one, the
// answer every payday app gave was **forever**, arrived at by not deciding, on
// the one table that never stops growing.
//
// # Two clocks, per kind of thing
//
// `retain` is operational: how much of the trail the database carries, which is
// what a console can show and what a query costs. `destroy` is the obligation,
// and it is normally years the longer. `archive` is where a row lives out the
// difference.
//
// And neither is uniform across a deployment's entities, which is why `by:` is
// here from the start. What was done to a person is under a privacy regime and
// eventually has to stop existing; what a machine did is an operating record
// with the opposite requirement. One clock over the whole table forces the
// shorter of the two onto everything.
//
//	audit:
//	  profile: pipa
//	  archive: /var/lib/roster/audit
//	  min:
//	    profile: pipa
//	  by:
//	    robot:
//	      profile: forever
//	    holder:
//	      profile: gdpr
//
// Empty is forever, which is what a deployment that has not thought about it
// gets, and is the only honest default: a version upgrade is not the right
// thing to decide how long somebody's evidence lasts.
//
// # And per tenant, from the app
//
// A deployment whose tenants keep their history for different lengths of time
// answers that in code, through `trail.Policy.Tenants`, since what a tenant
// keeps is the app's data and not a setting. What this block says is the
// deployment's: its own windows, which a tenant that answers nothing gets, and
// its floors, `min:`, which nothing a tenant answers goes below.
type AuditConfig struct {
	// Profile fills the two clocks in from a named regime -- `pci`, `hipaa`,
	// `sox`, `pipa`, `pipa-sensitive`, `gdpr`, `forever`. See [trail.Profiles],
	// which carries the sentence each number comes from.
	//
	// It is a **starting point and not a guarantee**, for the reason written
	// there: what a deployment is obliged to keep depends on what it processes
	// and for whom, and two deployments of one app can be under different
	// rules. What a profile buys is that the number in the file is arguable
	// rather than arbitrary.
	//
	// Anything written beside it wins. A deployment that names a profile and
	// then sets `destroy:` knows something the table does not.
	Profile string `yaml:"profile"`

	// Retain is how long a row stays in the database, e.g. `2160h` for ninety
	// days. Empty takes the profile's, and then forever.
	Retain time.Duration `yaml:"retain"`

	// Archive is the directory rows are written to on their way out of the
	// database, for every kind and every tenant. Empty keeps no copy, which is
	// refused unless `discard` says the deployment means it.
	//
	// It is a flob store on the disk, a namespace per tenant -- see the `trail`
	// package -- whose every blob the database keeps an account of; see
	// `checkpoints` below. A directory a version before this one wrote files
	// into is the same setting: the first pass adopts what is there, and files
	// it by tenant.
	// An archive anywhere else flob reaches, S3 among them, is handed in by
	// the app as `trail.Policy.Archive`.
	Archive string `yaml:"archive"`

	// Discard is a deployment saying it means to keep nothing.
	//
	// Its own setting rather than an empty `archive`, because those are two
	// different states that look alike: *I have not configured where* and *I do
	// not want one*. A blank field that defaults to destruction is the
	// configuration mistake that gets discovered by an auditor.
	Discard bool `yaml:"discard"`

	// Destroy is how long a row exists at all, counted from when it was
	// written: it leaves the database at `retain` and the archive at this.
	// Empty takes the profile's, and then forever.
	Destroy time.Duration `yaml:"destroy"`

	// Every is how often the policy is applied, for every kind. Empty is
	// [trail.Swept], a day.
	Every time.Duration `yaml:"every"`

	// Content is how much of a write a row of the trail holds -- `fields`,
	// `patch` or `value` -- for every kind `by:` does not name otherwise. Empty
	// is `patch`. See [audit.Content] (#92).
	//
	// `value` keeps the whole row as each write left it, which is a copy of the
	// row on every write in the one table nothing rewrites. A deployment asks
	// for it where it has a reason to, and best by kind: a record of what a
	// machine did, say. A field the schema declared `personal` or `secret` is
	// on the trail at no level, and no setting here puts it back.
	Content string `yaml:"content"`

	// Min is the deployment's own floor: the least it keeps of every kind,
	// whatever a tenant answers. See [AuditMinConfig].
	Min AuditMinConfig `yaml:"min"`

	// By is the kinds that are not like the rest, keyed by the name the schema
	// registered -- `holder`, `robot`, `audit`. A kind named here that this app
	// does not have is refused rather than ignored.
	By map[string]AuditKeepConfig `yaml:"by"`

	// Checkpoints is where a pass leaves what the archive held when it ended,
	// and the key it signs that with. See `trail.Checkpoint`.
	Checkpoints AuditCheckpointsConfig `yaml:"checkpoints"`

	// Keys seals what the archive's rows say, a key per tenant. See
	// [AuditKeysConfig].
	Keys AuditKeysConfig `yaml:"keys"`

	// Digest keys the digest each row of the trail keeps of the row its write
	// left. See [AuditDigestConfig].
	Digest AuditDigestConfig `yaml:"digest"`
}

// AuditDigestConfig keys the digest each row of the trail keeps of the row its
// write left (`Audit.digest`, payday#102): what lets a live row be checked
// against the trail, so that one changed in the database past the servers is
// found.
//
//	audit:
//	  digest:
//	    key: /run/secrets/audit-digest   # openssl rand -base64 32
//
// The key is kept out of the database and out of its backups -- mounted as a
// secret, as `audit.checkpoints.key` is -- since a digest is worth what it
// keeps from somebody who can write the database, and that is a key they do
// not have. Empty digests nothing, which is what a trail was before.
type AuditDigestConfig struct {
	// Key is at least 32 random bytes in base64, as `openssl rand -base64 32`
	// prints them. It is one of two things, told apart by how it begins:
	//
	//   - `base64:`, and then the key, which is how a secret is given through
	//     the environment (`<APP>_AUDIT_DIGEST_KEY`);
	//   - anything else: the file the key is in, which is how a secret is
	//     mounted.
	Key string `yaml:"key"`

	// Previous is the keys Key has replaced, in either form, for checking the
	// digests made under them. A digest says which key it was made under, and
	// one under a key in neither is said to be so rather than to differ.
	Previous []string `yaml:"previous"`
}

// digest is the block, read: nil for none.
func (c AuditDigestConfig) digest() (*audit.Digest, error) {
	if strings.TrimSpace(c.Key) == "" {
		if len(c.Previous) > 0 {
			return nil, errors.New("previous: names keys a digest is checked with, and there is no key to make one")
		}

		return nil, nil
	}

	key, err := digestKeyOf(c.Key)
	if err != nil {
		return nil, fmt.Errorf("key: %w", err)
	}

	previous := make([][]byte, len(c.Previous))
	for i, v := range c.Previous {
		if previous[i], err = digestKeyOf(v); err != nil {
			return nil, fmt.Errorf("previous[%d]: %w", i, err)
		}
	}

	return audit.NewDigest(key, previous...)
}

// digestKeyOf reads what `audit.digest.key` says: the key itself after
// `base64:`, or the file it is in.
func digestKeyOf(v string) ([]byte, error) {
	v = strings.TrimSpace(v)

	text, inline := strings.CutPrefix(v, "base64:")
	if !inline {
		b, err := os.ReadFile(v)
		if err != nil {
			return nil, err
		}
		text = string(b)
	}

	text = strings.TrimSpace(text)
	b, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		if b, err = base64.RawStdEncoding.DecodeString(text); err != nil {
			return nil, errors.New("not base64: write one with `openssl rand -base64 32`")
		}
	}
	if len(b) < 32 {
		return nil, fmt.Errorf("%d bytes, and a key is at least 32: write one with `openssl rand -base64 32`", len(b))
	}

	return b, nil
}

// AuditKeysConfig seals what the archive's rows say: a key per tenant, each
// sealed in turn to a master whose private half the serving process does not
// hold.
//
//	audit:
//	  archive: /var/lib/app/audit
//	  keys:
//	    dir: /var/lib/app/audit-keys
//	    master: age1…   # `age-keygen -y` of a key kept offline
//
// Empty seals nothing, which is what an archive was before. Encryption at rest
// is still the storage's -- an encrypted disk, S3 with a KMS key -- and it
// covers what most requirements ask. What this adds is that a tenant that
// leaves takes what its rows said with it, out of every copy, the archive's
// backups among them; see `trail.Keys`.
type AuditKeysConfig struct {
	// Dir is the key store: a directory of its own, not inside `archive`, and
	// one whose backups are kept short, since they are what decides how long
	// a departed tenant's rows can still be read. A store that is not a
	// directory is handed in by the app as `trail.Keys.Store`.
	Dir string `yaml:"dir"`

	// Master is the public key each tenant's key is sealed to, as
	// `age-keygen` prints one (`age1…`), or several for more than one way
	// back. Its private half is kept offline -- or in a device, through the
	// age plugin that speaks to it: `age1yubikey1…`, `age1tpm1…`, which the
	// process seals to by running `age-plugin-<name>` from PATH, and without
	// the device. It is sealed to once where the process comes up, so a
	// plugin that will not run is a process that does not start.
	Master []string `yaml:"master"`

	// Identity is the master's private half, for a process that reads what
	// rows say -- an export, an operator at a shell -- and for no other: never
	// set where the process serves. Named by the file it is in, which is how a
	// secret is mounted, or the key itself (`AGE-SECRET-KEY-1…`), which is how
	// one comes through the environment for one command. A plugin's identity
	// (`AGE-PLUGIN-YUBIKEY-1…`) names the device instead, which opens what the
	// command reads and asks the terminal for what it wants: a PIN, a touch.
	Identity string `yaml:"identity"`
}

// on answers whether the block says anything at all.
func (c AuditKeysConfig) on() bool { return c.Dir != "" || len(c.Master) > 0 || c.Identity != "" }

// keys is the block, read.
func (c AuditKeysConfig) keys(archive string) (*trail.Keys, error) {
	if !c.on() {
		return nil, nil
	}

	k := &trail.Keys{}
	if c.Dir != "" {
		if archive != "" {
			if rel, err := filepath.Rel(archive, c.Dir); err == nil && (rel == "." || !strings.HasPrefix(rel, "..")) {
				return nil, errors.New("dir: inside audit.archive, which would keep the keys as long as the archive and back them up with it")
			}
		}

		k.Store = flob.NewOsStores(c.Dir)
	}

	for i, v := range c.Master {
		rs, err := recipientsOf(v)
		if err != nil {
			return nil, fmt.Errorf("master[%d]: %w", i, err)
		}

		k.Master = append(k.Master, rs...)
	}
	if len(k.Master) > 0 {
		if err := sealable(k.Master); err != nil {
			return nil, fmt.Errorf("master: %w", err)
		}
	}

	if v := strings.TrimSpace(c.Identity); v != "" {
		doc := v
		if !strings.HasPrefix(v, "AGE-SECRET-KEY-") && !strings.HasPrefix(v, "AGE-PLUGIN-") {
			b, err := os.ReadFile(v)
			if err != nil {
				return nil, fmt.Errorf("identity: %w", err)
			}

			doc = string(b)
		}

		ids, err := identitiesOf(doc)
		if err != nil {
			return nil, fmt.Errorf("identity: %w", err)
		}

		k.Identities = ids
	}

	return k, nil
}

// AuditCheckpointsConfig is where checkpoints are kept, and what signs them.
//
// There is no switch here. An archive always has a manifest in the database
// and a checkpoint at the end of every pass. What these settings change is
// what a checkpoint is worth against somebody who can write both the database
// and the archive: kept apart from the archive, on a store that refuses
// deletion, and signed by a key kept apart from the process's credentials.
type AuditCheckpointsConfig struct {
	// Dir is a directory of their own, which is where a disk that refuses to
	// delete -- a WORM mount -- goes. Empty keeps them in the archive, in
	// `_checkpoints`. A store that is not a directory, S3 with Object Lock
	// among them, is handed in by the app as `trail.Policy.Checkpoints`.
	Dir string `yaml:"dir"`

	// WriteOnce says the storage `dir` is on keeps what it is given -- a WORM
	// mount -- and has the process keep to it as well: it never erases a
	// checkpoint or changes its labels, and says where it comes up that they
	// are kept on a store that says it is write-once. See `trail.WriteOnce`.
	//
	// It is the process's promise and not the disk's. On an ordinary disk,
	// whoever holds the process's user, or root, removes or rewrites any file
	// in the directory as before, and this keeps nothing from them.
	WriteOnce bool `yaml:"write_once"`

	// Key is what checkpoints and the manifest's rows are signed with --
	// Ed25519, ECDSA or RSA -- and empty signs nothing. It is one of three
	// things, told apart by how it begins:
	//
	//   - `-----BEGIN`: the PEM document of a key, as `openssl genpkey`
	//     writes one, which is how a secret is given through the environment
	//     (`<APP>_AUDIT_CHECKPOINTS_KEY`);
	//   - a scheme that something registered with [RegisterSigner]: a key in
	//     a device, which signs and does not give its private half out --
	//     `pkcs11:` for an HSM, `tpm:`, `hashivault://`, `awskms://`;
	//   - anything else: the file such a document is in, which is how a
	//     secret is mounted.
	//
	// Read, and a device's key made to sign once, where the process comes up:
	// a key that does not read is a process that does not start rather than
	// checkpoints that stop being signed, or are signed by something nobody
	// can verify.
	Key string `yaml:"key"`

	// Trust is the public keys besides Key's own whose signatures a
	// verification believes -- a key rotated out, or the deployment's for a
	// verifier that is not given its private half -- each PKIX, base64, or
	// the PEM document of one.
	Trust []string `yaml:"trust"`
}

// keys is the checkpoints' keys, read.
func (c AuditCheckpointsConfig) keys() (crypto.Signer, []crypto.PublicKey, error) {
	trust := []crypto.PublicKey{}
	for i, v := range c.Trust {
		k, err := trail.ParsePublicKey(v)
		if err != nil {
			return nil, nil, fmt.Errorf("trust[%d]: %w", i, err)
		}

		trust = append(trust, k)
	}

	// Last, since a key in a device is a session opened; see [signerOf].
	key, err := signerOf(c.Key)
	if err != nil {
		return nil, nil, fmt.Errorf("key: %w", err)
	}

	return key, trust, nil
}

// AuditMinConfig is a floor: the least a deployment keeps, whatever a tenant's
// answer says.
//
// It is the deployment's obligations, as distinct from its windows. An access
// log the deployment must keep a year is not a customer's to shorten, so a
// tenant whose plan says ninety days is kept a year anyway, and the raise is
// logged. The deployment's own windows are held to it as well, and one below
// it is refused where the process comes up.
//
// Empty is no floor, on either clock -- the opposite of a window, whose empty
// is forever.
type AuditMinConfig struct {
	// Profile fills the floor in from a named regime. Most of them are floors
	// when read closely -- *at least a year* -- which is what this is for.
	// `forever` is refused: it is a window and not a floor.
	Profile string `yaml:"profile"`

	// Retain is the least time a row stays in the database.
	Retain time.Duration `yaml:"retain"`

	// Destroy is the least time a row exists at all.
	Destroy time.Duration `yaml:"destroy"`
}

// AuditKeepConfig is one kind's two clocks.
//
// It has no `archive` and no `every`: where the files go and how often the
// policy runs are the deployment's, and one directory read by one loop is what
// makes an archive readable as a whole. What differs per kind is how long.
type AuditKeepConfig struct {
	// Profile is a named regime for this kind alone. `forever` is one of them,
	// which is the usual answer for a record of what a machine did.
	Profile string `yaml:"profile"`

	// Retain is how long a row of this kind stays in the database.
	Retain time.Duration `yaml:"retain"`

	// Discard removes this kind with no copy kept, whatever `audit.archive`
	// says.
	Discard bool `yaml:"discard"`

	// Destroy is how long a row of this kind exists at all.
	Destroy time.Duration `yaml:"destroy"`

	// Min is this kind's floor, on top of `audit.min`.
	Min AuditMinConfig `yaml:"min"`

	// Content is how much of a write to this kind a row of the trail holds,
	// over `audit.content`. See [AuditConfig.Content].
	Content string `yaml:"content"`
}

// Contents is what the recorder keeps of each kind's writes, read and checked:
// the generated `Recorder` takes it, which is how `content:` reaches the trail.
//
// A method of its own rather than a part of [AuditConfig.Policy], because the
// two are read by different things: the policy by what moves and destroys the
// trail's rows, this by what writes them, inside every write. What it refuses
// is the same as there -- a word that is not a level, and a kind this app does
// not have -- for the same reason: a trail told to keep more than it keeps is
// found out by somebody looking for what is not there.
func (c AuditConfig) Contents() (audit.Contents, error) {
	out := audit.Contents{}

	v, err := audit.ParseContent(c.Content)
	if err != nil {
		return out, fmt.Errorf("content: %w", err)
	}
	out.Default = v

	for name, k := range c.By {
		if strings.TrimSpace(k.Content) == "" {
			continue
		}

		d, err := trail.DomainOf(name)
		if err != nil {
			return out, fmt.Errorf("by.%s: %w", name, err)
		}
		v, err := audit.ParseContent(k.Content)
		if err != nil {
			return out, fmt.Errorf("by.%s.content: %w", name, err)
		}
		if out.By == nil {
			out.By = map[pdid.Domain]audit.Content{}
		}
		out.By[d] = v
	}

	d, err := c.Digest.digest()
	if err != nil {
		return out, fmt.Errorf("digest: %w", err)
	}
	out.Digest = d

	return out, nil
}

// Policy is this block as the thing that applies it, resolved and checked.
//
// Both here rather than in a `Valid` somebody has to remember to call, which is
// the idiom every other block in this package already has: a refusal lives
// inside the call that makes the object. What it refuses is a policy that would
// destroy something nobody asked it to, and a kind this app does not have --
// see [trail.Policy.Valid]. Read it where the process comes up rather than at
// the first sweep a day later.
func (c AuditConfig) Policy() (trail.Policy, error) {
	keep, err := keepOf(c.Profile, trail.Keep{
		Retain:  c.Retain,
		Discard: c.Discard,
		Destroy: c.Destroy,
	})
	if err != nil {
		return trail.Policy{}, fmt.Errorf("audit.%w", err)
	}

	least, err := floorOf(c.Min)
	if err != nil {
		return trail.Policy{}, fmt.Errorf("audit.min.%w", err)
	}

	p := trail.Policy{
		Every: c.Every,
		Keep:  keep,
		Min:   least,
	}
	if c.Archive != "" {
		p.Archive = flob.NewOsStores(c.Archive)
	}

	if len(c.By) > 0 {
		p.By = map[pdid.Domain]trail.Keep{}
	}
	for name, v := range c.By {
		d, err := trail.DomainOf(name)
		if err != nil {
			return trail.Policy{}, fmt.Errorf("audit.by: %w", err)
		}

		// A block that says nothing but a floor is a floor and no window: the
		// kind keeps what everything else keeps, held to its own minimum.
		if v.window() {
			k, err := keepOf(v.Profile, trail.Keep{
				Retain:  v.Retain,
				Discard: v.Discard,
				Destroy: v.Destroy,
			})
			if err != nil {
				return trail.Policy{}, fmt.Errorf("audit.by.%s.%w", name, err)
			}

			p.By[d] = k
		}

		f, err := floorOf(v.Min)
		if err != nil {
			return trail.Policy{}, fmt.Errorf("audit.by.%s.min.%w", name, err)
		}
		if f.On() {
			if p.MinBy == nil {
				p.MinBy = map[pdid.Domain]trail.Floor{}
			}

			p.MinBy[d] = f
		}
	}

	if p.Signer, p.Trust, err = c.Checkpoints.keys(); err != nil {
		return trail.Policy{}, fmt.Errorf("audit.checkpoints.%w", err)
	}
	if c.Checkpoints.Dir != "" {
		p.Checkpoints = flob.NewOsStores(c.Checkpoints.Dir).Use(trail.CheckpointNamespace)
		if c.Checkpoints.WriteOnce {
			p.Checkpoints = trail.WriteOnce(p.Checkpoints)
		}
	} else if c.Checkpoints.WriteOnce {
		return trail.Policy{}, errors.New("audit.checkpoints.write_once: the checkpoints are kept in the archive, " +
			"which a pass erases from; give them a dir of their own")
	}
	if p.Keys, err = c.Keys.keys(c.Archive); err != nil {
		return trail.Policy{}, fmt.Errorf("audit.keys.%w", err)
	}

	if err := p.Valid(); err != nil {
		return trail.Policy{}, err
	}

	return p, nil
}

// window answers whether a kind's block says anything about its own windows,
// as opposed to only its floor.
func (c AuditKeepConfig) window() bool {
	return c.Profile != "" || c.Retain != 0 || c.Discard || c.Destroy != 0 || !c.Min.set()
}

func (c AuditMinConfig) set() bool { return c.Profile != "" || c.Retain != 0 || c.Destroy != 0 }

func floorOf(c AuditMinConfig) (trail.Floor, error) {
	f := trail.Floor{Retain: c.Retain, Destroy: c.Destroy}
	if c.Profile == "" {
		return f, nil
	}

	v, err := trail.NamedProfile(c.Profile)
	if err != nil {
		return trail.Floor{}, err
	}

	return v.OverFloor(f)
}

func keepOf(profile string, k trail.Keep) (trail.Keep, error) {
	if profile == "" {
		return k, nil
	}

	v, err := trail.NamedProfile(profile)
	if err != nil {
		return trail.Keep{}, err
	}

	return v.Over(k), nil
}
