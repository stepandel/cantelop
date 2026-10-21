DO $$
BEGIN
  -- Fresh installations already receive the Workspace-only schema from 0000.
  -- The legacy environments table is the upgrade marker for installations
  -- that applied the original 0000 migration before that schema was unified.
  IF to_regclass('cantelop_control.environments') IS NULL THEN
    RETURN;
  END IF;

  IF EXISTS (
    SELECT 1
    FROM cantelop_control.workspaces AS workspace
    LEFT JOIN cantelop_control.environments AS environment
      ON environment.workspace_id = workspace.id
     AND environment.app_id = workspace.app_id
    WHERE environment.id IS NULL
  ) THEN
    RAISE EXCEPTION 'cannot migrate a Workspace without its Environment projection';
  END IF;

  ALTER TABLE cantelop_control.user_quotas
    RENAME COLUMN max_runtime_instances TO max_sessions;
  ALTER TABLE cantelop_control.user_quotas
    RENAME CONSTRAINT user_quotas_max_runtime_instances_check
    TO user_quotas_max_sessions_check;

  ALTER TABLE cantelop_control.app_harnesses
    RENAME COLUMN max_runtime_instances TO max_sessions;
  ALTER TABLE cantelop_control.app_harnesses
    RENAME CONSTRAINT app_harnesses_max_runtime_instances_check
    TO app_harnesses_max_sessions_check;

  ALTER TABLE cantelop_control.operations
    DROP CONSTRAINT operations_kind_check;

  UPDATE cantelop_control.operations AS operation
  SET kind = 'register_workspace',
      idempotency_key = 'register-workspace:' || environment.workspace_id,
      input = jsonb_build_object(
        'workspace_id', environment.workspace_id,
        'workspace_slug', environment.slug
      ),
      output = CASE
        WHEN operation.output IS NULL THEN NULL
        ELSE (operation.output - 'provider_environment_id') ||
          jsonb_build_object('provider_workspace_id', environment.workspace_id)
      END
  FROM cantelop_control.environments AS environment
  WHERE operation.kind = 'register_environment'
    AND operation.input->>'environment_id' = environment.id;

  IF EXISTS (
    SELECT 1 FROM cantelop_control.operations
    WHERE kind = 'register_environment'
  ) THEN
    RAISE EXCEPTION 'cannot resolve a register_environment operation to a Workspace';
  END IF;

  UPDATE cantelop_control.operations AS operation
  SET input = (operation.input
        - 'environment_id'
        - 'environment_slug'
        - 'max_runtime_instances') ||
      jsonb_build_object(
        'workspace_slug', environment.slug,
        'max_sessions', operation.input->'max_runtime_instances'
      )
  FROM cantelop_control.environments AS environment
  WHERE operation.kind = 'register_app'
    AND operation.input ? 'environment_id'
    AND operation.input->>'environment_id' = environment.id;

  IF EXISTS (
    SELECT 1 FROM cantelop_control.operations
    WHERE kind = 'register_app' AND input ? 'environment_id'
  ) THEN
    RAISE EXCEPTION 'cannot resolve a register_app operation to a Workspace';
  END IF;

  ALTER TABLE cantelop_control.operations
    ADD CONSTRAINT operations_kind_check CHECK (
      kind IN ('register_app', 'register_workspace', 'sync_config',
               'deploy_api', 'materialize_build', 'archive_app')
    );

  ALTER TABLE cantelop_control.workspaces
    ADD COLUMN slug text,
    ADD COLUMN archived_at timestamp with time zone;

  UPDATE cantelop_control.workspaces AS workspace
  SET slug = environment.slug,
      created_at = environment.created_at,
      updated_at = environment.updated_at,
      archived_at = CASE
        WHEN environment.status IN ('disabled', 'archived')
          THEN COALESCE(environment.archived_at, environment.updated_at)
        ELSE NULL
      END
  FROM cantelop_control.environments AS environment
  WHERE environment.workspace_id = workspace.id
    AND environment.app_id = workspace.app_id;

  ALTER TABLE cantelop_control.workspaces
    ALTER COLUMN slug SET NOT NULL;

  DROP TABLE cantelop_control.environments;

  ALTER TABLE cantelop_control.workspaces
    DROP CONSTRAINT workspaces_app_id_id_unique,
    DROP CONSTRAINT workspaces_status_check,
    DROP COLUMN status;
  ALTER TABLE cantelop_control.workspaces
    ADD CONSTRAINT workspaces_app_slug_unique UNIQUE (app_id, slug),
    ADD CONSTRAINT workspaces_slug_check CHECK (
      slug ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'
      AND position('--' IN slug) = 0
    );
END
$$;
