import { afterAll, afterEach, expect, mock, test } from "bun:test";
import { createHash } from "node:crypto";
import { NextRequest } from "next/server";
import { RemoteAppError } from "@cantelop/sdk";

let budgetAllowed = true;
let calls = [];
let reply = {authenticated:false};
let error;
mock.module("server-only", () => ({}));
const { consumeDemoRequestBudget: realBudget } = await import("@/lib/demo-budget");
mock.module("@/lib/demo-budget", () => ({consumeDemoRequestBudget:async (id,database) => database ? realBudget(id,database) : budgetAllowed}));
mock.module("./claude-app", () => ({app:{workspace(selector) {
  calls.push({type:"workspace",selector});
  return {auth() {
    const invoke = async (method,input) => {calls.push({type:method,input});if(error) throw error;return reply;};
    return {status:async input => (await invoke("auth.status",input)).authenticated,login:input=>invoke("auth.login",input),
      submitCode:async input => (await invoke("auth.code",input)).authenticated,cancelLogin:input=>invoke("auth.cancel",input)};
  },session(options) {
    calls.push({type:"session",options});
    return {id:options.id,
      request:async (command,input) => {calls.push({type:"request",command,input});if(error) throw error;return reply;},
      dispatch:async (command,input) => {calls.push({type:"dispatch",command,input});return {id:"receipt"};},
      stop:async () => {calls.push({type:"stop"});if(error) throw error;},
      stream:async function* (input) {calls.push({type:"stream",input});yield {cursor:{streamId:"stream",sequence:1},data:{type:"session.ready"}};},
    };
  }};
}}}));
const {POST} = await import("./request/route");
const {GET} = await import("./events/route");
const {demoIdentity,demoUserID} = await import("./shared");
const originalKey=process.env.CLAUDE_DEMO_PRIVATE_JWK;
process.env.CLAUDE_DEMO_PRIVATE_JWK="test-cookie-secret";
afterAll(() => {if(originalKey === undefined) delete process.env.CLAUDE_DEMO_PRIVATE_JWK;else process.env.CLAUDE_DEMO_PRIVATE_JWK=originalKey;});
afterEach(() => {calls=[];budgetAllowed=true;reply={authenticated:false};error=undefined;});
function request(action,body={},cookie,origin="https://cantelop.com") {
  return new NextRequest("https://cantelop.com/api/examples/claude-plan/request",{method:"POST",headers:{host:"cantelop.com",origin,"content-type":"application/json",...(cookie?{cookie}:{})},body:JSON.stringify({action,body})});
}
async function connect() {
  const response=await POST(request("auth"));expect(response.status).toBe(200);
  return {cookie:response.headers.get("set-cookie").split(";")[0],body:await response.json()};
}
function events(id,cookie,extra={}) {return new NextRequest(`https://cantelop.com/api/examples/claude-plan/events?sessionId=${encodeURIComponent(id)}`,{headers:{cookie,...extra}});}

test("native auth uses a stable visitor Workspace without an application auth Session",async () => {
  const {cookie,body}=await connect();
  expect(body.authenticated).toBe(false);
  expect(calls.filter(c=>c.type==="auth.status")).toHaveLength(1);
  expect(calls.filter(c=>c.type==="session")).toHaveLength(0);
  const selector=calls.find(c=>c.type==="workspace").selector;
  calls=[];await POST(request("auth",{},cookie));expect(calls.find(c=>c.type==="workspace").selector).toEqual(selector);
  calls=[];await connect();expect(calls.find(c=>c.type==="workspace").selector).not.toEqual(selector);
});

test("visitor mapping preserves the previously JWT-derived Workspace",() => {
  const identity={subject:"visitor:"+"a".repeat(32)};
  const old=createHash("sha256").update(JSON.stringify(["cantelop-claude-api-owner",`blog-demo:${identity.subject}`])).digest("hex").slice(0,48);
  expect(demoUserID(identity)).toBe(old);
});

const attemptID = () => "lgn_"+crypto.randomUUID().replaceAll("-","");
test("native login, code retries and cancellation use Workspace auth",async () => {
  const {cookie}=await connect(),attemptId=attemptID();calls=[];
  reply={authenticated:false,attemptId,url:"https://claude.ai/oauth/authorize",expiresAt:new Date()};
  expect((await POST(request("authLogin",{},cookie))).status).toBe(200);
  expect(calls.find(c=>c.type==="auth.login")).toBeTruthy();
  calls=[];reply={authenticated:true};
  expect((await POST(request("authCode",{attemptId,code:"abc#def"},cookie))).status).toBe(200);
  expect(calls.find(c=>c.type==="auth.code").input).toMatchObject({attemptId,code:"abc#def"});
  expect(calls.filter(c=>c.type==="stop")).toHaveLength(0);
  calls=[];await POST(request("authCancel",{attemptId},cookie));
  expect(calls.find(c=>c.type==="auth.cancel").input.attemptId).toBe(attemptId);
});

