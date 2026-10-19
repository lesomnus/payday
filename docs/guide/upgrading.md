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
