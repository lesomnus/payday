/**
 * The client, which is this small because nothing here is generated per
 * service.
 *
 * A page does not read through this -- it reads through `store.ts`, so that a
 * row it drew redraws when the row changes. This is for what does not want
 * that: several writes as one transaction, a one-off call, a script.
 *
 * protobuf-es emits the service descriptors beside the messages and Connect's
 * `createClient` takes a descriptor, so adding an entity to the schema is one
 * line here and nothing to keep in step.
 *
 * The transport is the only thing that changes between a real server and the
 * sandbox, and nothing above this file knows which it got.
 *
 * @module
 */

import { createClient, type Client, type Transport } from '@connectrpc/connect'

import { ThingService } from '../gen/app/thing_svc_pb.js'
import { TenantService } from '../gen/payday/tenant_svc_pb.js'
import { HolderService } from '../gen/payday/holder_svc_pb.js'
import { BatchService } from '@lesomnus/payday/pdpb'

export interface App {
	readonly thing: Client<typeof ThingService>
	readonly tenant: Client<typeof TenantService>
	readonly holder: Client<typeof HolderService>

	/** Several writes as one transaction; see `payday/batch`. */
	readonly batch: Client<typeof BatchService>
}

export function app(transport: Transport): App {
	return {
		thing: createClient(ThingService, transport),
		tenant: createClient(TenantService, transport),
		holder: createClient(HolderService, transport),
		batch: createClient(BatchService, transport),
	}
}
