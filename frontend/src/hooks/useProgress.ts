import { useState, useCallback, useRef } from "react";
import type { ProgressEvent } from "@/lib/types";

export function useProgress() {
  const [progress, setProgress] = useState<ProgressEvent | null>(null);
  const sourceRef = useRef<EventSource | null>(null);

  const connect = useCallback(() => {
    disconnect();
    const es = new EventSource("/api/progress");
    sourceRef.current = es;

    es.onmessage = (event) => {
      const data: ProgressEvent = JSON.parse(event.data);
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
  }, []);

  return { progress, connect, disconnect, reset };
}
