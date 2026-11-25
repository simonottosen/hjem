import { History } from "lucide-react";
import { formatDate } from "@/lib/formatting";

// A result opened from a link can be up to a month old, and nothing else on
// the page says so: its sales and estimates look exactly like today's.
export function StoredResultBanner({ createdAt }: { createdAt: string }) {
  return (
    <div className="rounded-lg border bg-muted/50 px-4 py-3 text-sm text-muted-foreground">
      <div className="flex items-start gap-2">
        <History className="size-4 mt-0.5 shrink-0" />
        <p>
          Resultat fra {formatDate(createdAt)}. Søg igen for at hente de nyeste
          salg.
        </p>
      </div>
    </div>
  );
}
