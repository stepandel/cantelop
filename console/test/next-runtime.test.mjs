import { expect, test } from "bun:test";
import { createAsyncLocalStorage } from "next/dist/server/app-render/async-local-storage.js";

test("Next request contexts work when routes load directly in the test runner", async () => {
  const context = createAsyncLocalStorage();
  await context.run("request", async () => {
    await Promise.resolve();
    expect(context.getStore()).toBe("request");
  });
  expect(context.getStore()).toBeUndefined();
});
