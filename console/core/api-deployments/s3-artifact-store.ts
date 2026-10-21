import { GetObjectCommand, HeadBucketCommand, S3Client } from "@aws-sdk/client-s3";

import type { APIArtifactStore } from "./artifact-store";

interface S3Sender {
  send(command: GetObjectCommand | HeadBucketCommand): Promise<unknown>;
}

export class S3APIArtifactStore implements APIArtifactStore {
  constructor(private readonly client: S3Sender, private readonly bucket: string) {
    if (!/^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$/.test(bucket)) throw new TypeError("invalid artifact bucket");
  }

  async check(): Promise<void> {
    await this.client.send(new HeadBucketCommand({ Bucket: this.bucket }));
  }

  async get(objectKey: string): Promise<Buffer> {
    if (!/^deployment-uploads\/[0-9a-f]{32}\/api\/worker\.mjs$/.test(objectKey)) {
      throw new TypeError("invalid artifact object key");
    }
    const result = await this.client.send(new GetObjectCommand({ Bucket: this.bucket, Key: objectKey })) as {
      Body?: { transformToByteArray(): Promise<Uint8Array> };
      ContentLength?: number;
    };
    if (!result.Body || (result.ContentLength !== undefined && (result.ContentLength < 1 || result.ContentLength > 1 << 20))) {
      throw new Error("artifact store returned an invalid object");
    }
    const body = Buffer.from(await result.Body.transformToByteArray());
    if (body.length < 1 || body.length > 1 << 20) throw new Error("artifact store returned an invalid object");
    return body;
  }
}

export function configuredAPIArtifactStore(environment: NodeJS.ProcessEnv = process.env): S3APIArtifactStore {
  const bucket = required(environment, "CANTELOP_ARTIFACT_BUCKET");
  const endpoint = new URL(required(environment, "CANTELOP_ARTIFACT_ENDPOINT"));
  if (endpoint.protocol !== "https:" || endpoint.username || endpoint.password || endpoint.search || endpoint.hash || endpoint.pathname !== "/") {
    throw new Error("CANTELOP_ARTIFACT_ENDPOINT must be an HTTPS origin");
  }
  const accessKeyId = required(environment, "CANTELOP_ARTIFACT_ACCESS_KEY_ID");
  const secretAccessKey = required(environment, "CANTELOP_ARTIFACT_SECRET_ACCESS_KEY");
  const region = environment.CANTELOP_ARTIFACT_REGION?.trim() || "auto";
  if (!/^[A-Za-z0-9][A-Za-z0-9-]{0,31}$/.test(region)) throw new Error("invalid artifact region");
  return new S3APIArtifactStore(new S3Client({
    endpoint: endpoint.origin,
    region,
    credentials: { accessKeyId, secretAccessKey },
  }), bucket);
}

function required(environment: NodeJS.ProcessEnv, name: string): string {
  const value = environment[name]?.trim();
  if (!value) throw new Error(`${name} is required`);
  return value;
}
