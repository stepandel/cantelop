interface Worker { runOnce(): Promise<boolean>; readonly idleDelayMilliseconds?: number }

// Each queue advances independently. A long image download must not prevent
// cancellation (or operations for unrelated apps) from being picked up.
export async function runWorkerLoops(workers: Worker[], once: boolean, signal?: AbortSignal): Promise<void> {
  await Promise.all(workers.map(async (worker) => {
    do {
      const worked = await worker.runOnce();
      if (once || signal?.aborted) return;
      if (!worked) await new Promise((resolve) => setTimeout(resolve, worker.idleDelayMilliseconds ?? 1000));
    } while (!signal?.aborted);
  }));
}
