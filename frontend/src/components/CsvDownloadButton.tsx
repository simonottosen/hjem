import { Download } from "lucide-react";
import { Button } from "@/components/ui/button";

interface CsvDownloadButtonProps {
  query: string;
  range: number;
}

export function CsvDownloadButton({ query, range }: CsvDownloadButtonProps) {
  const url =
    "/download/csv?q=" +
    encodeURIComponent(query) +
    "&range=" +
    encodeURIComponent(range);

  return (
    <Button variant="outline" size="sm" asChild>
      <a href={url}>
        <Download />
        Download CSV
      </a>
    </Button>
  );
}
