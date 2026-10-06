// Loads the map only once a lookup actually has one to draw. maplibre-gl more
// than doubles the bundle, and most visits never start a search at all.
//
// A plain dynamic import rather than React.lazy: lazy() rethrows a failed
// chunk load into the nearest error boundary, and this app has none, so a
// blocked CDN or a stale cached entry point would blank the whole page over a
// decoration. Here it just stays absent, which is what the progress UI is
// built to survive anyway.

import { useEffect, useState, type ComponentType } from "react";
import type { ProgressMapProps } from "./ProgressMapCanvas";

export function ProgressMap({ map }: ProgressMapProps) {
  const [Canvas, setCanvas] = useState<ComponentType<ProgressMapProps> | null>(null);
  const wanted = map !== null;

  useEffect(() => {
    if (!wanted) return;
    let live = true;
    import("./ProgressMapCanvas")
      // Wrapped in a function: a bare component is itself callable, and
      // setState would run it as an updater instead of storing it.
      .then((m) => live && setCanvas(() => m.ProgressMapCanvas))
      .catch((err) => console.warn("[hjem] Progress map unavailable:", err));
    return () => {
      live = false;
    };
  }, [wanted]);

  if (!Canvas || !wanted) return null;
  return <Canvas map={map} />;
}
