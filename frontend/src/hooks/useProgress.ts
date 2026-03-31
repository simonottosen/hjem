import { useState, useCallback, useRef } from "react";
import type { ProgressEvent } from "@/lib/types";

export function useProgress() {
  const [progress, setProgress] = useState<ProgressEvent | null>(null);
  const sourceRef = useRef<EventSource | null>(null);
  const seenActiveRef = useRef(false);

  const connect = useCallback(() => {
    disconnect();
    seenActiveRef.current = false;
    const es = new EventSource("/api/progress");
    sourceRef.current = es;

    es.onmessage = (event) => {
      const data: ProgressEvent = JSON.parse(event.data);

      // Ignore stale "done"/"idle" events that arrive before the new
      // search has started on the server. Only start trusting events
      // once we see an active stage (dawa, boliga_list, etc.).
      if (!seenActiveRef.current) {
        if (data.stage === "done" || data.stage === "idle" || data.stage === "error") {
          return; // stale from previous search — skip
        }
        seenActiveRef.current = true;
      }

      setProgress(data);

      if (data.stage === "done" || data.stage === "error") {
        es.close();
        sourceRef.current = null;
      }
    };

    es.onerror = () => {
      es.close();
      sourceRef.current = null;
    };
  }, []);

  const disconnect = useCallback(() => {
    if (sourceRef.current) {
      sourceRef.current.close();
      sourceRef.current = null;
    }
  }, []);

  const reset = useCallback(() => {
    setProgress(null);
    seenActiveRef.current = false;
  }, []);

  return { progress, connect, disconnect, reset };
}
