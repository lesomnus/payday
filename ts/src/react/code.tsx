/**
 * A document in an editor, when the app brought one.
 *
 * # Why the app brings it
 *
 * Monaco is fifteen megabytes and it needs web workers whose URLs only a
 * bundler can write. payday cannot import it: a dependency would put it in
 * every app that imports the panel, an optional dynamic `import` would still
 * make a bundler resolve the name at build time, and neither can set the
 * `MonacoEnvironment` a worker is found through -- that is the app's, the same
 * way the sandbox's worker file is.
 *
 * So it is handed in, and absent is the ordinary case:
 *
 *     import * as monaco from 'monaco-editor'
 *     <Devtools entities={entities} monaco={monaco} />
 *
 * Without it the panel shows the same document as coloured text that cannot be
 * edited, which is what it did before there was one.
 *
 * @module
 */

import { useEffect, useRef, type CSSProperties, type ReactNode } from 'react'

import type { Schema } from './jsonschema.js'

/**
 * What is asked of `monaco`, written out rather than imported.
 *
 * A type-only import would be a devDependency on fifteen megabytes for four
 * signatures, and this list is also the documentation of how little is being
 * used -- `store/idb.ts` and `sandbox/index.ts` do the same for the browser
 * globals they need.
 */
export interface MonacoLike {
	readonly editor: {
		create(el: HTMLElement, opts: Record<string, unknown>): Editor
		/**
		 * A diff editor, answered as `unknown` and narrowed where it is used.
		 *
		 * Its `setModel` cannot be described here. The parameter is checked
		 * against monaco's own, and the only type that passes is `ITextModel`
		 * itself -- sixty-odd members transcribed to say a thing this file
		 * calls once, and stale the next release. So the narrowing is one cast
		 * at the call site instead.
		 */
		createDiffEditor(el: HTMLElement, opts: Record<string, unknown>): unknown
		createModel(value: string, language: string, uri: unknown): Model
	}

	readonly Uri: { parse(v: string): { toString(): string } }

	/**
	 * Where a JSON Schema is registered, which is `jsonDefaults` from
	 * `monaco-editor/esm/vs/language/json/monaco.contribution`.
	 *
	 * Asked for separately rather than reached through `monaco.languages.json`,
	 * which is deprecated and typed as `{ deprecated: true }` -- so a panel
	 * that read it there would compile against one version of monaco and not
	 * the next. Optional because an editor with no schema is still an editor:
	 * what is lost is the completion and the red line, not the typing.
	 */
	readonly json?: {
		setDiagnosticsOptions(opts: {
			validate?: boolean
			schemas?: { uri: string; fileMatch?: string[]; schema?: unknown }[]
		}): void
	}
}

interface Model {
	getValue(): string
	setValue(v: string): void
	onDidChangeContent(cb: () => void): { dispose(): void }
	dispose(): void
}

interface Editor {
	dispose(): void
}

interface DiffEditor {
	setModel(v: { original: Model; modified: Model }): void
	dispose(): void
}

const style = {
	box: { width: '100%', height: '100%', minHeight: 0, display: 'flex' },
	half: { flex: 1, minWidth: 0, height: '100%' },
} satisfies Record<string, CSSProperties>

/**
 * Code is the document, edited.
 *
 * The model is keyed by `uri` and the schema is registered against the same
 * one, which is how the JSON language service knows which schema applies to
 * which document: a panel showing a Robot and then a Tenant is two documents,
 * and one file name for both would validate the second against the first.
 */
