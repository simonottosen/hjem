import { useCallback, useRef } from "react";
import { RotateCcw } from "lucide-react";
import { cn } from "@/lib/utils";
import { formatBucket, pickResolution } from "@/lib/timeseries";

interface TimeRangeSliderProps {
  /** Full available domain [minMs, maxMs]. */
  min: number;
  max: number;
  /** Current selected window [startMs, endMs]. */
  value: [number, number];
  onChange: (range: [number, number]) => void;
}

type Thumb = "start" | "end";

/**
 * A shared two-handle time-range slider. Drag either handle to shrink or grow
 * the visible window; the whole dashboard's charts zoom in sync. A minimum
 * window of ~14 days keeps the handles from crossing.
 */
const MIN_SPAN = 14 * 24 * 60 * 60 * 1000;

export function TimeRangeSlider({ min, max, value, onChange }: TimeRangeSliderProps) {
  const trackRef = useRef<HTMLDivElement>(null);
  const [start, end] = value;
  const domain = Math.max(1, max - min);

  const pctOf = (ms: number) => ((ms - min) / domain) * 100;
  const isZoomed = start > min || end < max;
  const res = pickResolution(end - start);

  const msFromClientX = useCallback(
    (clientX: number) => {
      const track = trackRef.current;
      if (!track) return min;
      const rect = track.getBoundingClientRect();
      const frac = Math.min(1, Math.max(0, (clientX - rect.left) / rect.width));
      return min + frac * domain;
    },
    [min, domain]
  );

  const startDrag = useCallback(
    (thumb: Thumb) => (e: React.PointerEvent) => {
      e.preventDefault();
      (e.target as HTMLElement).setPointerCapture(e.pointerId);

      const onMove = (ev: PointerEvent) => {
        const pos = msFromClientX(ev.clientX);
        if (thumb === "start") {
          onChange([Math.min(pos, end - MIN_SPAN), end]);
        } else {
          onChange([start, Math.max(pos, start + MIN_SPAN)]);
        }
      };
      const onUp = () => {
        window.removeEventListener("pointermove", onMove);
        window.removeEventListener("pointerup", onUp);
      };
      window.addEventListener("pointermove", onMove);
      window.addEventListener("pointerup", onUp);
    },
    [start, end, msFromClientX, onChange]
  );

  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between text-xs text-muted-foreground">
        <span className="font-medium text-foreground">{formatBucket(start, res)}</span>
        <span className="hidden sm:inline">Træk i håndtagene for at zoome</span>
        {isZoomed ? (
          <button
            onClick={() => onChange([min, max])}
            className="inline-flex items-center gap-1 rounded-sm px-1.5 py-0.5 hover:bg-accent hover:text-accent-foreground transition-colors"
          >
            <RotateCcw className="size-3" />
            Nulstil
          </button>
        ) : (
          <span className="font-medium text-foreground">{formatBucket(end, res)}</span>
        )}
      </div>

      <div className="relative flex h-6 items-center px-2">
        <div
          ref={trackRef}
          className="relative h-1.5 w-full rounded-full bg-muted"
        >
          {/* selected window */}
          <div
            className="absolute h-full rounded-full bg-primary/70"
            style={{ left: `${pctOf(start)}%`, right: `${100 - pctOf(end)}%` }}
          />
          {(["start", "end"] as Thumb[]).map((thumb) => {
            const ms = thumb === "start" ? start : end;
            return (
              <div
                key={thumb}
                role="slider"
                aria-label={thumb === "start" ? "Startdato" : "Slutdato"}
                aria-valuemin={min}
                aria-valuemax={max}
                aria-valuenow={ms}
                onPointerDown={startDrag(thumb)}
                className={cn(
                  "absolute top-1/2 size-4 -translate-x-1/2 -translate-y-1/2",
                  "cursor-ew-resize rounded-full border-2 border-primary bg-background shadow-sm",
                  "touch-none hover:scale-110 transition-transform"
                )}
                style={{ left: `${pctOf(ms)}%` }}
              />
            );
          })}
        </div>
      </div>

      {isZoomed && (
        <div className="flex items-center justify-between text-xs text-muted-foreground sm:hidden">
          <span>{formatBucket(start, res)}</span>
          <span>{formatBucket(end, res)}</span>
        </div>
      )}
    </div>
  );
}
