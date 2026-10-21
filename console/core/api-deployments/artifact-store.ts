export interface APIArtifactStore {
  check(): Promise<void>;
  get(objectKey: string): Promise<Buffer>;
}
