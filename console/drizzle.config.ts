import { defineConfig } from "drizzle-kit";

export default defineConfig({
  dialect: "postgresql",
  schema: "./core/db/schema.ts",
  out: "./core/db/drizzle",
  migrations: {
    table: "__drizzle_migrations",
    schema: "cantelop_control",
  },
  strict: true,
  verbose: true,
});
