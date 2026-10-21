"use client";

import { useEffect, useState } from "react";

/**
 * The value of a promise a server component streams in after the first
 * paint: undefined until it settles. A newer promise (router.refresh) keeps
 * the last value on screen until it settles in turn. The server resolves
 * failures to a value of their own, so a rejection never reaches here.
 */
export function useStreamed<T>(promise: Promise<T>): T | undefined {
  const [settled, setSettled] = useState<{ value: T } | undefined>(undefined);
  useEffect(() => {
    let current = true;
    void promise.then((value) => { if (current) setSettled({ value }); });
    return () => { current = false; };
  }, [promise]);
  return settled?.value;
}
