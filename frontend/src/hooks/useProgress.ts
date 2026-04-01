import { useState, useCallback, useRef } from "react";
import type { ProgressEvent, LookupResponse } from "@/lib/types";
import { fetchProgress } from "@/lib/api";

const POLL_INTERVAL_MS = 2000;

export function useProgress() {
  const [progress, setProgress] = useState<ProgressEvent | null>(null);
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const onResultRef = useRef<((data: LookupResponse) => void) | null>(null);
  const onErrorRef = useRef<((msg: string) => void) | null>(null);

  const stop = useCallback(() => {
    if (intervalRef.current) {
      clearInterval(intervalRef.current);
      intervalRef.current = null;
    }
  }, []);

  const startPolling = useCallback(
    (
      onResult: (data: LookupResponse) => void,
      onError: (msg: string) => void
    ) => {
      stop();
      onResultRef.current = onResult;
      onErrorRef.current = onError;

      const poll = async () => {
        try {
          const data = await fetchProgress();
          setProgress(data);

          if (data.stage === "done" && data.result) {
            stop();
            onResultRef.current?.(data.result as LookupResponse);
          } else if (data.stage === "error") {
            stop();
            onErrorRef.current?.(data.message || "Ukendt fejl");
          }
        } catch {
          // Network error during poll — keep trying
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
