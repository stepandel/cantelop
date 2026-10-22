import "server-only";

import { cache } from "react";

import { fleetControl } from "@core/fleets/control";
import { FleetBudget } from "@core/fleets/budget";
import type { AppPolicy, FleetScope } from "@core/fleets/model";
import type { InstallationStatus } from "@core/fleets/repository";

export const FLEET_ID_PATTERN = /^flt_[a-f0-9]{32}$/;
export const INSTALLATION_ID_PATTERN = /^ins_[a-f0-9]{32}$/;

// The console's view of the owner reads. Dates cross to client components as
// ISO strings and credit amounts stay decimal strings: they are bigints.
export interface CustomerFleet {
  id: string;
  name: string;
  max_apps: number | null;
  used_apps: number;
  active_principals: number;
  created_at: string;
}

export interface FleetPrincipal {
  id: string;
  label: string;
  scopes: FleetScope[];
  expires_at: string;
  revoked_at: string | null;
  created_at: string;
}

export interface FleetPackage {
  id: string;
  name: string;
  version: string;
  runtime_id: string;
  disabled_at: string | null;
  created_at: string;
}

export interface FleetInstallation {
  id: string;
  external_key: string;
  app_id: string;
  package_id: string;
  version: string;
  next_package_id: string | null;
  status: InstallationStatus;
  error_code: string | null;
  release_status: string | null;
  policy: AppPolicy;
  policy_revision: number;
  enforced_revision: number;
  created_at: string;
  updated_at: string;
}

export interface FleetBudgetEpoch {
  epoch: string;
  included: string;
  settled: string;
  reserved: string;
  available: string;
  expires_at: string;
}

export interface FleetInstallationDetail extends FleetInstallation {
  next_version: string | null;
  runtime_id: string;
  budgets: FleetBudgetEpoch[];
}

type Row = Record<string, unknown>;

function iso(value: unknown): string {
  return value instanceof Date ? value.toISOString() : String(value);
}

function isoOrNull(value: unknown): string | null {
  return value === null || value === undefined ? null : iso(value);
}

function fleet(row: Row): CustomerFleet {
  return {
    id: String(row.id),
    name: String(row.name),
    max_apps: row.max_apps === null ? null : Number(row.max_apps),
    used_apps: Number(row.used_apps),
    active_principals: Number(row.active_principals),
    created_at: iso(row.created_at),
  };
}

function installation(row: Row): FleetInstallation {
  return {
    id: String(row.id),
    external_key: String(row.external_key),
    app_id: String(row.app_id),
    package_id: String(row.package_id),
    version: String(row.version),
    next_package_id: (row.next_package_id as string | null) ?? null,
    status: row.status as InstallationStatus,
    error_code: (row.error_code as string | null) ?? null,
    release_status: (row.release_status as string | null) ?? null,
    policy: row.policy as AppPolicy,
    policy_revision: Number(row.policy_revision),
    enforced_revision: Number(row.enforced_revision),
    created_at: iso(row.created_at),
    updated_at: iso(row.updated_at),
  };
}

export async function getCustomerFleets(userID: string): Promise<CustomerFleet[]> {
  return (await fleetControl().listForOwner(userID)).map(fleet);
}

// Request-memoized, so Home and the shell share one read per render.
export const cachedCustomerFleets = cache(getCustomerFleets);

export async function getCustomerFleet(userID: string, fleetID: string): Promise<CustomerFleet> {
  return fleet(await fleetControl().fleetForOwner(userID, fleetID));
}

export const cachedCustomerFleet = cache(getCustomerFleet);

export async function getFleetPrincipals(userID: string, fleetID: string): Promise<FleetPrincipal[]> {
  return (await fleetControl().principalsForOwner(userID, fleetID)).map((row) => ({
    id: String(row.id),
    label: String(row.label),
    scopes: row.scopes as FleetScope[],
    expires_at: iso(row.expires_at),
    revoked_at: isoOrNull(row.revoked_at),
    created_at: iso(row.created_at),
  }));
}

export async function getFleetPackages(userID: string, fleetID: string): Promise<FleetPackage[]> {
  return (await fleetControl().packagesForOwner(userID, fleetID)).map((row) => ({
    id: String(row.id),
    name: String(row.name),
    version: String(row.version),
    runtime_id: String(row.runtime_id),
    disabled_at: isoOrNull(row.disabled_at),
    created_at: iso(row.created_at),
  }));
}

export async function getFleetInstallations(
  userID: string,
  fleetID: string,
  after?: string,
): Promise<{ items: FleetInstallation[]; after: string | null }> {
  const page = await fleetControl().installationsForOwner(userID, fleetID, { after });
  return { items: page.items.map(installation), after: page.after };
}

export async function getFleetInstallation(
  userID: string,
  fleetID: string,
  installationID: string,
): Promise<FleetInstallationDetail> {
  const repo = fleetControl();
  const row = await repo.installationForOwner(userID, fleetID, installationID);
  const budgets = await new FleetBudget(repo.db).listForApp(row.app_id);
  return {
    ...installation(row),
    next_version: ((row as Row).next_version as string | null) ?? null,
    runtime_id: String(row.runtime_id),
    budgets: budgets.map((epoch) => ({
      epoch: String(epoch.epoch),
      included: String(epoch.included),
      settled: String(epoch.settled),
      reserved: String(epoch.reserved),
      available: String(epoch.available),
      expires_at: iso(epoch.expires_at),
    })),
  };
}
