import { test, expect } from "bun:test";
import { receive } from "./agent";

test("echo output and request reply share the received message", async () => {
  const events: unknown[] = [];
  let result: unknown;
  await receive({
    message: { payload: { message: "hello" } },
    output: {
      async send(event: unknown) {
        events.push(event);
      },
    },
    reply(value: unknown) {
      result = value;
    },
  } as never);
  expect(events).toEqual([{ message: "hello" }]);
  expect(result).toEqual({ message: "hello" });
});
