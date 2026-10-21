export interface AppRouteCommand {
  appID: string;
  appSlug: string;
}

export interface AppRoutingProvider {
  publish(command: AppRouteCommand): Promise<void>;
  remove(command: AppRouteCommand): Promise<void>;
}

export class AppRoutingProviderError extends Error {
  constructor(readonly retryable: boolean) {
    super("App routing provider request failed");
    this.name = "AppRoutingProviderError";
  }
}
