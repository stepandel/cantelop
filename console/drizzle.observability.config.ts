import { defineConfig } from "drizzle-kit";

export default defineConfig({
  dialect: "postgresql",
  schema: "./core/observability/schema.ts",
  out: "./core/observability/drizzle",
  migrations: {
    table: "__drizzle_migrations",
    schema: "cantelop_observability",
  },
  strict: true,
  verbose: true,
});
