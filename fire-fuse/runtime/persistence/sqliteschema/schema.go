package sqliteschema

const Canonical = baseCanonical + SessionStreams + ActiveSandboxes + ManagedApps

const baseCanonical = `
CREATE TABLE compute_tenants (
    id TEXT PRIMARY KEY,
    created_at TEXT NOT NULL
) STRICT;
CREATE TABLE apps (
    id TEXT PRIMARY KEY,
	slug TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    deleted_at TEXT
) STRICT;
CREATE UNIQUE INDEX apps_routable_slug ON apps(slug) WHERE deleted_at IS NULL;
CREATE TABLE app_tenants (
    app_id TEXT PRIMARY KEY REFERENCES apps(id) ON DELETE CASCADE,
    tenant_id TEXT NOT NULL REFERENCES compute_tenants(id),
    created_at TEXT NOT NULL
) STRICT;
CREATE INDEX app_tenants_tenant_id ON app_tenants(tenant_id, app_id);
CREATE TABLE app_builds (
    app_id TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    version INTEGER NOT NULL CHECK (version > 0),
    build_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (app_id, version),
    UNIQUE (app_id, build_id)
) STRICT;
CREATE TABLE app_build_operations (
    id TEXT PRIMARY KEY,
    app_id TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    status TEXT NOT NULL CHECK (status IN ('receiving', 'queued', 'processing', 'succeeded', 'failed')),
    size_bytes INTEGER NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    build_id TEXT,
    build_version INTEGER CHECK (build_version > 0),
    error_code TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (app_id, build_version) REFERENCES app_builds(app_id, version),
    CHECK ((status = 'succeeded') = (build_id IS NOT NULL AND build_version IS NOT NULL)),
    CHECK ((status = 'failed') = (error_code IS NOT NULL))
) STRICT;
CREATE UNIQUE INDEX app_build_operations_pending_app
    ON app_build_operations(app_id)
    WHERE status IN ('receiving', 'queued', 'processing');
CREATE TABLE app_session_runtimes (
    app_id TEXT PRIMARY KEY REFERENCES apps(id) ON DELETE CASCADE,
	active_build_id TEXT,
	profile TEXT NOT NULL,
	internal_port INTEGER NOT NULL CHECK (internal_port BETWEEN 1 AND 65535),
    FOREIGN KEY (app_id, active_build_id)
        REFERENCES app_builds(app_id, build_id)
        DEFERRABLE INITIALLY DEFERRED
) STRICT;
CREATE TABLE app_release_admission (
    app_id TEXT PRIMARY KEY REFERENCES apps(id) ON DELETE CASCADE,
    generation TEXT NOT NULL,
    closed INTEGER NOT NULL CHECK (closed IN (0, 1))
) STRICT;
CREATE TRIGGER app_release_admission_insert AFTER INSERT ON apps
BEGIN
    INSERT INTO app_release_admission (app_id, generation, closed) VALUES (NEW.id, '', 1);
END;
CREATE TABLE workspaces (
    id TEXT PRIMARY KEY,
    app_id TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    slug TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    archived_at TEXT
) STRICT;
CREATE INDEX workspaces_app_id ON workspaces(app_id, id);
CREATE UNIQUE INDEX workspaces_routable_slug ON workspaces(app_id, slug)
    WHERE archived_at IS NULL;
CREATE TABLE app_config_values (
    app_id TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('variable', 'secret')),
    value_text TEXT,
    value_nonce BLOB,
    value_ciphertext BLOB,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (app_id, name),
    CHECK (
        (kind = 'variable' AND value_text IS NOT NULL AND value_nonce IS NULL AND value_ciphertext IS NULL)
        OR
        (kind = 'secret' AND value_text IS NULL AND value_nonce IS NOT NULL AND value_ciphertext IS NOT NULL)
    )
) STRICT;
CREATE TABLE app_config_revisions (
    app_id TEXT PRIMARY KEY REFERENCES apps(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL CHECK (revision >= 0)
) STRICT;
CREATE TABLE sandboxes (
    id TEXT PRIMARY KEY,
    state TEXT NOT NULL,
    record BLOB NOT NULL
) STRICT;
CREATE TABLE host_slots (
    id TEXT PRIMARY KEY,
    state TEXT NOT NULL,
    record BLOB NOT NULL
) STRICT;
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    build_id TEXT NOT NULL,
    sandbox_id TEXT NOT NULL UNIQUE REFERENCES sandboxes(id),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    keep_alive_seconds INTEGER NOT NULL CHECK (keep_alive_seconds BETWEEN 0 AND 604800)
) STRICT;
CREATE INDEX sessions_workspace_id ON sessions(workspace_id, id);
CREATE TABLE session_keys (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    key_hash TEXT NOT NULL CHECK (length(key_hash) = 64),
    session_id TEXT NOT NULL UNIQUE REFERENCES sessions(id),
    PRIMARY KEY (workspace_id, key_hash)
) STRICT;
CREATE TABLE session_work_leases (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    lease_id TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (session_id, lease_id)
) STRICT;
CREATE INDEX session_work_leases_expiry
    ON session_work_leases(expires_at, session_id, lease_id);
CREATE TABLE request_events (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id TEXT NOT NULL UNIQUE,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    session_id TEXT REFERENCES sessions(id),
    received_at TEXT NOT NULL,
    status_code INTEGER NOT NULL CHECK (status_code BETWEEN 100 AND 599),
    duration_us INTEGER NOT NULL CHECK (duration_us >= 0),
    request_id TEXT
) STRICT;
CREATE INDEX request_events_workspace_sequence
    ON request_events(workspace_id, sequence DESC);
CREATE TABLE message_deliveries (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    logical_session_id TEXT NOT NULL,
    keep_alive_seconds INTEGER NOT NULL CHECK (keep_alive_seconds BETWEEN 0 AND 604800),
    status TEXT NOT NULL CHECK (status IN ('accepted', 'handling', 'handled', 'failed')),
    work_state TEXT NOT NULL CHECK (work_state IN ('pending', 'delivering', 'awaiting_quiescence', 'quiescent', 'unavailable')),
    payload_nonce BLOB NOT NULL,
    payload_ciphertext BLOB NOT NULL,
    session_id TEXT REFERENCES sessions(id),
    generation INTEGER CHECK (generation BETWEEN 1 AND 9007199254740991),
    error_code TEXT,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    accepted_at TEXT NOT NULL,
    started_at TEXT,
    handled_at TEXT,
    quiescent_at TEXT,
    updated_at TEXT NOT NULL,
    request_id TEXT,
    reply_requested INTEGER NOT NULL DEFAULT 0 CHECK (reply_requested IN (0, 1)),
    reply_nonce BLOB,
    reply_ciphertext BLOB,
    admitted_at TEXT,
    CHECK (
        (status = 'accepted' AND work_state = 'pending' AND session_id IS NULL AND generation IS NULL AND error_code IS NULL AND started_at IS NULL AND handled_at IS NULL AND quiescent_at IS NULL)
        OR (status = 'handling' AND work_state = 'delivering' AND attempts > 0 AND session_id IS NULL AND generation IS NULL AND error_code IS NULL AND started_at IS NOT NULL AND handled_at IS NULL AND quiescent_at IS NULL)
        OR (status = 'handled' AND work_state IN ('awaiting_quiescence', 'quiescent', 'unavailable') AND attempts > 0 AND session_id IS NOT NULL AND generation IS NOT NULL AND error_code IS NULL AND started_at IS NOT NULL AND handled_at IS NOT NULL AND ((work_state = 'quiescent') = (quiescent_at IS NOT NULL)))
        OR (status = 'failed' AND attempts > 0 AND error_code IS NOT NULL AND started_at IS NOT NULL AND handled_at IS NOT NULL AND ((generation IS NULL AND work_state = 'unavailable' AND quiescent_at IS NULL) OR (generation IS NOT NULL AND session_id IS NOT NULL AND work_state IN ('awaiting_quiescence', 'quiescent', 'unavailable') AND ((work_state = 'quiescent') = (quiescent_at IS NOT NULL)))))
    )
) STRICT;
CREATE INDEX message_deliveries_dispatch
    ON message_deliveries(status, work_state, accepted_at, id);
CREATE INDEX message_deliveries_session
    ON message_deliveries(workspace_id, logical_session_id, id);
CREATE INDEX message_deliveries_terminal_updated
    ON message_deliveries(work_state, updated_at, id)
    WHERE work_state IN ('quiescent', 'unavailable');
CREATE TABLE message_executions (
    message_id TEXT PRIMARY KEY REFERENCES message_deliveries(id) ON DELETE CASCADE,
    record BLOB NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    terminal INTEGER NOT NULL CHECK (terminal IN (0, 1))
) STRICT;
CREATE TABLE message_delivery_events (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id TEXT NOT NULL REFERENCES message_deliveries(id) ON DELETE CASCADE,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    logical_session_id TEXT NOT NULL,
    session_id TEXT,
    request_id TEXT,
    attempt INTEGER NOT NULL CHECK (attempt >= 0),
    generation INTEGER CHECK (generation BETWEEN 1 AND 9007199254740991),
    transition TEXT NOT NULL CHECK (transition IN ('accepted', 'handling', 'requeued', 'settled', 'failed', 'quiescent', 'unavailable')),
    status TEXT NOT NULL CHECK (status IN ('accepted', 'handling', 'handled', 'failed')),
    work_state TEXT NOT NULL CHECK (work_state IN ('pending', 'delivering', 'awaiting_quiescence', 'quiescent', 'unavailable')),
    error_code TEXT,
    occurred_at TEXT NOT NULL,
    CHECK (
        (transition = 'accepted' AND status = 'accepted' AND work_state = 'pending' AND attempt = 0 AND session_id IS NULL AND generation IS NULL AND error_code IS NULL)
        OR (transition = 'handling' AND status = 'handling' AND work_state = 'delivering' AND attempt > 0 AND session_id IS NULL AND generation IS NULL AND error_code IS NULL)
        OR (transition = 'requeued' AND status = 'accepted' AND work_state = 'pending' AND attempt > 0 AND session_id IS NULL AND generation IS NULL AND error_code IS NULL)
        OR (transition = 'settled' AND status IN ('handled', 'failed') AND work_state = 'awaiting_quiescence' AND attempt > 0 AND session_id IS NOT NULL AND generation IS NOT NULL AND ((status = 'failed') = (error_code IS NOT NULL)))
        OR (transition = 'failed' AND status = 'failed' AND work_state = 'unavailable' AND attempt > 0 AND generation IS NULL AND error_code IS NOT NULL)
        OR (transition = 'quiescent' AND status IN ('handled', 'failed') AND work_state = 'quiescent' AND attempt > 0 AND session_id IS NOT NULL AND generation IS NOT NULL AND ((status = 'failed') = (error_code IS NOT NULL)))
        OR (transition = 'unavailable' AND status IN ('handled', 'failed') AND work_state = 'unavailable' AND attempt > 0 AND session_id IS NOT NULL AND generation IS NOT NULL AND ((status = 'failed') = (error_code IS NOT NULL)))
    )
) STRICT;
CREATE INDEX message_delivery_events_workspace_sequence
    ON message_delivery_events(workspace_id, sequence DESC);
CREATE INDEX message_delivery_events_message_sequence
    ON message_delivery_events(message_id, sequence);
`

