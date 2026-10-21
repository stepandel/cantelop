CREATE TABLE cantelop_control.deployment_logs (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  release_id text NOT NULL CONSTRAINT deployment_logs_release_id_releases_id_fk REFERENCES cantelop_control.releases(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  source text NOT NULL,
  level text NOT NULL,
  message text NOT NULL,
  CONSTRAINT deployment_logs_source_check CHECK (source IN ('deployment', 'api', 'session_runtime')),
  CONSTRAINT deployment_logs_level_check CHECK (level IN ('info', 'warning', 'error'))
);
--> statement-breakpoint
CREATE INDEX deployment_logs_release ON cantelop_control.deployment_logs(release_id, id);
--> statement-breakpoint
-- Capture transitions in the same transaction, including cancellation and stale-worker
-- recovery. Only curated codes are recorded; never operation input/output or URLs.
CREATE FUNCTION cantelop_control.log_release_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'UPDATE' THEN
    IF NEW.status IS NOT DISTINCT FROM OLD.status AND NEW.error_code IS NOT DISTINCT FROM OLD.error_code THEN RETURN NEW; END IF;
  END IF;
  INSERT INTO cantelop_control.deployment_logs(release_id, source, level, message)
  VALUES (NEW.id, 'deployment', CASE WHEN NEW.status = 'failed' THEN 'error' ELSE 'info' END,
    'Deployment ' || NEW.status || COALESCE(': ' || NEW.error_code, ''));
  RETURN NEW;
END;
$$;
--> statement-breakpoint
CREATE TRIGGER deployment_release_log AFTER INSERT OR UPDATE ON cantelop_control.releases
FOR EACH ROW EXECUTE FUNCTION cantelop_control.log_release_transition();
--> statement-breakpoint
CREATE FUNCTION cantelop_control.log_component_created() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO cantelop_control.deployment_logs(release_id, source, level, message)
  VALUES (NEW.release_id, NEW.kind, 'info', 'Queued for deployment');
  RETURN NEW;
END;
$$;
--> statement-breakpoint
CREATE TRIGGER deployment_component_log AFTER INSERT ON cantelop_control.release_components
FOR EACH ROW EXECUTE FUNCTION cantelop_control.log_component_created();
--> statement-breakpoint
CREATE FUNCTION cantelop_control.log_deployment_operation() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  component record;
  message text;
  severity text := 'info';
BEGIN
  SELECT release_id, kind INTO component FROM cantelop_control.release_components WHERE operation_id = NEW.id;
  IF NOT FOUND THEN RETURN NEW; END IF;
  IF NEW.status IS DISTINCT FROM OLD.status OR NEW.attempts IS DISTINCT FROM OLD.attempts THEN
    message := CASE NEW.status
      WHEN 'running' THEN 'Started attempt ' || NEW.attempts
      WHEN 'queued' THEN 'Retry scheduled for ' || to_char(NEW.available_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')
      WHEN 'succeeded' THEN 'Completed successfully'
      WHEN 'failed' THEN 'Failed: ' || COALESCE(NEW.error_code, 'unknown_error')
      ELSE NEW.status END;
    severity := CASE WHEN NEW.status = 'failed' THEN 'error' WHEN NEW.status = 'queued' THEN 'warning' ELSE 'info' END;
    INSERT INTO cantelop_control.deployment_logs(release_id, source, level, message)
    VALUES (component.release_id, component.kind, severity, message);
  END IF;
  IF NEW.progress IS DISTINCT FROM OLD.progress AND NEW.progress IS NOT NULL THEN
    INSERT INTO cantelop_control.deployment_logs(release_id, source, level, message)
    VALUES (component.release_id, component.kind,
      CASE WHEN NEW.progress ~ '(unavailable|unreachable|invalid|http_|failed)' THEN 'warning' ELSE 'info' END,
      NEW.progress);
  END IF;
  RETURN NEW;
END;
$$;
--> statement-breakpoint
CREATE TRIGGER deployment_operation_log AFTER UPDATE ON cantelop_control.operations
FOR EACH ROW EXECUTE FUNCTION cantelop_control.log_deployment_operation();
