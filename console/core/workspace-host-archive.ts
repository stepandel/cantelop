import type { ProductRepository } from "./db/product-repository";

// The gateway opens host-registered Workspaces by slug without asking product
// control. Only acknowledge a deletion once the host stops resolving the
// Workspace, so a later open by the same slug gets a replacement.
export async function waitForWorkspaceHostArchive(
  products: Pick<ProductRepository, "isWorkspaceHostArchived">,
  workspaceID: string,
  timeoutMilliseconds = 5_000,
  intervalMilliseconds = 100,
  sleep: (milliseconds: number) => Promise<void> = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds)),
): Promise<boolean> {
  for (let waited = 0; ; waited += intervalMilliseconds) {
    if (await products.isWorkspaceHostArchived(workspaceID)) return true;
    if (waited >= timeoutMilliseconds) return false;
    await sleep(intervalMilliseconds);
  }
}
