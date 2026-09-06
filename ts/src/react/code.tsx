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
	layout(): void
	updateOptions(opts: Record<string, unknown>): void
	dispose(): void
}

const style = {
	box: { width: '100%', height: '100%', minHeight: 0 },
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
	value: string
	schema: Schema | undefined
	readOnly: boolean
	onChange: (v: string) => void
}): ReactNode {
	const box = useRef<HTMLDivElement>(null)

	// The callback, kept where the effect can read the current one. Rebuilding
	// the editor when a parent re-renders would throw away the caret, the
	// folding and the undo history on every keystroke somewhere else.
	const onChange = useRef(props.onChange)
	onChange.current = props.onChange

	useEffect(() => {
		const el = box.current
		if (el === null) return

		// A scheme, because `fileMatch` is matched against the model's URI as a
		// string: a bare `app.Tenant.json` parses to a URI with no scheme and
		// matches nothing, which is a schema that is registered and never
		// applies -- an editor with completion and validation silently off.
		const uri = props.monaco.Uri.parse(`inmemory://payday/${props.uri}`)

		props.monaco.json?.setDiagnosticsOptions({
			validate: true,
			schemas:
				props.schema === undefined
					? []
					: [{ uri: `payday://schema/${props.uri}`, fileMatch: [uri.toString()], schema: props.schema }],
		})

		const model = props.monaco.editor.createModel(props.value, 'json', uri)
		const editor = props.monaco.editor.create(el, {
			model,
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
		})

		const sub = model.onDidChangeContent(() => onChange.current(model.getValue()))

		return () => {
			sub.dispose()
			editor.dispose()
			model.dispose()
		}
		// The document, and not what is being typed into it: `value` is the
		// answer that arrived, and re-creating the model on every keystroke is
		// what makes an editor impossible to type in.
	}, [props.monaco, props.uri, props.readOnly]) // eslint-disable-line react-hooks/exhaustive-deps

	return <div ref={box} style={style.box} />
}
