/**
 * The server these tests send bytes to.
 *
 * It is a real server on purpose -- a mock would agree with whatever the
 * TypeScript believes, which is the one thing not worth checking -- so what a
 * test file needs is a process, an address to reach it at, and a way to stop
 * it.
 *
 * # Built once, before any of them, which is the whole of why this exists
 *
 * `go run` compiles and then runs, so a `go run` waited on with a deadline is a
 * deadline over a compilation. That is fine on a warm build cache and is not
 * fine on a cold one: the cache key includes `go.sum`, so the first run after
 * any dependency moves pays for the whole app -- twice over, because the two
 * server-backed files run at once and each was compiling its own copy. It went
 * over a minute in CI and both failed saying the server printed no address,
 * which was true and said nothing about why.
 *
 * So the build is [setup], which vitest runs once before any worker starts and
 * holds to no per-test deadline, and what a file does is start what was built.
 * The deadline stays on the part it was written for: a server that started and
 * did not say where.
 *
 * It builds for every run, including `test:sandbox`, which runs the browser
 * file alone and uses no server. That is a warm cache and a second of it. A
 * setup that skipped the build by reading which script invoked it would be a
 * setup that guesses, and the run it guessed wrong about is the one where the
 * server-backed files fail having been told nothing built one.
 */

import { execFile, spawn, type ChildProcessWithoutNullStreams } from 'node:child_process'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'

const run = promisify(execFile)

const here = dirname(fileURLToPath(import.meta.url))

/** repo is the checkout, which is where `go` is run from. */
export const repo = resolve(here, '../../../..')

/**
 * bin names the built server, in the environment because that is what a
 * `globalSetup` and a test worker share -- the setup runs in its own process
 * and returns before any worker exists.
 */
const bin = 'PDTEST_TESTSRV'

/**
 * setup builds the server and takes it away afterwards. It is vitest's
 * `globalSetup`; see the note above for why the build is here rather than in
 * the file that starts one.
 *
 * The binary goes to a directory of its own rather than into the checkout: a
 * test run that leaves an executable behind has changed the tree it was run
 * against.
 */
export default async function setup(): Promise<() => Promise<void>> {
	const dir = await mkdtemp(join(tmpdir(), 'payday-testsrv-'))
	const path = join(dir, 'testsrv')

	await run('go', ['build', '-o', path, './internal/apptest/testsrv'], { cwd: repo })
	process.env[bin] = path

	return async () => {
		await rm(dir, { recursive: true, force: true })
	}
}

/** Server is one running `testsrv`, and where to reach it. */
export interface Server {
	addr: string
	stop: () => void
}

/** start runs the built server and answers once it has said its address. */
export async function start(): Promise<Server> {
	const path = process.env[bin]
	if (path === undefined) {
		throw new Error(`${bin} is not set, so nothing built the server; see globalSetup`)
	}

	const p = spawn(path, [], { cwd: repo })

	return { addr: await said(p), stop: () => p.kill() }
}

/** said answers with the address the server printed, or gives up loudly. */
function said(p: ChildProcessWithoutNullStreams): Promise<string> {
	return new Promise((ok, no) => {
		const t = setTimeout(() => no(new Error('the server printed no address')), 60_000)
		let buf = ''

		p.stdout.on('data', (b: Buffer) => {
			buf += b.toString()
			const i = buf.indexOf('\n')
			if (i < 0) return

			clearTimeout(t)
			ok(buf.slice(0, i).trim())
		})
		p.on('error', no)
		p.stderr.on('data', (b: Buffer) => process.stderr.write(b))
	})
}
