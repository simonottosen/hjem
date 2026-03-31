import type { ProgressEvent } from "@/lib/types";
import { Progress } from "@/components/ui/progress";
import { formatTime } from "@/lib/formatting";

interface ProgressBarProps {
  progress: ProgressEvent | null;
}

export function ProgressBar({ progress }: ProgressBarProps) {
  if (!progress) {
    return (
      <div className="space-y-2">
        <Progress value={undefined} className="h-3" />
        <p className="text-sm text-muted-foreground">Søger...</p>
      </div>
    );
  }

  const pct =
    progress.total > 0 && progress.current > 0
      ? Math.round((progress.current / progress.total) * 100)
      : undefined;

  let timeStr = "";
  if (progress.elapsed_ms > 0) {
    timeStr = "Tid: " + formatTime(progress.elapsed_ms);
    if (
      progress.total > 0 &&
      progress.current > 0 &&
      progress.current < progress.total
    ) {
      const msPerItem = progress.elapsed_ms / progress.current;
      const remaining = msPerItem * (progress.total - progress.current);
      timeStr += " | Ca. " + formatTime(remaining) + " tilbage";
    }
  }

  return (
    <div className="space-y-2">
      <Progress value={pct} className="h-3" />
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
