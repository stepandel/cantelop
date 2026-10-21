export type InfrastructureRange = "1h" | "6h" | "24h";

export interface InfrastructurePoint {
  at: string;
  values: Array<number | null>;
}

export interface InfrastructureChart {
  id: string;
  title: string;
  units: string;
  dimensions: string[];
  points: InfrastructurePoint[];
}

// One machine: compute runs the Netdata parent, and storage and the builder
// stream to it. `reachable` is false when the machine's newest sample is
// stale or it has never streamed at all (no `last_collected_at`).
export interface InfrastructureHost {
  id: string;
  title: string;
  role: string;
  reachable: boolean;
  last_collected_at?: string;
  charts: InfrastructureChart[];
}

export interface InfrastructureSnapshot {
  observed_at: string;
  range: InfrastructureRange;
  // Absent from observers that predate per-host metrics; `charts` then holds
  // the single compute host's charts.
  hosts?: InfrastructureHost[];
  charts: InfrastructureChart[];
}
