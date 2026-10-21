import { expect, test } from "bun:test";

import { parseIntegrationRotationRequest } from "./integration-request";

test("rotation requests default to a day of grace and bound explicit periods", () => {
  expect(parseIntegrationRotationRequest({})).toBe(86400);
  expect(parseIntegrationRotationRequest({ retry: true })).toBe("retry");
  expect(parseIntegrationRotationRequest({ grace_seconds: 0 })).toBe(0);
  expect(parseIntegrationRotationRequest({ grace_seconds: 604800 })).toBe(604800);
  for (const invalid of [null, [], "3600", { grace_seconds: -1 }, { grace_seconds: 604801 }, { grace_seconds: 1.5 },
    { grace_seconds: "60" }, { grace_seconds: 60, extra: true }, { grace: 60 }, { retry: false }, { retry: true, grace_seconds: 60 }, { retry: "true" }])
    expect(() => parseIntegrationRotationRequest(invalid)).toThrow(TypeError);
});
