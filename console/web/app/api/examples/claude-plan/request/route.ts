import { type NextRequest, NextResponse } from "next/server";
import { RemoteAppError } from "@cantelop/sdk";
import { readControlJSON } from "@core/http/request";
import { isSameConsoleOrigin } from "@/lib/console-origin";
import { admitDemoRequest, demoIdentity, withDemoCookie, demoWorkspace, demoSession, demoUserID } from "../shared";

export const dynamic = "force-dynamic";
export const maxDuration = 180;
const MODELS = ["claude-fable-5-1", "claude-opus-5-5", "claude-sonnet-5"];
const ACTIONS = ["auth", "authLogin", "authCode", "authCancel", "session", "message"];

export async function POST(request: NextRequest) {
  if (!isSameConsoleOrigin(request.headers)) return NextResponse.json({error:"invalid_request"},{status:400});
  // Only request parsing maps TypeError to invalid input; SDK network failures are also TypeErrors.
  const input = await readControlJSON(request,48*1024).catch(() => null) as {action?: unknown;body?: unknown} | null;
  try {
    if (!input || typeof input.action !== "string" || !ACTIONS.includes(input.action) || !plainObject(input.body) || !validBody(String(input.action),input.body))
      return NextResponse.json({error:"invalid_request"},{status:400});
    const {action,body} = input;
    const identity = await demoIdentity(request,action === "auth" || action === "authLogin");
    if (!identity) return NextResponse.json({error:"Connect the demo before continuing."},{status:400});
    if (!await admitDemoRequest(identity)) return NextResponse.json({error:"The live demo request limit has been reached. Try again later."},{status:429});
    const workspace = demoWorkspace(identity);
    const options = {signal:request.signal};
    let result: unknown;
    let status = 200;
    if (action === "auth" || action === "authLogin" || action === "authCode" || action === "authCancel") {
      const auth = workspace.auth();
      if (action === "auth") result = {authenticated:await auth.status(options)};
      else if (action === "authLogin") result = await auth.login(options);
      else if (action === "authCode") result = {authenticated:await auth.submitCode({...options,attemptId:body.attemptId as string,code:body.code as string})};
      else {
        await auth.cancelLogin({...options,attemptId:body.attemptId as string});
        result = {attemptId:body.attemptId};
      }
    } else if (action === "session") {
      const session = workspace.session({id:`${demoUserID(identity)}:${crypto.randomUUID()}`,keepAliveSeconds:300});
      // Open the Session before the browser subscribes; streams do not provision Sessions.
      await session.request({type:"snapshot"},{...options,timeoutMs:120000});
      result={sessionId:session.id};status=202;
    } else {
      const session = demoSession(identity,body.sessionId as string);
      if (!session) return NextResponse.json({error:"Session not found"},{status:404});
      const messageId = crypto.randomUUID();
      const accepted = await session.dispatch({type:"queue",id:messageId,text:body.text as string,settings:{model:body.model as string}},options);
      result={sessionId:session.id,messageId,receiptId:accepted.id};status=202;
    }
    return withDemoCookie(NextResponse.json(result,{status,headers:{"cache-control":"private, no-store"}}),identity);
  } catch (error) {
    if (error instanceof RemoteAppError) {
      const errors: Record<string,{status:number;message:string}> = {
        login_not_active:{status:409,message:"No active login attempt. Start a new one."},
        code_rejected:{status:422,message:"Claude did not accept the code. Try again."},
        login_failed:{status:502,message:"Claude login did not complete. Start a new attempt."},
        login_timeout:{status:504,message:"Claude did not respond in time. Check the status before retrying."},
      };
      const failure = errors[error.code];
      return NextResponse.json({error:failure?.message ?? "The Claude deployment could not complete the request.",code:error.code},
        {status:failure?.status ?? (error.status >=400 && error.status <=599 && error.status !== 401 && error.status !== 403 ? error.status : 502),headers:{"cache-control":"private, no-store"}});
    }
    return NextResponse.json({error:"The live Claude deployment could not be reached."},{status:502});
  }
}
function validBody(action: string, body: Record<string,unknown>): boolean {
  if (action === "auth" || action === "authLogin") return Object.keys(body).length === 0;
  if (action === "authCancel") return Object.keys(body).length === 1 && loginAttempt(body.attemptId);
  if (action === "authCode") return Object.keys(body).every(key => ["attemptId","code"].includes(key)) && loginAttempt(body.attemptId) &&
    typeof body.code === "string" && /^[\x21-\x7e]{1,2048}$/.test(body.code);
  // A message selects its model; conversations are kept separate by the client starting a new Session.
  if (action === "session") return Object.keys(body).length === 0;
  // demoSession validates the Session ID format and ownership.
  return Object.keys(body).every(key => ["sessionId","text","mode","model"].includes(key)) && MODELS.includes(String(body.model)) && typeof body.sessionId === "string" &&
    typeof body.text === "string" && !!body.text.trim() && new TextEncoder().encode(body.text).length <=32*1024 && body.mode === "queue";
}
function plainObject(value: unknown): value is Record<string,unknown> {
  return !!value && typeof value === "object" && !Array.isArray(value) && Object.getPrototypeOf(value) === Object.prototype;
}

function loginAttempt(value: unknown): value is string {return typeof value === "string" && /^lgn_[a-f0-9]{32}$/.test(value);}
