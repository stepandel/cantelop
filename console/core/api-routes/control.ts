import "server-only";

import { controlDatabase } from "../db/database";
import { ReleaseRepository } from "../db/release-repository";
import { observationRepository } from "../observability/control";
import { productControl } from "../product-control";
import { appRouteDetail as publicRouteDetail, type AppRouteDetail } from "./detail";
import { appRouteOverview, type AppRouteOverview } from "./overview";
import { relayRequest, sendRelayRequest, type RelayOptions, type RelayResponse } from "./relay";

/** The routes of an App the user owns. Rejects with `not_found` otherwise. */
export async function appRoutes(userID: string, appID: string): Promise<AppRouteOverview> {
  const ownerUserID = await productControl().resolveAppOwner(userID, appID);
  const active = await new ReleaseRepository(controlDatabase()).activeRoutesForUser(ownerUserID, appID);
  let traffic = null;
  try {
    const observations = observationRepository();
    traffic = {
      routes: await observations.routeTraffic(appID, active?.routes ?? []),
      windowHours: observations.retentionWindowHours,
    };
  } catch {
    // Traffic lives in an isolated store. Declared routes stay readable without it.
  }
  return appRouteOverview(active, traffic);
}

/**
 * One route of an App the user owns. The route need not be declared: a path
 * seen only in traffic has a detail page too. Rejects with `not_found` for a
 * foreign App; an unreadable observability store rejects as well, since
 * without it there is nothing to show.
 */
export async function appRouteDetail(userID: string, appID: string, method: string, path: string): Promise<AppRouteDetail> {
  const ownerUserID = await productControl().resolveAppOwner(userID, appID);
  const active = await new ReleaseRepository(controlDatabase()).activeRoutesForUser(ownerUserID, appID);
  const declared = (active?.routes ?? []).some((route) => route.method === method && route.path === path);
  const observations = observationRepository();
  const detail = await observations.routeDetail(appID, method, path);
  return publicRouteDetail(detail, declared, observations.retentionWindowHours);
}

/**
 * Sends one request to an App the user owns, at a route its live release
 * declares. Throws ControlError("not_found") for a foreign App or an
 * undeclared route, TypeError for a malformed request, and RelayError when
 * the App did not answer.
 */
export async function relayToApp(userID: string, appID: string, input: unknown, options?: RelayOptions): Promise<RelayResponse> {
  const { app, ownerUserID } = await productControl().getAccessibleApp(userID, appID);
  const active = await new ReleaseRepository(controlDatabase()).activeRoutesForUser(ownerUserID, appID);
  const request = relayRequest(input, active?.routes ?? []);
  return sendRelayRequest(`https://${app.hostname}`, request, options);
}
