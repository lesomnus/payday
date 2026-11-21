/** A device keeps effects across operator changes; each operator keeps a separate read cache. */
import { digest } from './digest.js'
import type { Queue } from './disk.js'
import { Writes } from './writes.js'

/** An app-owned, non-secret reference. The server verifies its binding to the device and person. */
export interface Presence {
	readonly reference: string
	readonly subject: string
}

/** Constructed by DeviceQueue.represent; use identity for the person's Store. */
export interface Representation {
	readonly device: DeviceQueue
	readonly presence: Presence
	readonly identity: string
}

export interface DeviceOpts {
	readonly name: string
	/** The device credential's identity, never the credential itself. */
	readonly identity: string
	readonly disk?: Queue & { close(): void }
}

export class DeviceQueue {
	readonly name: string
	readonly identity: string
	readonly writes: Writes
	private readonly disk: DeviceOpts['disk']
	private readonly listeners = new Set<() => void>()

	constructor(opts: DeviceOpts) {
		this.name = `${opts.name}:${opts.identity}`
		this.identity = opts.identity
		this.disk = opts.disk
		this.writes = new Writes(opts.disk, () => { for (const f of this.listeners) f() })
	}

	async represent(presence: Presence): Promise<Representation> {
		if (!presence.reference || !presence.subject) throw new Error('device: presence requires a reference and subject')
		const held = Object.freeze({ reference: presence.reference, subject: presence.subject })
		const identity = await digest(JSON.stringify(['payday/presence', this.identity, held.subject, held.reference]))
		return Object.freeze({ device: this, presence: held, identity })
	}

	subscribe(cb: () => void): () => void {
		this.listeners.add(cb)
		return () => { this.listeners.delete(cb) }
	}

	/** Reloads writes without reading any person's rows. */
	hydrate(): Promise<void> { return this.writes.reload() }

	/** Only device sign-out discards the device's unsent effects. Operator sign-out calls Store.forget. */
	async forget(): Promise<void> {
		this.writes.clear()
		await this.disk?.clear()
	}

	/** Close after outstanding sends finish. This keeps the queue for a reload. */
	close(): void { this.disk?.close() }
}
