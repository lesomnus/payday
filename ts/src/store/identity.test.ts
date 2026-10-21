import { describe, expect, it } from 'vitest'

import { identityOf } from './identity.js'

describe('identityOf', () => {
	it('is sixteen hex characters of the SHA-256', async () => {
		expect(await identityOf('abc')).toBe('ba7816bf8f01cfea')
	})

	it('is never the credential', async () => {
		const v = await identityOf('@acme/admin')
		expect(v).not.toContain('acme')
		expect(v).toMatch(/^[0-9a-f]{16}$/)
	})
})
