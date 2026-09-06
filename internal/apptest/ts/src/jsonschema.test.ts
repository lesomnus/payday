/**
 * The JSON Schema a descriptor already implies, over this app's own schema.
 *
 * Here rather than in payday's package for the same reason `store.test.ts` is:
 * what is worth testing is the pair. A schema written to suit the generator
 * would prove nothing about whether a real one -- with edges that point back,
 * a map, a `oneof` and a `Timestamp` -- comes out as something an editor can
 * use.
 */

import { describe, expect, it } from 'vitest'

import { jsonSchemaOf } from '@lesomnus/payday/react/jsonschema'

import { AuditSchema } from '../gen/app/payday/audit_pb.js'
import { RobotSchema } from '../gen/app/robot_pb.js'
import { TenantSchema } from '../gen/app/payday/tenant_pb.js'
import { RobotService } from '../gen/app/robot_svc_pb.js'

type Obj = Record<string, unknown>

function defs(v: Obj): Obj {
	return v.definitions as Obj
}

function of(v: Obj, name: string): Obj {
	return (defs(v)[name] as Obj).properties as Obj
}

describe('a message becomes a schema', () => {
	it('points at itself, and puts everything it reaches beside it', () => {
		const got = jsonSchemaOf(RobotSchema) as Obj

		expect(got.$ref).toBe('#/definitions/app.Robot')
		expect(Object.keys(defs(got))).toContain('app.Robot')

		// The edge, which is a message and so is a definition of its own --
		// and the reason this is not written inline: a Cell has a Tenant and a
		// Tenant is reached from three places.
		expect(Object.keys(defs(got))).toContain('app.Tenant')
	})

	it('is the fields as JSON names them', () => {
		const p = of(jsonSchemaOf(TenantSchema) as Obj, 'app.Tenant')

		expect(Object.keys(p)).toContain('dateUpdated')
		expect(Object.keys(p), 'the proto spelling is not what anything here emits').not.toContain('date_updated')
	})

	it('says what a scalar is, and what a 64-bit one is', () => {
		const p = of(jsonSchemaOf(AuditSchema) as Obj, 'app.Audit')

		expect(p.action).toEqual({ type: 'string' })

		// Bytes are base64 in protobuf JSON, and saying `string` without that
		// is a hover that does not answer the only question anybody has.
		expect(p.id).toEqual({ type: 'string', description: 'base64' })
	})

	it('leaves a well-known type as what JSON makes of it', () => {
		const p = of(jsonSchemaOf(TenantSchema) as Obj, 'app.Tenant')

		// Not a `$ref` to `google.protobuf.Timestamp`. In JSON it is a string,
		// and a schema pointing at the message would flag every correct
		// document and complete `seconds` and `nanos` into it.
		expect(p.dateUpdated).toEqual({ type: 'string', format: 'date-time' })
	})

	it('is an array for a repeated field and an object for a map', () => {
		const p = of(jsonSchemaOf(TenantSchema) as Obj, 'app.Tenant')
		expect((p.labels as Obj).type).toBe('object')

		const list = jsonSchemaOf(RobotService.method.list.output) as Obj
		const items = of(list, RobotService.method.list.output.typeName).items as Obj
		expect(items.type).toBe('array')
	})

	it('refuses a field the message does not have', () => {
		const d = (defs(jsonSchemaOf(TenantSchema) as Obj)['app.Tenant'] as Obj)

		// Which is the whole point of validating: a typo is a red line under
		// the caret rather than a refusal from the server.
		expect(d.additionalProperties).toBe(false)
	})

	// An entity reaches its neighbours and its neighbours reach back, so a
	// schema written inline would not terminate. This is the check that it
	// does.
	it('terminates on a schema that points back at itself', () => {
		const got = jsonSchemaOf(RobotSchema) as Obj
		expect(JSON.stringify(got).length).toBeGreaterThan(0)
	})
})
