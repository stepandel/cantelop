import { expect, test } from "bun:test";
import { appSlugFromHostname, assertAPIInvocation, assertPublicAppDomain, publicAppHostname } from "../src/index";

test("alternate domains keep the exact single-label App boundary", () => {
  const domain = "agents.example.net";
  const invocation = { app_id: "app_0123456789abcdef0123456789abcdef", request_id: "req_0123456789abcdef0123456789abcdef", hostname: "demo.agents.example.net" };
  expect(publicAppHostname("demo", domain)).toBe(invocation.hostname);
  expect(appSlugFromHostname(invocation.hostname, domain)).toBe("demo");
  expect(() => assertAPIInvocation(invocation, domain)).not.toThrow();
  expect(() => assertAPIInvocation(invocation)).toThrow();
  for (const host of ["demo.cantelop.dev", "demo.agents.example.net.evil.example", "a.b.agents.example.net", "agents.example.net", "demo.agents.example.net.", "demo.agents.example.net:443", "Demo.agents.example.net"]) {
    expect(() => appSlugFromHostname(host, domain)).toThrow();
  }
});

test("invalid deployment domains never widen the routing contract", () => {
  for (const domain of ["", "example.net.", "EXAMPLE.net", "*.example.net", "https://example.net", "example.net:443", "127.0.0.1", "example..net", "example.net/", " example.net"]) {
    expect(() => assertPublicAppDomain(domain)).toThrow();
  }
});
