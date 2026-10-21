import { expect, test } from "bun:test";
import { followStream } from "./claude-plan-stream";

async function follow(events) {
  const updates = [];
  let cancelled = false;
  const body = new ReadableStream({
    start(controller) {
      for (const data of events) {
        const bytes = new TextEncoder().encode(`data: ${JSON.stringify({data})}\n\n`);
        // Split frames inside UTF-8 sequences as well as JSON and SSE boundaries.
        for (let offset = 0; offset < bytes.length; offset += 3) controller.enqueue(bytes.slice(offset, offset + 3));
      }
    },
    cancel() { cancelled = true; },
  });
  await followStream(body, "message", new AbortController().signal, update => updates.push(update));
  expect(cancelled).toBe(true);
  return updates;
}
const native = event => ({type:"claude",id:"message",event});
const completed = {type:"message.status",id:"message",status:"completed"};

test("partial Claude text and tool status stream before the completed reply", async () => {
  const updates = await follow([
    {type:"message.status",id:"message",status:"queued"},
    {type:"message.status",id:"message",status:"running"},
    native({type:"stream_event",event:{type:"message_start"}}),
    native({type:"stream_event",event:{delta:{type:"text_delta",text:"Hello 🌍"}}}),
    native({type:"assistant",message:{content:[{type:"text",text:"Hello 🌍"},{type:"tool_use",name:"Write",input:{file_path:"/workspace/hello.md"}}]}}),
    completed,
  ]);
  expect(updates).toContainEqual({kind:"running",status:"Claude Code is writing",response:"Hello 🌍"});
  expect(updates).toContainEqual({kind:"running",status:"Writing hello.md",response:"Hello 🌍"});
  expect(updates.at(-1)).toEqual({kind:"completed",response:"Hello 🌍"});
});

test("SDK output decoder reconstructs out-of-order fragments and ignores other messages", async () => {
  const json = JSON.stringify({type:"result",result:"A fragmented reply 🌍"});
  const parts = [json.slice(0,15),json.slice(15)];
  const updates = await follow([
    {type:"claude",id:"other",event:{type:"result",result:"foreign reply"}},
    ...[1,0].map(index => ({type:"claude.fragment",id:"message",eventId:"event",index,total:2,json:parts[index]})),
    completed,
  ]);
  expect(updates.at(-1)).toEqual({kind:"completed",response:"A fragmented reply 🌍"});
  expect(updates.some(update => update.response?.includes("foreign"))).toBe(false);
});

test("expired native sign-in retains the reconnect message", async () => {
  expect((await follow([{type:"auth.required",id:"message"}])).at(-1))
    .toEqual({kind:"failed",message:"Claude sign-in expired. Reconnect and try again."});
});

test("a dropped stream reports failure instead of displaying an incomplete reply as done", async () => {
  const body = new ReadableStream({start(controller) {controller.close();}});
  await expect(followStream(body,"message",new AbortController().signal,()=>{}))
    .rejects.toThrow("The Claude event stream ended before the message finished.");
});
