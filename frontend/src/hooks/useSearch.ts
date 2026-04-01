import { useState, useCallback } from "react";
import type { LookupResponse } from "@/lib/types";
import { startLookup } from "@/lib/api";

export function useSearch() {
  const [data, setData] = useState<LookupResponse | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const search = useCallback(
    async (
      query: string,
      range: number,
      filter: number,
      onStarted: () => void
    ) => {
      setIsLoading(true);
      setError(null);
      setData(null);

      try {
        await startLookup(query, range, filter);
        onStarted(); // Signal that polling should begin
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
