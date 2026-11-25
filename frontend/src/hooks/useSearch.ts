import { useState, useCallback, useEffect, useRef } from "react";
import type { LookupResponse } from "@/lib/types";
import { cancelLookup, startLookup, type LookupSession } from "@/lib/api";

export function useSearch() {
  const [data, setData] = useState<LookupResponse | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // A ref, not state: this is only sent back to the server on the next search
  // so it can cancel our previous lookup. Nothing renders from it.
  const lookupIdRef = useRef<string | null>(null);
  // Whether the lookup in lookupIdRef is still running on the server. Cleared
  // when its result or error is delivered, which both arrive through this
  // hook. Left alone when starting a new search fails: a refusal can come
  // before the server got as far as replacing the previous lookup, so if that
  // one was running it may still be, with nobody polling it. Cancelling one
  // that did go is a harmless 404.
  const inFlightRef = useRef(false);

  // A closed tab can no longer poll, but its lookup keeps one of the server's
  // few concurrent slots until it finishes on its own — minutes, for a large
  // radius. pagehide rather than beforeunload or unload: it is the one event
  // mobile browsers reliably fire when a tab goes away, and listening for it
  // does not keep the page out of the back/forward cache. It also fires when
  // the page goes into that cache, though, marked persisted: that page is kept
  // whole and resumes polling on Back or Forward, so its lookup is left running.
  useEffect(() => {
    const onPageHide = (e: PageTransitionEvent) => {
      if (e.persisted) {
        return;
      }
      if (inFlightRef.current && lookupIdRef.current) {
        cancelLookup(lookupIdRef.current);
      }
    };
    window.addEventListener("pagehide", onPageHide);
    return () => window.removeEventListener("pagehide", onPageHide);
  }, []);

  const search = useCallback(
    async (
      query: string,
      range: number,
      filter: number,
      onStarted: (session: LookupSession) => void
    ) => {
      setIsLoading(true);
      setError(null);
      setData(null);

      try {
        const session = await startLookup(query, range, filter, lookupIdRef.current);
        lookupIdRef.current = session.lookupId;
        inFlightRef.current = true;
        onStarted(session); // Signal that polling should begin
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
        setIsLoading(false);
      }
    },
    []
  );

  const setResult = useCallback((result: LookupResponse) => {
    inFlightRef.current = false;
    setData(result);
    setIsLoading(false);
  }, []);

  const setSearchError = useCallback((msg: string) => {
    inFlightRef.current = false;
    setError(msg);
    setIsLoading(false);
  }, []);

  return { data, isLoading, error, search, setResult, setSearchError };
}
