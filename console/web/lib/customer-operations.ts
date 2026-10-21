import "server-only";

import { ControlError } from "@core/domain/errors";
import { productControl } from "@core/product-control";
import type { RequestEventPage, Session, Snapshot } from "@/lib/telemetry-types";
import { getCustomerRequestEvents, getCustomerSnapshot } from "@/lib/customer-telemetry";
import { terminateSession } from "@/lib/fire-fuse";

export interface CustomerSession extends Session {
  app_id: string;
}

interface CustomerSessionDependencies {
  assertApp(userID: string, appID: string): Promise<unknown>;
  snapshot(userID: string): Promise<Snapshot>;
  terminate(sessionID: string): Promise<Session>;
}

const defaultSessionDependencies: CustomerSessionDependencies = {
  assertApp: (userID, appID) => productControl().getApp(userID, appID),
  snapshot: (userID) => getCustomerSnapshot(userID),
  terminate: (sessionID) => terminateSession(sessionID),
};

export async function listCustomerSessions(
  userID: string,
  appID: string,
  dependencies: CustomerSessionDependencies = defaultSessionDependencies,
): Promise<{ items: CustomerSession[] }> {
  await dependencies.assertApp(userID, appID);
  const snapshot = await dependencies.snapshot(userID);
  if (!snapshot || !Array.isArray(snapshot.sessions)) {
    throw new Error("observer returned invalid customer Sessions");
  }
  return {
    items: snapshot.sessions
      .filter((session) => session.app_id === appID)
      .sort((left, right) => right.created_at.localeCompare(left.created_at)),
  };
}

export async function getCustomerSession(
  userID: string,
  appID: string,
  sessionID: string,
  dependencies: CustomerSessionDependencies = defaultSessionDependencies,
): Promise<CustomerSession> {
  await dependencies.assertApp(userID, appID);
  const session = (await dependencies.snapshot(userID)).sessions.find((candidate) =>
    candidate.app_id === appID && candidate.id === sessionID,
  );
  if (!session) throw new ControlError("not_found");
  return session;
}

export async function terminateCustomerSession(
  userID: string,
  appID: string,
  sessionID: string,
  dependencies: CustomerSessionDependencies = defaultSessionDependencies,
): Promise<CustomerSession> {
  const existing = await getCustomerSession(userID, appID, sessionID, dependencies);
  const session = await dependencies.terminate(sessionID);
  if (!session || session.id !== sessionID || session.workspace_id !== existing.workspace_id) {
    throw new Error("runtime returned an invalid Session");
  }
  return { ...session, app_id: appID };
}

export async function listCustomerLogs(userID: string, appID: string, search: URLSearchParams): Promise<RequestEventPage> {
  await productControl().getApp(userID, appID);
  const scoped = new URLSearchParams(search);
  scoped.set("app_id", appID);
  return getCustomerRequestEvents(userID, scoped);
}