const SessionStreams = `
CREATE TABLE session_streams (
 workspace_id TEXT NOT NULL, session_id TEXT NOT NULL, stream_id TEXT NOT NULL UNIQUE,
 sequence INTEGER NOT NULL DEFAULT 0, closed INTEGER NOT NULL DEFAULT 0,
 view_revision INTEGER, view_updated_at TEXT, view_nonce BLOB, view_ciphertext BLOB,
 PRIMARY KEY(workspace_id,session_id)
) STRICT;
CREATE TABLE session_stream_entries (
 stream_id TEXT NOT NULL REFERENCES session_streams(stream_id) ON DELETE CASCADE,
 sequence INTEGER NOT NULL, source_sandbox_id TEXT NOT NULL, source_cursor INTEGER NOT NULL,
 message_id TEXT NOT NULL, created_at TEXT NOT NULL, projection INTEGER NOT NULL,
 nonce BLOB, ciphertext BLOB, fingerprint BLOB NOT NULL,
 PRIMARY KEY(stream_id,sequence), UNIQUE(stream_id,source_sandbox_id,source_cursor)
) STRICT;
`

// Managed Apps start denied in the same transaction that creates the App.
const ManagedApps = `
CREATE INDEX IF NOT EXISTS sandboxes_managed_app_state ON sandboxes(CASE WHEN json_valid(record) THEN json_extract(record,'$.app_id') END,state);
CREATE TABLE IF NOT EXISTS managed_apps (
 app_id TEXT PRIMARY KEY REFERENCES apps(id) ON DELETE CASCADE,
 record BLOB NOT NULL
) STRICT;
CREATE TABLE IF NOT EXISTS managed_storage (
 workspace_id TEXT PRIMARY KEY, app_id TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE, bytes INTEGER NOT NULL CHECK(bytes>0)
) STRICT;
CREATE TABLE IF NOT EXISTS managed_usage (
 id TEXT PRIMARY KEY, app_id TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
 record BLOB NOT NULL
) STRICT;
`

// ActiveSandboxes avoids scanning terminated records during admission and cleanup.
const ActiveSandboxes = `
CREATE INDEX IF NOT EXISTS sandboxes_active ON sandboxes(id) WHERE state <> 'terminated';
`