test("invalid auth inputs never reach the SDK",async () => {
  const {cookie}=await connect();calls=[];
  for(const [action,body] of [["token",{subject:"other"}],["auth",{claudeToken:"secret"}],["authCode",{attemptId:crypto.randomUUID(),code:"line\nbreak"}],["authCancel",{attemptId:"bad"}]])
    expect((await POST(request(action,body,cookie))).status).toBe(400);
  expect(calls).toHaveLength(0);
});

test("Sessions open before subscription and each queued message selects its allowed model",async () => {
  const {cookie}=await connect();calls=[];
  const response=await POST(request("session",{},cookie));expect(response.status).toBe(202);
  const body=await response.json();
  expect(calls.find(c=>c.type==="request").command).toEqual({type:"snapshot"});
  expect(calls.some(c=>c.type==="dispatch")).toBe(false);
  calls=[];await POST(request("message",{sessionId:body.sessionId,text:"hello",mode:"queue",model:"claude-sonnet-5"},cookie));
  expect(calls.find(c=>c.type==="dispatch").command).toMatchObject({type:"queue",text:"hello",settings:{model:"claude-sonnet-5"}});
  calls=[];
  for (const bodyOverride of [{model:"unknown"},{settings:{tools:["Bash"]}},{tools:["Bash"]},{maxTurns:100},{mcps:{}}]) {
    expect((await POST(request("message",{sessionId:body.sessionId,text:"hello",mode:"queue",model:"claude-sonnet-5",...bodyOverride},cookie))).status).toBe(400);
  }
  expect(calls).toHaveLength(0);
});

async function chat(cookie) {
  return (await (await POST(request("session",{},cookie))).json()).sessionId;
}

test("foreign Sessions are rejected before delivery or subscription",async () => {
  const first=await connect();const session=await (await POST(request("session",{},first.cookie))).json();
  const second=await connect();calls=[];
  expect((await POST(request("message",{sessionId:session.sessionId,text:"steal",mode:"queue",model:"claude-sonnet-5"},second.cookie))).status).toBe(404);
  expect((await GET(events(session.sessionId,second.cookie))).status).toBe(404);
  expect(calls.some(c=>["request","dispatch","stream"].includes(c.type))).toBe(false);
});

test("SDK subscriptions become browser SSE with explicit resume cursors",async () => {
  const {cookie}=await connect();const sessionId=await chat(cookie);calls=[];
  const response=await GET(events(sessionId,cookie,{"last-event-id":"stream:0"}));
  expect(response.status).toBe(200);
  expect(await response.text()).toContain('data: {"cursor":{"streamId":"stream","sequence":1},"data":{"type":"session.ready"}}');
  expect(calls.find(c=>c.type==="stream").input.after).toEqual({streamId:"stream",sequence:0});
});

test("identity and request budgets precede SDK operations",async () => {
  expect((await POST(request("authCancel",{attemptId:attemptID()}))).status).toBe(400);expect(calls).toHaveLength(0);
  const {cookie}=await connect();const sessionId=await chat(cookie);calls=[];budgetAllowed=false;
  expect((await POST(request("auth",{},cookie))).status).toBe(429);
  expect((await GET(events(sessionId,cookie))).status).toBe(429);expect(calls).toHaveLength(0);
});

test("forged cookies and cross-origin requests cannot use the demo",async () => {
  const {cookie}=await connect();const sessionId=await chat(cookie);calls=[];
  const tampered=cookie.replace(/=[a-f0-9]{32}/,"="+"0".repeat(32));
  expect((await POST(request("authCode",{attemptId:attemptID(),code:"test"},tampered))).status).toBe(400);
  expect((await POST(request("auth",{},cookie,"https://evil.example"))).status).toBe(400);
  expect((await GET(events(sessionId,cookie,{"sec-fetch-site":"cross-site"}))).status).toBe(400);expect(calls).toHaveLength(0);
});

test("native login failures retain actionable HTTP errors",async () => {
  const {cookie}=await connect();
  for(const [code,status] of [["login_not_active",409],["code_rejected",422],["login_failed",502],["login_timeout",504]]) {
    error=new RemoteAppError(code,400);expect((await POST(request("authCode",{attemptId:attemptID(),code:"abc"},cookie))).status).toBe(status);
  }
});


test("Connect establishes its visitor and checks native sign-in in one request",async () => {
  reply={authenticated:false,attemptId:attemptID(),url:"https://claude.ai/oauth/authorize",expiresAt:new Date()};
  const response=await POST(request("authLogin"));
  expect(response.status).toBe(200);
  expect(response.headers.get("set-cookie")).toContain("HttpOnly");
  expect((await response.json()).url).toBe(reply.url);
  expect(calls.filter(c=>c.type==="auth.login")).toHaveLength(1);
  expect(calls.some(c=>c.type==="session")).toBe(false);
  calls=[];reply={authenticated:true};
  const signedIn=await POST(request("authLogin",{},response.headers.get("set-cookie").split(";")[0]));
  expect((await signedIn.json()).authenticated).toBe(true);
  expect(calls.filter(c=>c.type==="auth.login")).toHaveLength(1);
  expect(calls.filter(c=>c.type==="stop")).toHaveLength(0);
  calls=[];budgetAllowed=false;
  expect((await POST(request("authLogin"))).status).toBe(429);
  expect(calls).toHaveLength(0);
});
