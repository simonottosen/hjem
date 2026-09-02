import { useState, useCallback, useRef } from "react";
import type { ProgressEvent, LookupResponse } from "@/lib/types";
import { fetchProgress, SessionGoneError } from "@/lib/api";

const POLL_INTERVAL_MS = 2000;

export function useProgress() {
  const [progress, setProgress] = useState<ProgressEvent | null>(null);
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const onResultRef = useRef<((data: LookupResponse) => void) | null>(null);
  const onErrorRef = useRef<((msg: string) => void) | null>(null);

  // Bumped by stop(), captured by each poll. Clearing the interval is not
  // enough on its own: a fetch that is already in flight still resolves, and
  // the id it asked about may be one the server has since replaced. Answering
  // that late 404 would clear the *new* search's interval and show its user an
  // error, leaving a live lookup stuck behind a dead spinner. Comparing
  // generations is what lets a retired poll recognise itself and do nothing.
  const generationRef = useRef(0);

  const stop = useCallback(() => {
    generationRef.current += 1;
    if (intervalRef.current) {
      clearInterval(intervalRef.current);
      intervalRef.current = null;
    }
  }, []);

  const startPolling = useCallback(
    (
      lookupId: string,
      onResult: (data: LookupResponse) => void,
      onError: (msg: string) => void
    ) => {
      stop();
      const generation = generationRef.current;
      const current = () => generation === generationRef.current;

      onResultRef.current = onResult;
      onErrorRef.current = onError;

      const poll = async () => {
        let data: ProgressEvent;
        try {
          data = await fetchProgress(lookupId);
        } catch (err) {
          if (!current()) return;
          if (err instanceof SessionGoneError) {
            // Not transient: this id will never come back, so polling it
            // forever would leave the user on a spinner indefinitely.
            stop();
            onErrorRef.current?.(err.message);
            return;
          }
          console.warn("[hjem] Progress poll failed:", err);
          // Keep polling — transient network error
          return;
        }

        if (!current()) return;
        setProgress(data);

        if (data.stage === "done" && data.result) {
          stop();
          const result = data.result as LookupResponse;
          // Merge, don't assign. The result carries warnings the server
          // derived from the data itself (an unvaluable subject property),
          // while the progress stream carries the Boliga fetch failures;
          // assigning either one over the other silently drops the rest.
          // Fetch failures currently reach us through both, hence the dedupe.
          if (data.warnings?.length) {
            result.warnings = [
              ...new Set([...(result.warnings ?? []), ...data.warnings]),
            ];
          }
          onResultRef.current?.(result);
        } else if (data.stage === "error") {
          stop();
          onErrorRef.current?.(data.message || "Ukendt fejl");
        }
      };

      // Poll immediately, then on interval
      poll();
      intervalRef.current = setInterval(poll, POLL_INTERVAL_MS);
    },
    [stop]
  );

  const reset = useCallback(() => {
    stop();
    setProgress(null);
  }, [stop]);

  return { progress, startPolling, reset };
}
