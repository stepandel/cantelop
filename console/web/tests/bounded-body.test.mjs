import { expect, test } from "bun:test";

import { readBoundedUTF8Body } from "../../core/http/bounded-body.ts";

test("reads a body within the byte limit", async () => {
  const body = new Response('{"refresh_token":"ok"}').body;
  expect(await readBoundedUTF8Body(body, 64)).toBe('{"refresh_token":"ok"}');
});

test("cancels a streaming body as soon as it exceeds the byte limit", async () => {
  let canceled = false;
  const body = new ReadableStream({
    start(controller) {
      controller.enqueue(new Uint8Array(4096));
      controller.enqueue(new Uint8Array([1]));
    },
    cancel() {
      canceled = true;
    },
  });
  await expect(readBoundedUTF8Body(body, 4096)).rejects.toThrow("exceeds limit");
  expect(canceled).toBe(true);
});
