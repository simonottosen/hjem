import { useState, useCallback } from "react";
import type { LookupResponse } from "@/lib/types";
import { searchLookup } from "@/lib/api";

export function useSearch() {
  const [data, setData] = useState<LookupResponse | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const search = useCallback(
    async (query: string, range: number, filter: number) => {
      setIsLoading(true);
      setError(null);
      setData(null);

      try {
        const result = await searchLookup(query, range, filter);
        setData(result);
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
      } finally {
        setIsLoading(false);
      }
    },
    []
  );

  return { data, isLoading, error, search };
}
