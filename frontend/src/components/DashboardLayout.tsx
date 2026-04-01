import type { LookupResponse } from "@/lib/types";
import { MetricsRow } from "./MetricsRow";
import { PriceScatterChart } from "./PriceScatterChart";
import { SqMeterLineChart } from "./SqMeterLineChart";
import { SalesTable } from "./SalesTable";
import { CsvDownloadButton } from "./CsvDownloadButton";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";

interface DashboardLayoutProps {
  data: LookupResponse;
  rawData: LookupResponse;
  query: string;
  range: number;
  excludedAddrs: Set<number>;
  onToggleExcluded: (addrIdx: number) => void;
}

export function DashboardLayout({
  data,
  rawData,
  query,
  range,
  excludedAddrs,
  onToggleExcluded,
}: DashboardLayoutProps) {
  return (
    <div className="space-y-4">
      <MetricsRow data={data} />

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
        <Card>
          <CardHeader>
            <CardTitle className="text-sm">Salgspriser over tid</CardTitle>
          </CardHeader>
          <CardContent>
            <PriceScatterChart data={data} />
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="text-sm">
              Kvadratmeterpris over tid
            </CardTitle>
          </CardHeader>
          <CardContent>
            <SqMeterLineChart data={data} />
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader className="flex flex-row items-center justify-between">
          <CardTitle className="text-sm">
            Salgsdata
            {excludedAddrs.size > 0 && (
              <span className="text-xs font-normal text-muted-foreground ml-2">
                ({excludedAddrs.size} adresser ekskluderet)
              </span>
            )}
          </CardTitle>
          <CsvDownloadButton query={query} range={range} />
        </CardHeader>
        <CardContent className="overflow-x-auto">
          <SalesTable
            data={rawData}
            excludedAddrs={excludedAddrs}
            onToggleExcluded={onToggleExcluded}
          />
        </CardContent>
      </Card>
    </div>
  );
}
