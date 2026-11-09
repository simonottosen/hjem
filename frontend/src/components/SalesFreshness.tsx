import { formatDate } from "@/lib/formatting";

// Boliga only learns of a sale once the deed is registered, weeks or months
// after the purchase date, so the newest sale on a freshly fetched page is
// routinely months old. Without the fetch date and the reason beside it, that
// reads as a page that has stopped updating.
export function SalesFreshness({ fetchedAt }: { fetchedAt?: string }) {
  if (!fetchedAt) return null;
  return (
    <p className="text-xs text-muted-foreground mb-3">
      Salg hentet fra Boliga {formatDate(fetchedAt)}. Boliga får først et salg
      med, når skødet er tinglyst — typisk 1–3 måneder efter købsdatoen — så de
      nyeste salg mangler ofte endnu.
    </p>
  );
}
