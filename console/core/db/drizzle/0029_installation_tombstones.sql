CREATE TABLE "cantelop_control"."fleet_installation_tombstones" (
	"fleet_id" text NOT NULL,
	"external_key" text NOT NULL,
	"created_at" timestamp with time zone DEFAULT now() NOT NULL,
	CONSTRAINT "fleet_installation_tombstones_fleet_id_external_key_pk" PRIMARY KEY("fleet_id","external_key")
);
--> statement-breakpoint
ALTER TABLE "cantelop_control"."fleet_installation_tombstones" ADD CONSTRAINT "fleet_installation_tombstones_fleet_id_fleets_id_fk" FOREIGN KEY ("fleet_id") REFERENCES "cantelop_control"."fleets"("id") ON DELETE no action ON UPDATE no action;