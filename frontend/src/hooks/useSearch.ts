import { useState, useCallback, useRef } from "react";
import type { LookupResponse } from "@/lib/types";
import { startLookup } from "@/lib/api";

export function useSearch() {
  const [data, setData] = useState<LookupResponse | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // A ref, not state: this is only sent back to the server on the next search
  // so it can cancel our previous lookup. Nothing renders from it.
  const lookupIdRef = useRef<string | null>(null);

  const search = useCallback(
    async (
      query: string,
      range: number,
      filter: number,
      onStarted: (lookupId: string) => void
    ) => {
      setIsLoading(true);
      setError(null);
      setData(null);

      try {
        const lookupId = await startLookup(query, range, filter, lookupIdRef.current);
        lookupIdRef.current = lookupId;
        onStarted(lookupId); // Signal that polling should begin
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
        setIsLoading(false);
      }
    },
    []
  );

  const setResult = useCallback((result: LookupResponse) => {
    setData(result);
    setIsLoading(false);
  }, []);

  const setSearchError = useCallback((msg: string) => {
    setError(msg);
    setIsLoading(false);
  }, []);

  return { data, isLoading, error, search, setResult, setSearchError };
}
