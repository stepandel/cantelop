import { randomBytes } from "node:crypto";
import type { FleetScope } from "./scopes";

export { scopes, type FleetScope } from "./scopes";
export interface FleetActor {
  id: string;
  fleetId: string;
  ownerUserId: string;
  scopes: FleetScope[];
}
export class FleetError extends Error {
  constructor(
    readonly code: string,
    readonly status = 400,
  ) {
    super(code);
  }
}
export function resource(prefix: string) {
  return `${prefix}_${randomBytes(16).toString("hex")}`;
}
export function requireScope(actor: FleetActor, scope: FleetScope) {
  if (!actor.scopes.includes(scope)) throw new FleetError("forbidden", 403);
}
export function identifier(value: unknown, prefix: string): asserts value is string {
  if (typeof value !== "string" || !new RegExp(`^${prefix}_[a-f0-9]{32}$`).test(value))
    throw new FleetError("invalid_id");
}
export function integer(value: unknown, min: number, max: number): number {
  if (!Number.isSafeInteger(value) || (value as number) < min || (value as number) > max)
    throw new FleetError("invalid_limit");
  return value as number;
}
export interface AppPolicy {
  active: boolean;
  expiresAt: string;
  maxSandboxes: number;
  maxVcpus: number;
  maxMemoryMib: number;
  maxWorkspaces: number;
  maxStorageBytes: number;
}
export function policy(input: unknown): AppPolicy {
  if (!input || typeof input !== "object" || Array.isArray(input)) throw new FleetError("invalid_policy");
  const v = input as Record<string, unknown>;
  const keys = [
    "active",
    "expiresAt",
    "maxSandboxes",
    "maxVcpus",
    "maxMemoryMib",
    "maxWorkspaces",
    "maxStorageBytes",
  ];
  if (
    Object.keys(v).length !== keys.length ||
    Object.keys(v).some((k) => !keys.includes(k)) ||
    typeof v.active !== "boolean" ||
    typeof v.expiresAt !== "string" ||
    !Number.isFinite(Date.parse(v.expiresAt))
  )
    throw new FleetError("invalid_policy");
  return {
    active: v.active,
    expiresAt: new Date(v.expiresAt).toISOString(),
    maxSandboxes: integer(v.maxSandboxes, 0, 1000),
    maxVcpus: integer(v.maxVcpus, 0, 10000),
    maxMemoryMib: integer(v.maxMemoryMib, 0, 1_000_000),
    maxWorkspaces: integer(v.maxWorkspaces, 0, 10000),
    maxStorageBytes: integer(v.maxStorageBytes, 0, 1024 ** 4),
  };
}
