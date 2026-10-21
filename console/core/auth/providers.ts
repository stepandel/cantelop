/** The identity providers the console signs people in with. A user is keyed
 *  by a provider-verified email, and each provider subject is a linked sign-in
 *  method, so the same person arriving through both providers is one user.
 *
 *  Nothing else lives here: the sign-in buttons ship this list to the browser,
 *  and identity resolution is server-only. */
export const IDENTITY_PROVIDERS = ["github", "google"] as const;
export type IdentityProvider = (typeof IDENTITY_PROVIDERS)[number];

export function isIdentityProvider(value: unknown): value is IdentityProvider {
  return typeof value === "string" && (IDENTITY_PROVIDERS as readonly string[]).includes(value);
}
