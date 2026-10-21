import { expect, test } from "bun:test";
import { sourceInfo as resolveSourceInfo } from "../lib/source-info.ts";

const sourceInfo = (environment) => resolveSourceInfo(environment, null);

const commit = "0123456789abcdef0123456789abcdef01234567";

test("a publication pin maps private deployments to their public snapshot", () => {
  const publication = { schema_version: 1, repository: "https://github.com/stepandel/cantelop", commit, snapshot_sha256: "a".repeat(64) };
  expect(resolveSourceInfo({ VERCEL_GIT_COMMIT_SHA: "f".repeat(40) }, publication).sourceURL).toBe(`https://github.com/stepandel/cantelop/tree/${commit}`);
  expect(() => resolveSourceInfo({}, { ...publication, snapshot_sha256: null })).toThrow("invalid public source pin");
  expect(resolveSourceInfo({ CANTELOP_SOURCE_REPOSITORY: "https://github.com/operator/fork", VERCEL_GIT_COMMIT_SHA: "f".repeat(40) }, publication).commit).toBe("f".repeat(40));
});

test("source offers identify the deployed revision and allow a fork repository", () => {
  expect(sourceInfo({ VERCEL_GIT_COMMIT_SHA: commit }).sourceURL).toBe(`https://github.com/stepandel/cantelop/tree/${commit}`);
  expect(sourceInfo({ CANTELOP_SOURCE_COMMIT: commit, CANTELOP_SOURCE_REPOSITORY: "https://github.com/operator/fork.git" }).sourceURL).toBe(`https://github.com/operator/fork/tree/${commit}`);
  expect(() => sourceInfo({ CANTELOP_SOURCE_COMMIT: commit, VERCEL_GIT_COMMIT_SHA: "f".repeat(40) })).toThrow("does not match");
});

test("source offers never substitute a moving branch for a missing revision", () => {
  expect(sourceInfo({})).toBeNull();
  expect(() => sourceInfo({ CANTELOP_SOURCE_COMMIT: "main" })).toThrow();
});

test("source offers reject credential-bearing and unsafe repository URLs", () => {
  for (const repository of ["http://github.com/operator/fork", "https://user:secret@github.com/operator/fork", "https://github.com/operator/fork?token=secret", "https://github.com/operator/fork#main", "https://github.com/"]) {
    expect(() => sourceInfo({ CANTELOP_SOURCE_COMMIT: commit, CANTELOP_SOURCE_REPOSITORY: repository })).toThrow();
  }
});

test("source offers validate the repository after removing a trailing .git", () => {
  expect(() => sourceInfo({ CANTELOP_SOURCE_COMMIT: commit, CANTELOP_SOURCE_REPOSITORY: "https://github.com/.git" })).toThrow();
  expect(sourceInfo({ CANTELOP_SOURCE_COMMIT: commit, CANTELOP_SOURCE_REPOSITORY: "https://github.com/operator/fork.git/" }).repository).toBe("https://github.com/operator/fork");
});
