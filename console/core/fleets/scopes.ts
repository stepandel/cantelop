// Dependency-free, so the console's client components can list the scopes.
export const scopes = [
  "packages:write",
  "installations:write",
  "installations:read",
  "credentials:read",
  "policy:write",
  "usage:read",
] as const;
export type FleetScope = (typeof scopes)[number];
