import { AlertTriangle } from "lucide-react";

interface WarningBannerProps {
  warnings: string[];
}

export function WarningBanner({ warnings }: WarningBannerProps) {
  if (!warnings.length) return null;

  return (
    <div className="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800">
      <div className="flex items-start gap-2">
        <AlertTriangle className="size-4 mt-0.5 shrink-0" />
        <div className="space-y-1">
          {warnings.map((w, i) => (
            <p key={i}>{w}</p>
          ))}
          <p className="text-xs text-amber-600 mt-2">
            Søg igen senere for at hente de manglende data — allerede hentede
            gader genbruges fra cache.
          </p>
        </div>
      </div>
    </div>
  );
}