export function Code(props: {
	monaco: MonacoLike
	uri: string

	/**
	 * The document to open on, read **once**.
	 *
	 * A new answer is a new editor: give this a `key` that changes with the
	 * document and let React remount it. Writing a later `value` into the
	 * model instead cannot be made correct -- what arrives here while somebody
	 * types is this editor's own text one render behind, and putting that back
	 * throws away the keystrokes that came after it. There is no way from in
	 * here to tell a stale echo from an answer, and guessing wrong eats
	 * characters.
	 */
	value: string

	/**
	 * What the value was, for a diff against it beside the editor.
	 *
	 * Absent is one editor. Given, the region is halved: the document is typed
	 * into on the **left**, and the right is an inline diff of it against this,
	 * read-only.
	 *
	 * That way round rather than Monaco's own side-by-side, where the editable
	 * half is the right one: typing belongs where reading starts, and a diff is
	 * something to glance at. It is also why the right is a second editor and
	 * not the same widget -- one diff editor cannot be half editable and half
	 * not, and its editable half is the one it draws the diff in.
	 *
	 * The diff is behind a short delay; see [Code.debounceMs]. Recomputing it
	 * per keystroke costs nothing anybody sees and makes the right-hand side
	 * flicker through every half-typed word.
	 */
	original?: string
	schema: Schema | undefined
	readOnly: boolean
	onChange: (v: string) => void

	/** How long the diff waits for typing to stop. */
	debounceMs?: number
}): ReactNode {
	const box = useRef<HTMLDivElement>(null)

	// Kept in a ref and left out of the deps below, because an editor is not
	// something anything swaps mid-life -- and because a caller writing
	// `monaco={{ editor, Uri, json }}` inline hands over a new object on every
	// render, which would rebuild the editor between two keystrokes. What that
	// looks like is typing that arrives out of order and half missing.
	const api = useRef(props.monaco)
	api.current = props.monaco

	// The models, kept so that they can be disposed of.
	const mine = useRef<Model>(null)

	/** Where the diff goes, when there is one. */
	const other = useRef<HTMLDivElement>(null)

	// The callback, kept where the effect can read the current one rather than
	// the one from the render that built the editor.
	const onChange = useRef(props.onChange)
	onChange.current = props.onChange

	const split = props.original !== undefined
	const wait = props.debounceMs ?? 300

	useEffect(() => {
		const el = box.current
		const el2 = other.current
		if (el === null) return

		const monaco = api.current

		// A scheme, because `fileMatch` is matched against the model's URI as a
		// string: a bare `app.Tenant.json` parses to a URI with no scheme and
		// matches nothing, which is a schema that is registered and never
		// applies -- an editor with completion and validation silently off.
		const uri = monaco.Uri.parse(`inmemory://payday/${props.uri}`)

		monaco.json?.setDiagnosticsOptions({
			validate: true,
			schemas:
				props.schema === undefined
					? []
					: [{ uri: `payday://schema/${props.uri}`, fileMatch: [uri.toString()], schema: props.schema }],
		})

		const opts = {
			readOnly: props.readOnly,
			automaticLayout: true,
			minimap: { enabled: false },
			scrollBeyondLastLine: false,
			fontSize: 12,
			fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
			theme: 'vs-dark',
			tabSize: 2,
			renderLineHighlight: 'none',
			overviewRulerLanes: 0,
		}

		const live = monaco.editor.createModel(props.value, 'json', uri)
		mine.current = live

		const editor = monaco.editor.create(el, { ...opts, model: live })

		const shut: (() => void)[] = [
			() => {
				editor.dispose()
				live.dispose()
				mine.current = null
			},
		]

		if (split && el2 !== null) {
			// Two more models. The right-hand side compares what was read
			// against a **copy** of what is being typed, and the copy is what
			// the delay is on -- the live model cannot be the one it reads,
			// because then there is no delay.
			const was = monaco.editor.createModel(
				props.original ?? '',
				'json',
				monaco.Uri.parse(`inmemory://payday/${props.uri}.was`),
			)
			const snap = monaco.editor.createModel(
				props.value,
				'json',
				monaco.Uri.parse(`inmemory://payday/${props.uri}.now`),
			)

			const diff = monaco.editor.createDiffEditor(el2, {
				...opts,
				readOnly: true,
				originalEditable: false,

				// Inline, because this is half the width: two columns in it
				// would be four on the screen for one document.
				renderSideBySide: false,
			}) as DiffEditor
			diff.setModel({ original: was, modified: snap })

			let timer: ReturnType<typeof setTimeout> | undefined
			const later = live.onDidChangeContent(() => {
				if (timer !== undefined) clearTimeout(timer)
				timer = setTimeout(() => snap.setValue(live.getValue()), wait)
			})

			shut.unshift(() => {
				if (timer !== undefined) clearTimeout(timer)
				later.dispose()
				diff.dispose()
				snap.dispose()
				was.dispose()
			})
		}

		const sub = live.onDidChangeContent(() => onChange.current(live.getValue()))
		shut.unshift(() => sub.dispose())

		return () => {
			for (const f of shut) f()
		}
		// Not on `value`: that is what is being typed, and rebuilding the
		// editor on every keystroke is what makes one impossible to type in.
	}, [props.uri, props.readOnly, split, wait]) // eslint-disable-line react-hooks/exhaustive-deps

	return (
		<div style={style.box}>
			<div ref={box} style={style.half} />
			{split && <div ref={other} style={{ ...style.half, borderLeft: '1px solid #2c2c2c' }} />}
		</div>
	)
}
