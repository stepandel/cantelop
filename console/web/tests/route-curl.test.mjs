import { expect, test } from "bun:test";

import { curlCommand } from "../lib/route-curl";

const origin = "https://vera.cantelop.dev";

test("writes a pasteable curl command for each kind of method", () => {
  expect(curlCommand(origin, { method: "GET", path: "/health" })).toBe("curl 'https://vera.cantelop.dev/health'");
  expect(curlCommand(origin, { method: "HEAD", path: "/health" })).toBe("curl -I 'https://vera.cantelop.dev/health'");
  expect(curlCommand(origin, { method: "DELETE", path: "/sessions" })).toBe("curl -X DELETE 'https://vera.cantelop.dev/sessions'");
  expect(curlCommand(origin, { method: "POST", path: "/chat" }))
    .toBe("curl -X POST 'https://vera.cantelop.dev/chat' -H 'content-type: application/json' -d '{}'");
});

test("keeps an observed path inside its shell quotes", () => {
  expect(curlCommand(origin, { method: "GET", path: "/a'$(id)" })).toBe("curl 'https://vera.cantelop.dev/a'\\''$(id)'");
});
