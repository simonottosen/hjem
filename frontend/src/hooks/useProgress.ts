import { useState, useCallback, useRef } from "react";
import type { ProgressEvent, LookupResponse } from "@/lib/types";
import { fetchProgress, postBoligaIngest, SessionGoneError } from "@/lib/api";
import { runBoligaTasks } from "@/lib/boliga";

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

  // Aborts the browser's Boliga fetching when this search is abandoned.
  // Generation checks alone would only discard the results: the requests
  // themselves would run to completion, competing with the replacing search
  // for the browser's six connections to the same host.
  const relayRef = useRef<AbortController | null>(null);

  const stop = useCallback(() => {
    generationRef.current += 1;
    relayRef.current?.abort();
    relayRef.current = null;
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

      // The server holds the boliga_client stage for up to 20s while it waits
      // for us, so polling every 2s sees it repeatedly. Without this the same
      // streets would be fetched ten times over.
      let relayStarted = false;

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

        if (data.stage === "boliga_client" && data.boliga_tasks && !relayStarted) {
          relayStarted = true;
          const ctrl = new AbortController();
          relayRef.current = ctrl;
          runBoligaTasks(data.boliga_tasks, ctrl.signal)
            // Nothing to post for a search the user has replaced: stop() has
            // already aborted the relay, so the outcome is a partial one for a
            // lookup no one is waiting on.
            .then((outcome) => {
              if (current()) postBoligaIngest(lookupId, outcome);
            })
            .catch((err) => console.warn("[hjem] Boliga relay failed:", err));
        }

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
