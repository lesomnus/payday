# Refusals

A server talking to a server needs one sentence. A form does not — it has to
know which box to draw the red line around.

```
rpc error: code = InvalidArgument desc = slug.tenant: alias: must not be empty
```

There is no way to get the field out of that except by parsing it, which turns
the message into a wire format nobody declared and makes every rewording of it
break a page. So the field path travels **beside** the message, in the
`google.rpc.BadRequest` that is already in the dependency tree and already
understood by every gRPC tool.

- [1. Refusing, on the server](#1-refusing-on-the-server)
- [2. Building a path up](#2-building-a-path-up)
- [3. Reading it, on the page](#3-reading-it-on-the-page)
- [4. The words are yours](#4-the-words-are-yours)
- [5. What this is not for](#5-what-this-is-not-for)

## 1. Refusing, on the server

```go
import "github.com/lesomnus/payday/pderr"

if req.GetAlias() == "" {
	return nil, pderr.Invalidf("alias", "must not be empty")
}
```

Several at once, which is what a form wants — one round trip, every box marked:

```go
return nil, pderr.Invalid(
	pderr.Violation{Field: "alias", Why: "must not be empty"},
	pderr.Violation{Field: "labels", Why: "at most 16"},
)
```

Both answer `InvalidArgument` with the violations attached as details. Nothing
about the transport changes: `grpcurl` shows them, and a Go client that knows
nothing about payday still gets a sensible `status.Error`.

## 2. Building a path up

A field path is relative to the request, and a request is a tree. When you
validate part of a request by handing it to something that validates that part,
what comes back is a refusal about `alias` that belongs at `slug.tenant.alias`.

`At` is that step:

```go
if err := validateSlug(req.GetSlug()); err != nil {
	return nil, pderr.At("slug", err)
}
```

It moves everything the error carries underneath a name. This is the only way
violations are meant to be built up — doing it by hand is how a path ends up
naming a box that is not there.

The generated servers already work this way. `HolderPick` validates a
`HolderRefBySlug` by calling `TenantPick` on part of it, and the path comes out
right without either of them knowing where they sit.

### `At` will not change what kind of error it is

An error that already answers `Unimplemented` or `NotFound` comes back
untouched. The code is what a caller acts on — retry, go get a credential, give
up — and a wrapper that overwrote it would be telling them to go and fix a field
over something that was never about a field.

## 3. Reading it, on the page

```ts
import { isInvalid, messages } from '@lesomnus/payday/pderr'
```

`isInvalid` is the check a form makes first:

```ts
try {
	await client.add(req)
} catch (err) {
	if (!isInvalid(err)) throw err     // gone, not allowed, not reachable
	setErrors(messages(err, say))
}
```

Everything that is not `InvalidArgument` is a different thing to show, and none
of it belongs under a box.

`messages` gives you a `Map<string, string[]>` keyed by field path, ready to
index by input name:

```tsx
<input name="alias" />
{errors.get('alias')?.map((m) => <span key={m}>{m}</span>)}
```

There is also `violations(err)` for the raw list and `byField(err)` if you want
the developer prose unchanged.

## 4. The words are yours

`messages` takes a table:

```ts
function say(field: string, why: string): string {
	switch (field) {
		case 'alias':
			return 'Pick a short name — letters, numbers and hyphens.'
		case 'slug.tenant.alias':
			return 'That organisation name does not look right.'
	}

	return 'Check this field.'
}
```

payday does not carry a language, a tone, or a set of translations, and a
framework that starts writing sentences for end users has taken on all three.
What the UI matches against is the **code and the field path**; the words are
the app's.

That is also why the fallback is `say`'s to decide. A violation whose path your
table does not recognise should get your general wording rather than leaking
developer prose onto a page — which is what happens when a UI shows
`err.message` because the field was not in its table.

## 5. What this is not for

`pderr` is about **the request being wrong**. It is not:

- **Authorization.** "You may not do this" is `PermissionDenied`, and it is not
  about a field. See [permissions](permissions.md).
- **A missing row.** `NotFound`, which the generated servers already answer.
- **A version conflict.** A patch whose `date_updated` no longer matches answers
  `FailedPrecondition` — *"a test in the patch did not hold"* — and what a page
  does about it is re-read and show the difference, not draw a red line.

If you find yourself reaching for `Invalidf` to say one of those, the code is
the thing carrying the meaning and changing it will break a caller that was
acting on it correctly.

## Where to go next

- [The page](client.md) — where a refusal ends up.
- [The server](server.md) — where validation runs, and in which layer.
