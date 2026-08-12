/**
 * The client, which is layer 2 and is this small on purpose.
 *
 * protobuf-es v2 emits the service **descriptors** beside the messages, and
 * Connect's `createClient` takes a descriptor and a transport. So there is no
 * generated file per service and nothing to keep in step with the schema: a
 * service added to the schema is a descriptor in `gen/`, and calling it is one
 * line here.
 *
 * That is the same discipline the store layer is held to -- generate the
 * declaration, implement the behaviour once -- applied one layer down, and it
 * is why `pd gen --ts` runs exactly one plugin.
 *
 * # Which transport
 *
 * Any Connect transport works, and that is the point of the layering: this file
 * knows a `Transport` and nothing about how bytes move. Two are worth naming.
 *
 *   - **A real server.** `createGrpcWebTransport` from `@connectrpc/connect-web`
 *     against the app's address.
 *   - **The sandbox.** `createDrpcTransport` from `@lesomnus/grpc-dgram`, over a
 *     message port to the whole app compiled to wasm and running in a worker.
 *
 * Switching between them is one argument. Nothing above this file changes,
 * which is what makes a sandbox worth having: the code that runs against it is
 * the code that runs against the server.
 *
 * @module
 */

import { createClient, type Client, type Transport } from '@connectrpc/connect'

import { TenantService } from '../gen/app/payday/tenant_svc_pb.js'
import { HolderService } from '../gen/app/payday/holder_svc_pb.js'
import { AuditService } from '../gen/app/payday/audit_svc_pb.js'
import { RobotService } from '../gen/app/robot_svc_pb.js'
import { JointService, FleetService, CellService } from '../gen/app/robot_svc_pb.js'
import { BatchService } from '@lesomnus/payday/pdpb'

/**
 * App is every service this app serves, over one transport.
 *
 * It is written by hand and not generated, and it is worth being clear that
 * this is a *choice* rather than a gap: a generated version of this file would
 * be a list of the same names, and the thing it would save is the one line
 * somebody writes when they add an entity. What it would cost is a generator
 * that has to know about a TypeScript module layout.
 */
export interface App {
	readonly tenant: Client<typeof TenantService>
	readonly holder: Client<typeof HolderService>
	readonly audit: Client<typeof AuditService>

	readonly robot: Client<typeof RobotService>
	readonly joint: Client<typeof JointService>
	readonly fleet: Client<typeof FleetService>
	readonly cell: Client<typeof CellService>

	/**
	 * Several writes as one transaction.
	 *
	 * It is payday's own service rather than one of this app's, which is why it
	 * takes `Any` and takes no position on what is in it. What may go in an
	 * operation is checked per operation by the server -- the credential, what
	 * is closed, the rate limit and the policy -- so a batch is not a way past
	 * anything the transport enforces.
	 */
	readonly batch: Client<typeof BatchService>
}

/** app wires every service of this app onto one transport. */
export function app(transport: Transport): App {
	return {
		tenant: createClient(TenantService, transport),
		holder: createClient(HolderService, transport),
		audit: createClient(AuditService, transport),

		robot: createClient(RobotService, transport),
		joint: createClient(JointService, transport),
		fleet: createClient(FleetService, transport),
		cell: createClient(CellService, transport),

		batch: createClient(BatchService, transport),
	}
}
