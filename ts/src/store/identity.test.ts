import { describe, expect, it } from 'vitest'

import { identityOf } from './identity.js'

describe('identityOf', () => {
	// The same vector is in `auth/authsession/authsession_test.go`: a page signed
	// in by cookie is told this name by the server rather than working it out,
	// and the two must not drift apart.
	it('is sixteen hex characters of the SHA-256', async () => {
		expect(await identityOf('abc')).toBe('ba7816bf8f01cfea')
	})

	it('is never the credential', async () => {
		const v = await identityOf('@acme/admin')
		expect(v).not.toContain('acme')
		expect(v).toMatch(/^[0-9a-f]{16}$/)
	})
})
