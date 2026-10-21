import { WorkerEntrypoint } from "cloudflare:workers";

import type { DispatchTarget } from "@cantelop/edge-contracts";

import {
  dispatchToOrigin,
  internalOnlyResponse,
  type OriginBrokerEnv,
} from "./core";

export default class OriginBroker extends WorkerEntrypoint<OriginBrokerEnv> {
  override fetch(): Response {
    return internalOnlyResponse();
  }

  dispatch(request: Request, target: DispatchTarget): Promise<Response> {
    return dispatchToOrigin(request, target, this.env);
  }
}
