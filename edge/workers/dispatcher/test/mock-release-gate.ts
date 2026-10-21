import { GATE_APP_HEADER, GATE_INVOCATION_HEADER } from "../src/release-gate";
import type { UserWorkerNamespace } from "../src/core";

export function mockReleaseGates(workers: UserWorkerNamespace): Pick<DurableObjectNamespace, "idFromName" | "get"> {
  return {
    idFromName(name: string) { return name as unknown as DurableObjectId; },
    get(id: DurableObjectId) {
      return {
        fetch(request: Request) {
          const invocation = JSON.parse(request.headers.get(GATE_INVOCATION_HEADER) ?? "null") as Record<string, unknown>;
          const headers = new Headers(request.headers);
          headers.delete(GATE_APP_HEADER);
          headers.delete(GATE_INVOCATION_HEADER);
          return workers.get(String(id), {}, { outbound: { CANTELOP_CONTEXT: invocation } })
            .fetch(new Request(request, { headers }));
        },
      } as DurableObjectStub;
    },
  };
}

export function withMockReleaseGate<T extends { USER_WORKERS: UserWorkerNamespace }>(env: T): T & { RELEASE_GATES: Pick<DurableObjectNamespace, "idFromName" | "get"> } {
  return { ...env, RELEASE_GATES: mockReleaseGates(env.USER_WORKERS) };
}
