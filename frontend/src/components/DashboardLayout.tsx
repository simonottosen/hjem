import type { LookupResponse } from "@/lib/types";
import { MetricsRow } from "./MetricsRow";
import { PriceScatterChart } from "./PriceScatterChart";
import { SqMeterLineChart } from "./SqMeterLineChart";
import { SalesTable } from "./SalesTable";
import { PropertyCard } from "./PropertyCard";
import { CsvDownloadButton } from "./CsvDownloadButton";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";

interface DashboardLayoutProps {
  data: LookupResponse;
  query: string;
  range: number;
}

export function DashboardLayout({
  data,
  query,
  range,
}: DashboardLayoutProps) {
  return (
    <div className="space-y-4">
      <MetricsRow data={data} />

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
        {/* Left column: Charts */}
        <div className="space-y-4">
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

        {/* Right column: Data exploration */}
        <div className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle className="text-sm">Salgsdata</CardTitle>
            </CardHeader>
            <CardContent>
              <SalesTable data={data} />
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="text-sm">Ejendomme</CardTitle>
            </CardHeader>
            <CardContent>
              <PropertyCard data={data} />
            </CardContent>
          </Card>
        </div>
      </div>

      <div className="flex justify-end">
        <CsvDownloadButton query={query} range={range} />
      </div>
    </div>
  );
}
