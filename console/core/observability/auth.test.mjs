import { expect, test } from "bun:test";

import { authenticateObservationIngest, authorizeObservationSources } from "./auth";

const token = "a".repeat(64);
const edgeToken = "b".repeat(64);

test("authenticates central observation producers", () => {
  expect(authenticateObservationIngest(`Bearer ${token}`, {
    CANTELOP_OBSERVABILITY_INGEST_TOKEN: token,
    CANTELOP_EDGE_OBSERVABILITY_INGEST_TOKEN: edgeToken,
  })).toBe("runtime");
  expect(authenticateObservationIngest(`Bearer ${edgeToken}`, {
    CANTELOP_OBSERVABILITY_INGEST_TOKEN: token,
    CANTELOP_EDGE_OBSERVABILITY_INGEST_TOKEN: edgeToken,
  })).toBe("edge");
  expect(() => authenticateObservationIngest(`Bearer ${"b".repeat(64)}`, {
    CANTELOP_OBSERVABILITY_INGEST_TOKEN: token,
    CANTELOP_EDGE_OBSERVABILITY_INGEST_TOKEN: "c".repeat(64),
  })).toThrow();
  expect(() => authenticateObservationIngest(`Bearer ${token}, Bearer ${token}`, {
    CANTELOP_OBSERVABILITY_INGEST_TOKEN: token,
    CANTELOP_EDGE_OBSERVABILITY_INGEST_TOKEN: edgeToken,
  })).toThrow();
  expect(() => authenticateObservationIngest(`Bearer ${token}`, {
    CANTELOP_OBSERVABILITY_INGEST_TOKEN: token,
    CANTELOP_EDGE_OBSERVABILITY_INGEST_TOKEN: token,
  })).toThrow();
});

test("scopes each ingestion credential to its producer class", () => {
  expect(() => authorizeObservationSources("edge", ["edge"])).not.toThrow();
  expect(() => authorizeObservationSources("runtime", ["sdk", "fire_fuse", "gateway"])).not.toThrow();
  expect(() => authorizeObservationSources("edge", ["edge", "sdk"])).toThrow();
  expect(() => authorizeObservationSources("runtime", ["fire_fuse", "edge"])).toThrow();
});
