import { Download } from "lucide-react";
import { Button } from "@/components/ui/button";
import { encodePolyline, type LonLat } from "@/lib/area";

interface CsvDownloadButtonProps {
  query: string;
  range: number;
  // Set for a drawn search, which the server is then asked for instead of the
  // radius.
  area?: LonLat[] | null;
}

export function CsvDownloadButton({ query, range, area }: CsvDownloadButtonProps) {
  const url =
    "/download/csv?q=" +
    encodeURIComponent(query) +
    (area
      ? "&a=" + encodeURIComponent(encodePolyline(area))
      : "&range=" + encodeURIComponent(range));

  return (
    <Button variant="outline" size="sm" asChild>
      <a href={url}>
        <Download />
        Download CSV
      </a>
    </Button>
  );
}
