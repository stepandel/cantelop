export interface PresignedUploadTarget {
  url: string;
  headers: Readonly<Record<string, string>>;
}

export interface MultipartUploadPartTarget extends PresignedUploadTarget {
  partNumber: number;
}

export interface MultipartUpload {
  storeUploadID: string;
  partSizeBytes: number;
  partCount: number;
}

export interface CompletedUploadPart {
  partNumber: number;
  etag: string;
}

export interface DeploymentArtifactStore {
  createSessionRuntimeUpload(objectKey: string, sizeBytes: number, digest: string): Promise<MultipartUpload>;
  abortSessionRuntimeUpload(objectKey: string, storeUploadID: string): Promise<void>;
  deleteSessionRuntimeObject(objectKey: string): Promise<void>;
  signAPIUpload(objectKey: string, sizeBytes: number, digest: string, expiresInSeconds: number): Promise<PresignedUploadTarget>;
  signSessionRuntimeParts(
    objectKey: string,
    upload: MultipartUpload,
    sizeBytes: number,
    expiresInSeconds: number,
  ): Promise<MultipartUploadPartTarget[]>;
  completeSessionRuntimeUpload(
    objectKey: string,
    storeUploadID: string,
    parts: CompletedUploadPart[],
    sizeBytes: number,
    partSizeBytes: number,
  ): Promise<void>;
  inspectObject(objectKey: string, sizeBytes: number, digest: string): Promise<"missing" | "matches" | "mismatch">;
  signSessionRuntimeDownload(objectKey: string, expiresInSeconds: number): Promise<string>;
}
