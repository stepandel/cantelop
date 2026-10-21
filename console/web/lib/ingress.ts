import type { AppDefinitionSummary } from "@core/api-deployments/app-definition";
import { appCommandType, type AppCommandType } from "@core/api-deployments/commands";

// An App has no customer routes: its generated Edge Worker serves one command
// path per SDK call the backend makes (POST /commands/{type}), an optional
// public homepage, and the exact webhook paths the App definition declares.
// "route" is any other path that was observed — a probe the Edge Worker
// answered with 404, or the single /commands path earlier SDK builds used.
export type IngressRole = "command" | "homepage" | "webhook" | "route";

export function ingressRole(
  definition: AppDefinitionSummary | undefined,
  route: { method: string; path: string },
): IngressRole {
  if (!definition) return "route";
  if (route.method === "POST" && appCommandType(route.path) !== undefined) return "command";
  if (route.path === "/" && (route.method === "GET" || route.method === "HEAD") && definition.homepage) return "homepage";
  if (definition.webhooks.some(({ method, path }) => method === route.method && path === route.path)) return "webhook";
  return "route";
}

export const ingressRoleLabel: Record<IngressRole, string> = {
  command: "SDK command",
  homepage: "Homepage",
  webhook: "Webhook",
  route: "",
};

/** Whether a request can usefully be sent from the console: commands need the
 *  App's integration credential and the backend client's runtime header, so
 *  they are exercised through the SDK rather than by hand. */
export function consoleSendable(role: IngressRole): boolean {
  return role !== "command";
}

export type CommandGroup = "messages" | "session" | "workspace" | "environment";

export const commandGroupLabel: Record<CommandGroup, string> = {
  messages: "Messages",
  session: "Session state",
  workspace: "Workspace",
  environment: "Environment",
};

export interface CommandInfo {
  type: AppCommandType;
  /** The SDK call that sends it, as the backend writes it. */
  call: string;
  group: CommandGroup;
  /** The command delivers a message into a Session, so its outcome continues
   *  past the edge into Session work. */
  work: boolean;
}

const commands: readonly CommandInfo[] = [
  { type: "dispatch", call: "session.dispatch()", group: "messages", work: true },
  { type: "steer", call: "session.steer()", group: "messages", work: true },
  { type: "request", call: "session.request()", group: "messages", work: true },
  { type: "cancel", call: "session.cancel()", group: "messages", work: true },
  { type: "status", call: "message.status()", group: "session", work: false },
  { type: "view", call: "session.view()", group: "session", work: false },
  { type: "stream", call: "session.stream()", group: "session", work: false },
  { type: "stop", call: "session.stop()", group: "session", work: false },
  { type: "workspace.resolve", call: "workspace.resolve()", group: "workspace", work: false },
  { type: "workspace.database", call: "workspace.database()", group: "workspace", work: false },
  { type: "workspace.inspect", call: "workspace.inspect()", group: "workspace", work: false },
  { type: "workspace.delete", call: "workspace.delete()", group: "workspace", work: false },
  { type: "app.environment.set", call: "app.environment().set()", group: "environment", work: false },
  { type: "app.environment.unset", call: "app.environment().unset()", group: "environment", work: false },
  { type: "app.environment.status", call: "app.environment().status()", group: "environment", work: false },
  { type: "auth", call: "workspace.auth()", group: "workspace", work: true },
];

const byType = new Map(commands.map((command) => [command.type, command]));

/** What a command path is, or undefined for any other path. */
export function commandInfo(route: { method: string; path: string }): CommandInfo | undefined {
  if (route.method !== "POST") return undefined;
  const type = appCommandType(route.path);
  return type === undefined ? undefined : byType.get(type);
}

/** Commands in the order the Ingress page lists them. */
export function commandOrder(info: CommandInfo): number {
  return commands.indexOf(info);
}
