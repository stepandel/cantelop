import { expect, test } from "bun:test";
import { GetObjectCommand, HeadBucketCommand } from "@aws-sdk/client-s3";

import { configuredAPIArtifactStore, S3APIArtifactStore } from "./s3-artifact-store";

const module = Buffer.from("export default { fetch() {} }");
const objectKey = "deployment-uploads/0123456789abcdef0123456789abcdef/api/worker.mjs";

test("checks storage and reads a staged release API module", async () => {
  const commands = [];
  const client = { async send(command) {
    commands.push(command);
    if (command instanceof GetObjectCommand) return {
      ContentLength: module.length,
      Body: { async transformToByteArray() { return module; } },
    };
    return {};
  } };
  const store = new S3APIArtifactStore(client, "cantelop-artifacts");
  await store.check();
  expect(commands[0]).toBeInstanceOf(HeadBucketCommand);
  expect(await store.get(objectKey)).toEqual(module);
  expect(commands[1]).toBeInstanceOf(GetObjectCommand);
  expect(commands[1].input).toMatchObject({ Bucket: "cantelop-artifacts", Key: objectKey });
});

test("requires a secured S3-compatible artifact endpoint", () => {
  const base = {
    CANTELOP_ARTIFACT_BUCKET: "cantelop-artifacts",
    CANTELOP_ARTIFACT_ACCESS_KEY_ID: "access-key",
    CANTELOP_ARTIFACT_SECRET_ACCESS_KEY: "secret-key",
  };
  expect(() => configuredAPIArtifactStore({ ...base, CANTELOP_ARTIFACT_ENDPOINT: "http://r2.example.com" })).toThrow("HTTPS origin");
  expect(() => configuredAPIArtifactStore({ ...base, CANTELOP_ARTIFACT_ENDPOINT: "https://r2.example.com/private" })).toThrow("HTTPS origin");
  expect(() => configuredAPIArtifactStore({ ...base, CANTELOP_ARTIFACT_ENDPOINT: "https://r2.example.com" })).not.toThrow();
});
