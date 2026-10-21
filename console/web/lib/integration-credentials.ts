import type { IntegrationCredentials } from "@core/configuration/repository";

/** What the Credentials page may hold without the token itself. */
export type CredentialState =
  | { issued: false }
  | {
    issued: true;
    slug: string;
    edge_url: string;
    previous_token_expires_at: string | null;
    rotation: IntegrationCredentials["rotation"];
  };

export function publicCredentialState(credentials: IntegrationCredentials): CredentialState {
  return {
    issued: true,
    slug: credentials.slug,
    edge_url: credentials.edge_url,
    previous_token_expires_at: credentials.previous_token_expires_at,
    rotation: credentials.rotation,
  };
}

/** Grace periods the page offers; the API accepts any value up to seven days. */
export const rotationGraceOptions = [
  { seconds: 24 * 60 * 60, label: "Keep the old token for 24 hours" },
  { seconds: 60 * 60, label: "Keep the old token for 1 hour" },
  { seconds: 7 * 24 * 60 * 60, label: "Keep the old token for 7 days" },
  { seconds: 0, label: "Revoke the old token immediately" },
] as const;

/** The backend environment the SDK reads when no integration profile is set. */
export function backendEnvironment(slug: string, token = "<token>"): string {
  return `CANTELOP_APP_SLUG=${slug}\nCANTELOP_INTEGRATION_TOKEN=${token}`;
}
