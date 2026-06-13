import { useEffect, useMemo, useState } from "react";
import type { LookupResponse } from "@/lib/types";
import { MetricsRow } from "./MetricsRow";
import { PriceScatterChart } from "./PriceScatterChart";
import { SqMeterLineChart } from "./SqMeterLineChart";
import { SalesTable } from "./SalesTable";
import { CsvDownloadButton } from "./CsvDownloadButton";
import { WarningBanner } from "./WarningBanner";
import { TimeRangeSlider } from "./TimeRangeSlider";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";

const DAY = 24 * 60 * 60 * 1000;

/** Full time domain spanned by sales, yearly stats and projections. */
function computeDomain(data: LookupResponse): [number, number] {
  let min = Infinity;
  let max = -Infinity;
  const consider = (ms: number) => {
    if (ms < min) min = ms;
    if (ms > max) max = ms;
  };

  for (const s of data.sales ?? []) consider(new Date(s.when).getTime());
  for (const key of Object.keys(data.sqmeters?.global ?? {})) {
    consider(new Date(key).getTime());
  }
  for (const proj of data.sqmeters?.projections ?? []) {
    for (const key of Object.keys(proj)) consider(new Date(key).getTime());
  }

  if (!isFinite(min) || !isFinite(max)) {
    const now = Date.now();
    return [now - 365 * DAY, now];
  }
  // Pad a degenerate (single-point) domain so handles have room to move.
  if (min === max) return [min - 30 * DAY, max + 30 * DAY];
  return [min, max];
}

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
  const [domainMin, domainMax] = useMemo(() => computeDomain(data), [data]);
  const [timeRange, setTimeRange] = useState<[number, number]>([
    domainMin,
    domainMax,
  ]);

  // Reset the zoom window whenever the underlying time domain changes
  // (new search, or an exclusion that shifts the earliest/latest date).
  useEffect(() => {
    setTimeRange([domainMin, domainMax]);
  }, [domainMin, domainMax]);

  return (
    <div className="space-y-4">
      {data.warnings && data.warnings.length > 0 && (
        <WarningBanner warnings={data.warnings} />
      )}
      <MetricsRow data={data} />

      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-sm">Tidsperiode</CardTitle>
        </CardHeader>
        <CardContent>
          <TimeRangeSlider
            min={domainMin}
            max={domainMax}
            value={timeRange}
            onChange={setTimeRange}
          />
        </CardContent>
      </Card>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
        <Card>
          <CardHeader>
            <CardTitle className="text-sm">Salgspriser over tid</CardTitle>
          </CardHeader>
          <CardContent>
            <PriceScatterChart data={data} range={timeRange} />
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="text-sm">
              Kvadratmeterpris over tid
            </CardTitle>
          </CardHeader>
          <CardContent>
            <SqMeterLineChart data={data} range={timeRange} />
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
