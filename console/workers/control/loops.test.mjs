import { expect, test } from "bun:test";
import { runWorkerLoops } from "./loops";

test("recovery keeps polling while a materialization request is blocked", async () => {
 const stop=new AbortController();
 let finish;
 const blocked=new Promise(resolve=>{finish=resolve;});
 let recoveryPolls=0;
 await runWorkerLoops([
  {async runOnce(){await blocked;return true;}},
  {async runOnce(){if(++recoveryPolls===2){stop.abort();finish();}return true;}},
 ],false,stop.signal);
 expect(recoveryPolls).toBe(2);
});


test("latency-sensitive queues resume without waiting for a background queue",async () => {
  const stop=new AbortController();
  let unblock, fastPolls=0;
  const blocked=new Promise(resolve=>{unblock=resolve;});
  const started=performance.now();
  await runWorkerLoops([
    {async runOnce(){await blocked;return true;}},
    {idleDelayMilliseconds:5,async runOnce(){if(++fastPolls===3){stop.abort();unblock();}return false;}},
  ],false,stop.signal);
  expect(fastPolls).toBe(3);
  expect(performance.now()-started).toBeLessThan(500);
});
