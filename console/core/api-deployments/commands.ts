// Kept free of other imports so the console's client code can name commands
// without bundling the manifest parser.

/** The SDK commands a backend sends, each at POST /commands/{type}. Mirrors
 *  APP_COMMAND_TYPES in @cantelop/sdk, in the order the SDK lists them. */
export const appCommandTypes = [
  "app.environment.set", "app.environment.unset", "app.environment.status",
  "workspace.resolve", "workspace.database", "workspace.inspect", "workspace.delete",
  "dispatch", "steer", "request", "auth", "cancel", "status", "view", "stop", "stream",
] as const;
export type AppCommandType = (typeof appCommandTypes)[number];

export const appCommandPathPrefix = "/commands/";

/** The command a path addresses, or undefined for any other path. */
export function appCommandType(path: string): AppCommandType | undefined {
  if (!path.startsWith(appCommandPathPrefix)) return undefined;
  const type = path.slice(appCommandPathPrefix.length);
  return (appCommandTypes as readonly string[]).includes(type) ? type as AppCommandType : undefined;
}
