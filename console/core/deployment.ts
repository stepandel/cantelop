const hostedConsoleOrigins = ["https://cantelop.com", "https://www.cantelop.com", "https://console.cantelop.dev"];
const label = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/;

export function configuredAppDomain(environment: NodeJS.ProcessEnv = process.env): string {
  const domain = environment.CANTELOP_APP_DOMAIN ?? "cantelop.dev";
  const labels = domain.split(".");
  if (domain.length > 189 || labels.length < 2 || labels.some((part) => !label.test(part)) || !/^[a-z]/.test(labels.at(-1)!)) {
    throw new Error("CANTELOP_APP_DOMAIN must be a lowercase DNS domain, without a scheme, port, wildcard, or trailing dot");
  }
  return domain;
}

export function publicAppHostname(slug: string, environment: NodeJS.ProcessEnv = process.env): string {
  if (!label.test(slug) || slug.includes("--")) throw new Error("invalid App slug");
  return `${slug}.${configuredAppDomain(environment)}`;
}

export function requireHTTPSOrigin(value: string, setting: string): string {
  let url: URL;
  try { url = new URL(value); } catch { throw new Error(`${setting} must be an exact HTTPS origin`); }
  if (url.protocol !== "https:" || url.port === "0" || url.origin !== value || url.username || url.password || url.pathname !== "/" || url.search || url.hash || (url.hostname.length > 253 || url.hostname.split(".").length < 2 || !/^[a-z]/.test(url.hostname.split(".").at(-1)!)) || url.hostname.split(".").some((part) => !label.test(part))) {
    throw new Error(`${setting} must be an exact HTTPS origin`);
  }
  return value;
}

/** Explicit operator origins replace the hosted allowlist. Request headers never add trust. */
export function configuredConsoleOrigins(environment: NodeJS.ProcessEnv = process.env): Set<string> {
  const configured = environment.CANTELOP_CONSOLE_ORIGINS;
  if (configured === undefined) return new Set(hostedConsoleOrigins);
  const origins = configured.split(",").map((value) => value.trim());
  for (const origin of origins) requireHTTPSOrigin(origin, "CANTELOP_CONSOLE_ORIGINS");
  return new Set(origins);
}

export function configuredDocsOrigin(environment: NodeJS.ProcessEnv = process.env): string {
  const value = environment.CANTELOP_DOCS_ORIGIN ?? environment.CANTELOP_CONTROL_ORIGIN ?? "https://console.cantelop.dev";
  if (environment.NODE_ENV !== "production" && /^http:\/\/(localhost|127\.0\.0\.1)(:\d+)?$/.test(value)) return value;
  return requireHTTPSOrigin(value, "CANTELOP_DOCS_ORIGIN");
}
