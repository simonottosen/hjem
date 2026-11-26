// Loads the drawing map only when the user asks for it, for the same reason
// and in the same way as ProgressMap: maplibre-gl is most of the bundle, and a
// plain dynamic import keeps a failed chunk from blanking the page.
//
// Unlike the progress map this one was asked for, so a failure says so rather
// than leaving the user pressing a button that does nothing.

import { useEffect, useState, type ComponentType } from "react";
import type { AreaPickerProps } from "./AreaPickerCanvas";

export function AreaPicker(props: AreaPickerProps) {
  const [Canvas, setCanvas] = useState<ComponentType<AreaPickerProps> | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let live = true;
    import("./AreaPickerCanvas")
      // Wrapped in a function: a bare component is itself callable, and
      // setState would run it as an updater instead of storing it.
      .then((m) => live && setCanvas(() => m.AreaPickerCanvas))
      .catch((err) => {
        console.warn("[hjem] Area map unavailable:", err);
        if (live) setFailed(true);
      });
    return () => {
      live = false;
    };
  }, []);

  if (failed) {
    return <p className="text-sm text-destructive">Kortet kunne ikke indlæses. Søg med radius i stedet.</p>;
  }
  if (!Canvas) return <p className="text-sm text-muted-foreground">Indlæser kort...</p>;
  return <Canvas {...props} />;
}
