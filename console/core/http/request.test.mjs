import { expect, test } from "bun:test";

import { readControlJSON } from "./request";

test("reads bounded JSON control requests", async () => {
  const request = new Request("https://console.example.com/v1/apps", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ slug: "agent" }),
  });
  expect(await readControlJSON(request, 1024)).toEqual({ slug: "agent" });
});

test("normalizes malformed and oversized bodies as invalid requests", async () => {
  const malformed = new Request("https://console.example.com/v1/apps", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: "{",
  });
  await expect(readControlJSON(malformed, 1024)).rejects.toBeInstanceOf(TypeError);

  const oversized = new Request("https://console.example.com/v1/apps", {
    method: "POST",
    headers: { "Content-Type": "application/json", "Content-Length": "2048" },
    body: "{}",
  });
  await expect(readControlJSON(oversized, 1024)).rejects.toBeInstanceOf(TypeError);
});
