import { CantelopClient, type WebhookContext } from "@cantelop/sdk";

type Input = { trialId: string };
type Reply = { trialId: string; handlerStartedAt: number };
type Context = WebhookContext<Input, never, Reply>;
const authorized = ({request,env}:Context) => typeof env.BENCHMARK_TOKEN === "string" &&
  env.BENCHMARK_TOKEN.length >= 32 && request.headers.get("authorization") === `Bearer ${env.BENCHMARK_TOKEN}`;
const cantelop = new CantelopClient();
export const app = cantelop.app<Input, never, Reply>({
  name: "session-activation-benchmark",
  runtime: {receive({message,reply}) {reply({trialId:message.payload.trialId,handlerStartedAt:Date.now()});}},
  environment: {BENCHMARK_TOKEN:{secret:true,required:true}},
  webhooks: {
    "/webhooks/prepare": {method:"POST", async handle(context) {
      if(!authorized(context)) return Response.json({}, {status:401});
      const workspace=await context.app.workspace({slug:"activation-benchmark"}).resolve();
      await (await workspace.database()).execute("SELECT 1");
      return Response.json({workspaceId:workspace.id});
    }},
    "/webhooks/trial": {method:"POST", async handle(context) {
      if(!authorized(context)) return Response.json({}, {status:401});
      const input=await context.request.json() as {trialId?:unknown;freshWorkspace?:unknown};
      if(typeof input.trialId !== "string" || !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(input.trialId) ||
        (input.freshWorkspace !== undefined && typeof input.freshWorkspace !== "boolean")) return Response.json({}, {status:400});
      const workspaceSlug=input.freshWorkspace ? `activation-${input.trialId}` : "activation-benchmark";
      const session=context.app.workspace({slug:workspaceSlug}).session({id:`benchmark:${input.trialId}`,keepAliveSeconds:0});
      const submittedAt=Date.now(),started=performance.now();
      try {
        const result=await session.request({trialId:input.trialId},{timeoutMs:30000,signal:context.request.signal});
        return Response.json({...result,submittedAt,responseMs:performance.now()-started,workspaceSlug});
      } finally {await session.stop();}
    }},
  },
});
