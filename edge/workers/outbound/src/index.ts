import { handleOutboundRequest, type OutboundEnv } from "./core";

export default {
  fetch(request: Request, env: OutboundEnv): Promise<Response> {
    return handleOutboundRequest(request, env);
  },
} satisfies ExportedHandler<OutboundEnv>;
