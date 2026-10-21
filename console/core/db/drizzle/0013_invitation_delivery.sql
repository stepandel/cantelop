ALTER TABLE cantelop_control.app_invitations DROP CONSTRAINT app_invitations_delivery_check;
--> statement-breakpoint
ALTER TABLE cantelop_control.app_invitations ADD CONSTRAINT app_invitations_delivery_check CHECK (delivery IN ('sending', 'sent', 'failed', 'delivered', 'delayed', 'bounced', 'complained', 'suppressed'));
--> statement-breakpoint
ALTER TABLE cantelop_control.app_invitations ADD COLUMN provider_message_id text;
--> statement-breakpoint
CREATE UNIQUE INDEX app_invitations_provider_message ON cantelop_control.app_invitations(provider_message_id);
--> statement-breakpoint
CREATE INDEX app_invitations_recipient_time ON cantelop_control.app_invitations(email, created_at);
--> statement-breakpoint
CREATE TABLE cantelop_control.invitation_suppressions (
  recipient_hash text PRIMARY KEY CHECK (recipient_hash ~ '^[a-f0-9]{64}$'),
  reason text NOT NULL CHECK (reason IN ('opt_out', 'bounced', 'complained', 'suppressed')),
  created_at timestamptz NOT NULL
);
--> statement-breakpoint
CREATE TABLE cantelop_control.invitation_delivery_events (
  id text PRIMARY KEY,
  invitation_id text NOT NULL REFERENCES cantelop_control.app_invitations(id) ON DELETE CASCADE,
  type text NOT NULL,
  created_at timestamptz NOT NULL
);
