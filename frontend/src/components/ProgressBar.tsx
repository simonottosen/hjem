import { useEffect, useRef, useState } from "react";
import type { ProgressEvent } from "@/lib/types";
import { Progress } from "@/components/ui/progress";
import { formatTime } from "@/lib/formatting";

interface ProgressBarProps {
  progress: ProgressEvent | null;
}

// Prior estimate: 4 seconds per item (based on observed Boliga API timing)
const PRIOR_MS_PER_ITEM = 4000;
// How many completed items before the actual rate starts dominating the prior
// Higher = slower to change from the 4s estimate
const BLEND_WEIGHT = 8;

export function ProgressBar({ progress }: ProgressBarProps) {
  const [displayPct, setDisplayPct] = useState(0);
  const animRef = useRef<number>(0);

  // Track the real target percentage and smoothly animate toward it
  const targetPctRef = useRef(0);
  const lastUpdateRef = useRef(Date.now());
  const estimatedTotalMsRef = useRef(0);

  useEffect(() => {
    if (!progress || progress.total <= 0 || progress.current < 0) {
      targetPctRef.current = 0;
      return;
    }

    const { current, total, elapsed_ms } = progress;

    // Blended ms-per-item estimate:
    // Starts at PRIOR (4s), gradually incorporates actual rate as items complete.
    // Formula: weighted average where prior gets BLEND_WEIGHT "virtual" items.
    const priorTotal = PRIOR_MS_PER_ITEM * BLEND_WEIGHT;
    const actualTotal = current > 0 ? elapsed_ms : 0;
    const blendedMsPerItem =
      (priorTotal + actualTotal) / (BLEND_WEIGHT + current);

    const estimatedTotalMs = blendedMsPerItem * total;
    estimatedTotalMsRef.current = estimatedTotalMs;

    // Target percentage based on elapsed time vs estimated total
    // This gives smooth progress even between discrete server updates
    const pct = Math.min(99, (elapsed_ms / estimatedTotalMs) * 100);
    targetPctRef.current = pct;
    lastUpdateRef.current = Date.now();
  }, [progress]);

  // Animation loop: smoothly interpolate displayPct toward targetPct
  // and slowly creep forward between server updates
  useEffect(() => {
    let running = true;

    function tick() {
      if (!running) return;

      setDisplayPct((prev) => {
        const target = targetPctRef.current;

        if (target <= 0) return 0;

        // Creep: advance slowly based on time since last server update
        // This keeps the bar moving between discrete SSE events
        const msSinceUpdate = Date.now() - lastUpdateRef.current;
        const estimatedTotal = estimatedTotalMsRef.current;
        let creep = 0;
        if (estimatedTotal > 0) {
          creep = (msSinceUpdate / estimatedTotal) * 100;
        }

        const effectiveTarget = Math.min(99, target + creep);

        // Smooth toward target: move 8% of the gap per frame
        const gap = effectiveTarget - prev;
        if (Math.abs(gap) < 0.05) return effectiveTarget;
        return prev + gap * 0.08;
      });

      animRef.current = requestAnimationFrame(tick);
    }

    animRef.current = requestAnimationFrame(tick);
    return () => {
      running = false;
      cancelAnimationFrame(animRef.current);
    };
  }, []);

  // Compute time strings from blended estimate
  let timeStr = "";
  if (progress && progress.elapsed_ms > 0) {
    timeStr = "Tid: " + formatTime(progress.elapsed_ms);

    if (
      progress.total > 0 &&
      progress.current >= 0 &&
      estimatedTotalMsRef.current > 0
    ) {
      const remaining = Math.max(
        0,
        estimatedTotalMsRef.current - progress.elapsed_ms
      );
      if (remaining > 0) {
        timeStr += " | Ca. " + formatTime(remaining) + " tilbage";
      }
    }
  }

  if (!progress) {
    return (
      <div className="space-y-2">
        <Progress value={undefined} className="h-3" />
        <p className="text-sm text-muted-foreground">Søger...</p>
      </div>
    );
  }

  return (
    <div className="space-y-2">
      <Progress
        value={Math.round(displayPct)}
        className="h-3"
      />
      <div className="flex justify-between items-center">
        <p className="text-sm text-muted-foreground">
          {progress.message || "Arbejder..."}
        </p>
        {timeStr && (
          <p className="text-xs text-muted-foreground italic">{timeStr}</p>
        )}
      </div>
    </div>
  );
}
