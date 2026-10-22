import { WorkerEntrypoint } from "cloudflare:workers";
import { assertAPIInvocation, assertAppId, RUNTIME_HOSTNAME, type APIInvocation,
  publishObservations, type ObservationIngestEnv } from "@cantelop/edge-contracts";
import { handleOutboundRequest, type OriginBrokerService } from "../../outbound/src/core";
import { observationsFromTail, type EdgeTailItem } from "../../tail/src/core";
import { validPackageCode } from "./fleet-package";

export interface FleetExecution {
  app_id: string;
  package_id: string;
  generation: string;
  configuration_revision: number;
  bindings: Record<string, string>;
}
export interface FleetRunnerEnv extends ObservationIngestEnv {
  FLEET_PACKAGES: DurableObjectNamespace;
  RELEASE_GATES: DurableObjectNamespace;
  FLEET_LOADER: WorkerLoader;
  BROKER: OriginBrokerService & { dispatchManaged(request: Request, target: APIInvocation): Promise<Response> };
}
type FleetScope = Omit<FleetExecution, "bindings">;

function validExecution(value: FleetScope): void {
  assertAppId(value.app_id);
  if (!/^pkg_[0-9a-f]{32}$/.test(value.package_id) || !/^act_[0-9a-f]{32}$/.test(value.generation) ||
      !Number.isSafeInteger(value.configuration_revision) || value.configuration_revision < 1) {
    throw new TypeError("invalid Fleet execution context");
  }
}

async function authorized(env: FleetRunnerEnv, execution: FleetScope, requestID?: string): Promise<boolean> {
  validExecution(execution);
  const gate = env.RELEASE_GATES.get(env.RELEASE_GATES.idFromName(execution.app_id));
  const response = await gate.fetch("https://release-gate.internal/authorize", {
    method: "POST", body: JSON.stringify({ app_id: execution.app_id, package_id: execution.package_id,
      generation: execution.generation, configuration_revision: execution.configuration_revision,
      ...(requestID ? { request_id: requestID } : {}) }),
  });
  return response.status === 204;
}

/** Only a release gate has a binding to this entrypoint. */
export class FleetRunner extends WorkerEntrypoint<FleetRunnerEnv, { execution: FleetExecution; invocation: APIInvocation }> {
  override async fetch(request: Request): Promise<Response> {
    const { execution, invocation } = this.ctx.props;
    validExecution(execution);
    assertAPIInvocation(invocation);
    if (invocation.app_id !== execution.app_id || !await authorized(this.env, execution)) {
      return new Response(null, { status: 503 });
    }
    const { bindings, ...scope } = execution;
    const key = JSON.stringify(["fleet-v1", scope.app_id, scope.package_id, scope.generation, scope.configuration_revision]);
    const exports = this.ctx.exports as unknown as {
      FleetRuntime(options: { props: unknown }): Fetcher;
      FleetEgress(options: { props: unknown }): Fetcher;
      FleetTail(options: { props: unknown }): Fetcher;
    };
    const worker = this.env.FLEET_LOADER.get(key, async () => {
      const store = this.env.FLEET_PACKAGES.get(this.env.FLEET_PACKAGES.idFromName(scope.package_id));
      const response = await store.fetch("https://fleet-package.internal/");
      if (!response.ok) throw new Error("Fleet package unavailable");
      const code: unknown = await response.json();
      if (!validPackageCode(code)) throw new Error("Fleet package invalid");
      const hash = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(code.module));
      const digest = `sha256:${Array.from(new Uint8Array(hash), b => b.toString(16).padStart(2, "0")).join("")}`;
      if (digest !== code.digest) throw new Error("Fleet package integrity failure");
      return {
        compatibilityDate: code.compatibility_date,
        mainModule: "worker.js", modules: { "worker.js": code.module },
        env: { ...bindings, CANTELOP_FLEET_EXECUTION_VERSION: "1" },
        globalOutbound: exports.FleetEgress({ props: scope }),
        tails: [exports.FleetTail({ props: { app_id: scope.app_id } })],
      };
    });
    // Request-specific capabilities must never enter the cached WorkerCode.
    return worker.getEntrypoint(undefined, { props: {
      CANTELOP_RUNTIME: exports.FleetRuntime({ props: { ...scope, invocation } }),
    } }).fetch(request);
  }
}

export class FleetRuntime extends WorkerEntrypoint<FleetRunnerEnv, FleetScope & { invocation: APIInvocation }> {
  async fetch(request: Request): Promise<Response> {
    const scope = this.ctx.props;
    if (new URL(request.url).hostname !== RUNTIME_HOSTNAME || !await authorized(this.env, scope, scope.invocation.request_id)) {
      return new Response(null, { status: 403 });
    }
    return handleOutboundRequest(request, { BROKER: {
      dispatch: (target, invocation) => this.env.BROKER.dispatchManaged(target, invocation),
    }, CANTELOP_CONTEXT: scope.invocation });
  }
}

export class FleetEgress extends WorkerEntrypoint<FleetRunnerEnv, FleetScope> {
  async fetch(request: Request): Promise<Response> {
    if (new URL(request.url).hostname === RUNTIME_HOSTNAME || !await authorized(this.env, this.ctx.props)) {
      return new Response(null, { status: 403 });
    }
    return handleOutboundRequest(request, { BROKER: this.env.BROKER });
  }
  // No connect() export: raw sockets cannot bypass the HTTP egress boundary.
}

export class FleetTail extends WorkerEntrypoint<FleetRunnerEnv, { app_id: string }> {
  async tail(items: TraceItem[]): Promise<void> {
    assertAppId(this.ctx.props.app_id);
    // The App comes from the private binding, the request ID from dispatcher-minted ingress metadata.
    const attributed = items.map(item => ({ ...item, scriptName: this.ctx.props.app_id })) as EdgeTailItem[];
    const observations = await observationsFromTail(attributed);
    if (observations.length) await publishObservations(this.env, observations, { userAgent: "cantelop-fleet-tail/1" });
  }
}
