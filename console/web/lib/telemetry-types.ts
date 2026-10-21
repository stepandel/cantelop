import type { PublicWorkspace } from "@core/product-service";

export type SessionState = "active" | "idle";
export type SandboxState = "starting" | "running" | "stopping" | "terminated" | "failed";

export interface SessionStartup {
  completed_at: string;
  duration_ms: number;
}

export interface SessionUsage {
  observed_at: string;
  cpu_usage_us: number;
  // Physical host RAM charged to the VM, retained for infrastructure accounting.
  memory_current_bytes: number;
  // Guest MemTotal minus MemAvailable; absent when guest telemetry is unavailable.
  guest_memory_used_bytes?: number;
  guest_memory_mib?: number;
}

// A tenant Workspace exactly as the product API serves it. The Workspace
// views also show last_activity_at when a record carries it.
export type Workspace = PublicWorkspace & { last_activity_at?: string };

// The observer's own Workspace record: the host keeps no timestamps.
export interface HostWorkspace {
  id: string;
  app_id: string;
  slug: string;
  archived_at?: string;
}

export interface Session {
  id: string;
  workspace_id: string;
  app_id: string;
  build_id: string;
  keep_alive_seconds: number;
  state: SessionState;
  startup?: SessionStartup;
  created_at: string;
  updated_at: string;
}

export interface RuntimeTiming {
  status: "pending" | "running" | "stopped" | "not_started" | "unknown";
  started_at?: string;
  stopped_at?: string;
}

export interface Sandbox {
  runtime_timing?: RuntimeTiming;
  id: string;
  workspace_id: string;
  app_id: string;
  profile: string;
  build_id: string;
  vcpus: number;
  memory_mib: number;
  memory_max_mib?: number;
  state: SandboxState;
  failure?: { reason?: string };
  startup?: SessionStartup;
  usage?: SessionUsage;
  created_at: string;
  updated_at: string;
}

// The operator snapshot read straight from the observer carries HostWorkspace
// records; a customer snapshot is a Snapshot<Workspace> of the tenant's own.
// Every Workspace is also a HostWorkspace, so views typed Snapshot take both.
export interface Snapshot<W extends HostWorkspace = HostWorkspace> {
  ready: boolean;
  observed_at: string;
  capacity?: {
    warm_slots: {
      target: number;
      ready: number;
      claimed: number;
      preparing: number;
    };
  };
  workspaces: W[];
  sessions: Session[];
  sandboxes: Sandbox[];
}

export interface RequestEvent {
  sequence: number;
  event_id: string;
  request_id?: string;
  workspace_id: string;
  session_id?: string;
  received_at: string;
  status_code: number;
  duration_ms: number;
}

export interface RequestEventPage {
  items: RequestEvent[];
  next_before_sequence?: number;
}

// The edge leg of a request: one Cloudflare Worker invocation and the
// application logs it emitted. Recorded by the edge, so it carries the method,
// path and logs the workspace gateway never sees.
export interface AppRequestLog {
  timestamp: string;
  level: string;
  message: string;
}

export interface AppRequestInvocation {
  request_id: string;
  timestamp: string;
  method?: string;
  path?: string;
  status?: number;
  duration_ms?: number;
  outcome?: string;
  logs: AppRequestLog[];
}
