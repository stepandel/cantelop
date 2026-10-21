export interface APIProviderRelease {
  id: string;
  etag: string;
  scriptName: string;
}

export interface APIProvider {
  deploy(appID: string, releaseID: string, module: Buffer): Promise<APIProviderRelease>;
  removeRelease(appID: string, releaseID: string): Promise<void>;
}

/** Curated diagnostic code, safe to retain in customer deployment logs. */
export class APIProviderError extends Error {
  constructor(message: string, readonly progress: string) {
    super(message);
    if (!/^api_[a-z0-9_]{1,59}$/.test(progress)) throw new TypeError("invalid API provider progress");
  }
}
