import type { ConfigProjectionValue } from "./repository";

export interface ConfigurationProjection {
  appID: string;
  scriptName?: string;
  revision: number;
  values: ConfigProjectionValue[];
  systemBindings?:ConfigProjectionValue[];
}

export interface APIConfigurationProvider {
  replaceConfiguration(projection: ConfigurationProjection): Promise<void>;
}

export class ConfigurationProviderError extends Error {
  constructor(readonly retryable: boolean) {
    super("configuration provider request failed");
    this.name = "ConfigurationProviderError";
  }
}
