import { afterEach, expect, test } from "bun:test";
import { configuredAppDomain, configuredConsoleOrigins, configuredDocsOrigin, publicAppHostname } from "./deployment";
import { ProductService } from "./product-service";
import { consoleRequestOrigin, isSameConsoleOrigin } from "../web/lib/console-origin";

const original = { ...process.env };
afterEach(() => { process.env = { ...original }; });

test("operator domains project into App API results and exact console-origin checks", async () => {
  process.env.CANTELOP_APP_DOMAIN = "agents.example.net";
  process.env.CANTELOP_CONSOLE_ORIGINS = "https://console.example.net";
  process.env.NODE_ENV = "production";
  const at = new Date("2026-10-21T00:00:00Z");
  const service = new ProductService({ async listAppsForUser() { return [{ id: "app_0123456789abcdef0123456789abcdef", slug: "demo", createdAt: at, updatedAt: at }]; } });
  expect((await service.listApps("user")).items[0].hostname).toBe("demo.agents.example.net");
  const headers = new Headers({ host: "console.example.net", origin: "https://console.example.net" });
  expect(consoleRequestOrigin(headers)).toBe("https://console.example.net");
  expect(isSameConsoleOrigin(headers)).toBe(true);
  headers.set("x-forwarded-host", "console.example.net.evil.example");
  expect(consoleRequestOrigin(headers)).toBeNull();
  expect(consoleRequestOrigin(new Headers({ host: "cantelop.com" }))).toBeNull();
  expect(configuredDocsOrigin({ CANTELOP_CONTROL_ORIGIN: "https://console.example.net" })).toBe("https://console.example.net");
});

test("hosted defaults remain exact and operator host aliases fail closed", () => {
  expect(configuredAppDomain({})).toBe("cantelop.dev");
  expect(publicAppHostname("demo", {})).toBe("demo.cantelop.dev");
  expect([...configuredConsoleOrigins({})]).toEqual(["https://cantelop.com", "https://www.cantelop.com", "https://console.cantelop.dev"]);
  for (const domain of ["", "example.net.", "EXAMPLE.net", "*.example.net", "https://example.net", "example.net:443", "127.0.0.1", "example..net", "example.net/", " example.net"]) {
    expect(() => configuredAppDomain({ CANTELOP_APP_DOMAIN: domain })).toThrow();
  }
  for (const origin of ["", "https://*.example.net", "https://console.example.net/", "https://user:pass@console.example.net", "http://console.example.net", "https://console.example.net?", "https://console.example.net#"]) {
    expect(() => configuredConsoleOrigins({ CANTELOP_CONSOLE_ORIGINS: origin })).toThrow();
  }
});

test("local development documentation accepts only loopback HTTP outside production", () => {
  expect(configuredDocsOrigin({ CANTELOP_CONTROL_ORIGIN: "http://localhost:3000" })).toBe("http://localhost:3000");
  expect(() => configuredDocsOrigin({ NODE_ENV: "production", CANTELOP_CONTROL_ORIGIN: "http://localhost:3000" })).toThrow();
  expect(() => configuredDocsOrigin({ CANTELOP_DOCS_ORIGIN: "http://console.example.net" })).toThrow();
});
