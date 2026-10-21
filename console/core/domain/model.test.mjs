import { describe, expect, test } from "bun:test";

import { assertResourceID, idPatterns, newResourceID } from "./ids";
import { defaultUserQuota, validateCreateApp, validateCreateWorkspace, validateIdentity } from "./model";

describe("control-plane identity", () => {
  test("accepts stable provider identity while treating email as metadata", () => {
    expect(validateIdentity({ provider: "github", providerSubject: "123456", email: "alice@example.com" })).toEqual({
      provider: "github", providerSubject: "123456", email: "alice@example.com",
    });
    expect(validateIdentity({ provider: "github", providerSubject: "123456", email: "Alice@Example.COM" })).toEqual({
      provider: "github", providerSubject: "123456", email: "alice@example.com",
    });
    expect(validateIdentity({ provider: "github", providerSubject: "123456", email: "Ärzte@Example.com" }).email).toBe("Ärzte@example.com");
    expect(() => validateIdentity({ provider: "github", providerSubject: "123456", email: " alice@example.com" })).toThrow();
    expect(() => validateIdentity({ provider: "github", providerSubject: "123456", email: "" })).toThrow();
  });

  test("generates opaque product IDs independent from providers", () => {
    for (const kind of Object.keys(idPatterns)) {
      const id = newResourceID(kind);
      expect(idPatterns[kind].test(id)).toBe(true);
      expect(() => assertResourceID(kind, id)).not.toThrow();
    }
  });
});

describe("App aggregate input", () => {
  test("validates routing and platform-owned runtime constraints", () => {
    expect(validateCreateApp({
      slug: "agent",
      sessionRuntime: { internalPort: 3000 },
    })).toEqual({
      slug: "agent",
      sessionRuntime: { internalPort: 3000 },
    });
    expect(() => validateCreateApp({
      slug: "bad--slug",
      sessionRuntime: { internalPort: 3000 },
    })).toThrow();
    expect(() => validateCreateApp({
      slug: "release-gate",
      sessionRuntime: { internalPort: 3000 },
    })).toThrow();
    expect(() => validateCreateWorkspace({ appID: "app_wrong", slug: "staging" })).toThrow();
  });

  test("keeps initial quota policy in the product domain", () => {
    expect(defaultUserQuota).toEqual({ maxApps: 5 });
  });
});
