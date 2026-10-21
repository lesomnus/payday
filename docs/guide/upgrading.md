# Upgrading

Two things move under an app: payday, and the packages its page is built with.
`pd upgrade` moves both by running the tools that own them — `go` for payday,
`npm` for the page — rather than by editing what those tools write, and it
commits nothing.

```sh
go tool pd upgrade --payday latest   # payday, then what that payday needs of the page
go tool pd upgrade                   # what this payday needs of the page
go tool pd upgrade --dry-run         # what would run
go tool pd versions .                # the floors, the targets, and what this app has
```

---

## 1. Floors and targets

For each page package payday depends on, it knows two versions:

- **the floor**: the oldest version payday builds a page with;
- **the target**: the version `pd new` writes today.

`pd upgrade` moves a package only when it is **below its floor**, and moves it
**to its target**. A package at or above its floor is left alone, however far
behind the target it is. A floor is where payday's code stops being something
payday has built. Anything above that is the app's business, and Renovate's.

| package | why it has a floor |
| --- | --- |
| `@bufbuild/protobuf`, `@bufbuild/protoc-gen-es` | the generator `pd gen --ts` writes with, and the runtime its code imports |
| `@connectrpc/connect`, `@connectrpc/connect-web` | the client a page makes of the services, and the browser's transport for it |
| `@lesomnus/payday` | payday's half of the page, which the template's `src` is written against |
| `typescript` | what payday's declarations and the template's `tsconfig.json` are compiled with |
| `vite`, `@vitejs/plugin-react` | the `vite.config.ts` that `pd new` and `pd sandbox init` write |

`go tool pd versions` prints the numbers, with the app's own beside them. A `!`
marks one that is below its floor, and `--json` gives the same rows to a
script. Node has a floor as well, but `pd upgrade` only reports it: it is the
machine's to install.

### Why the numbers can be trusted

- **A target is the template's.** It is what `pd new` writes. Renovate keeps it
  current (a regex manager reads `package.json.tmpl`), and CI's `new` job builds
  an app with it.
- **A floor is a version payday has built a page with.** CI's `floor` job:
  1. makes an app with `pd new`;
  2. writes every floor into its `package.json` exactly;
  3. installs from nothing on Node 22, then generates and builds.

  The first run of that job found a floor that npm never published: Vite 7.1.14,
  the lower bound the template had declared. That is why a floor is built rather
  than written down.
- **A floor is raised when payday comes to need something newer**, never because
  something newer exists. Raising one is a change to `pdcli.Floors`, and the
  same job proves it.

## 2. What moves together

Three pairs move as one:

- `vite` and `@vitejs/plugin-react`;
- `@bufbuild/protobuf` and `@bufbuild/protoc-gen-es`;
- `@connectrpc/connect` and `@connectrpc/connect-web`.

A major release of one is a major release of the other, and npm refuses the half
of a pair that moved alone. So when one of a pair is below its floor, both go to
their targets in a single `npm install`. There is one install for each section
of `package.json` the page keeps them in, so a dev dependency stays one.

## 3. Where the floors are enforced

- **`pd doctor`** says which packages are below their floors. These findings
  stop nothing.
- **`pd gen --ts`** refuses to write with a generator, or against a runtime, that
  is below its floor (`pdcli.ErrBelowFloor`). What it writes is committed and
  then compiled against that runtime, so an older generator's output would be
  found out at somebody else's compile.
- **Nothing else.** A page below the floor of `typescript` or `vite` still
  generates. Only `pd doctor` says so.

## 4. Upgrading payday

`--payday VERSION` runs, in order:

1. `go get github.com/lesomnus/payday@VERSION`;
2. `go mod tidy`;
3. `go tool pd upgrade`;
4. `go tool pd gen --ts` when the app has a page, which writes the Go half as
   well; `go tool pd gen` when it has none.

Steps 3 and 4 run the payday that step 1 fetched, not the one you started
with. Its floors are the ones the page has to meet, and code that payday's
generator writes is the only code its runtime accepts (`version.Same`).

`VERSION` is anything `go get` takes: `latest`, a tag, or a commit. It is
`go get` without `-u`, so payday's own dependencies move to what payday
requires and no further.

## 5. What it does not do

- **Commit.** What moved stays in the working tree: `go.mod`, `go.sum`,
  `package.json`, the lockfile, and the regenerated code. `git diff` shows it.
- **Add a package the page does not have**, or guess at a version it cannot
  read. A range like `latest` with nothing installed is one it leaves alone.
- **Move Node, a Dockerfile's base image, or the Node your CI runs.** Those are
  files and machines payday does not own.

## 6. Renovate, for the rest

Keeping current above the floors is Renovate's. An app made with `pd new` has a
`renovate.json` that extends payday's preset, `github>lesomnus/payday//renovate/app`
(`renovate/app.json` in this repository). An app made before that can extend the
same preset. The preset does these things:

- **It waits three days before it proposes a release** of an npm package or a
  Go module: time for a release published to do harm to be found and pulled,
  and the 72 hours in which npm lets a release be unpublished. That is what
  Renovate's own `security:minimumReleaseAgeNpm` waits. An update still
  waiting is on the dependency dashboard under "Pending Status Checks", where a
  tick opens it anyway. A fix for a known vulnerability is not held. A Go
  pseudo-version waits from the time in its name, which is its commit's.
- **It pins every GitHub Action by commit** (`helpers:pinGitHubActionDigests`).
  A tag is moved by whoever can push to the action's repository, and a commit
  is not. A workflow that names an action by its tag gets a pull request that
  names the commit instead, with the tag after it as a comment, and from then
  on an update moves the two together.
- **It asks osv.dev about vulnerabilities as well as GitHub**
  (`osvVulnerabilityAlerts`), and proposes the fix for each, when there is
  one, at once and whatever the app's schedule says.
- **It leaves payday and protobuf-orm to `pd upgrade --payday`.** An upgrade of
  either has to generate the app's code again. Renovate cannot generate, and
  code another payday generated is refused when it runs, so a pull request that
  only moves `go.mod` would fail every time.
- **It holds the generators until somebody asks for them.** `protoc-gen-go`,
  `protoc-gen-go-grpc` and `protoc-gen-es` write their version into every file
  they write, so a pull request that moves one fails `pd gen --check` until it
  is generated on. They wait on the dependency dashboard, and a tick there opens
  their pull request, `generators`, which says to run `go tool pd gen --ts .`
  on its branch. `@bufbuild/protobuf` waits with them: `protoc-gen-es` names
  its own release of it exactly. In roster's `non-major`, `protoc-gen-es` 2.15
  to 2.16 held twenty other updates back.

  The Go two also move with payday. `pd upgrade --payday` runs `go get`, which
  raises them to what payday requires when that is newer, and then generates.
- **It keeps vite and @vitejs/plugin-react in one pull request**, for the reason
  in §2.
- **It groups patches, digests, and minors from 1.0 on, into one `non-major`
  pull request.** A minor of a package before 1.0 is allowed to break, so it
  gets a pull request of its own and cannot hold the rest of the group back.
  go-webauthn's 0.17 to 0.18 changed `Verify` under roster, and held every
  other update in its group back.

  But golang.org/x is one pull request, `golang.org/x`. Every module of it is
  before 1.0 and most of them are tagged together about once a month, so a pull
  request each was four in one week in roster -- x/crypto, x/image, x/oauth2,
  x/sync -- and each conflicted in `go.mod` and `go.sum` with whichever was
  merged before it.

The rest of a Renovate configuration stays the app's: its schedule, its
reviewers, and rules of its own. A rule in the app's own `packageRules` is read
after the preset's and wins.

