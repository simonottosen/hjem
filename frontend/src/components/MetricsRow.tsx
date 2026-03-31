import { useMemo } from "react";
import type { LookupResponse } from "@/lib/types";
import { formatDKK, formatDate } from "@/lib/formatting";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Home, TrendingUp, Landmark, BarChart3 } from "lucide-react";

interface MetricsRowProps {
  data: LookupResponse;
}

export function MetricsRow({ data }: MetricsRowProps) {
  const addrs = data.addresses ?? [];
  const sales = data.sales ?? [];
  const primary = addrs[data.primary_idx];
  const valuation = data.valuation;

  // Primary address sales (sorted newest first)
  const primarySales = useMemo(() => {
    return sales
      .filter((s) => s.addr_idx === data.primary_idx)
      .sort((a, b) => new Date(b.when).getTime() - new Date(a.when).getTime());
  }, [sales, data.primary_idx]);

  const lastSale = primarySales[0];

  // Latest year aggregation
  const globalEntries = Object.entries(data.sqmeters.global);
  const sorted = globalEntries.sort(
    ([a], [b]) => new Date(b).getTime() - new Date(a).getTime()
  );
  const latest = sorted[0];

  // Sqm-based projected value: latest projected sqm price × building size
  const sqmProjectedValue = useMemo(() => {
    if (!primary?.building_size) return null;

    // Use the latest projection point if available
    if (data.sqmeters.projections?.length) {
      const lastProj =
        data.sqmeters.projections[data.sqmeters.projections.length - 1];
      if (lastProj) {
        let latestYear = "";
        let latestVal = 0;
        for (const [dateStr, val] of Object.entries(lastProj)) {
          if (dateStr > latestYear) {
            latestYear = dateStr;
            latestVal = val;
          }
        }
        if (latestVal > 0) {
          return {
            value: Math.round(latestVal * primary.building_size),
            sqmPrice: latestVal,
          };
        }
      }
    }

    // Fallback: use latest area average
    if (latest) {
      return {
        value: Math.round(latest[1].mean * primary.building_size),
        sqmPrice: latest[1].mean,
      };
    }

    return null;
  }, [data.sqmeters, primary, latest]);

  return (
    <div className="space-y-3">
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-3">
        {/* Card 1: Address + property info */}
        <Card className="py-4">
          <CardHeader className="pb-1 px-4">
            <CardTitle className="text-xs text-muted-foreground flex items-center gap-1">
              <Home className="size-3" />
              Adresse
            </CardTitle>
          </CardHeader>
          <CardContent className="px-4">
            <p className="text-sm font-semibold truncate">
              {primary?.full_txt ?? "—"}
            </p>
            <div className="flex flex-wrap gap-1 mt-1">
              {primary?.building_size > 0 && (
                <Badge variant="secondary">{primary.building_size} m²</Badge>
              )}
              {primary?.rooms > 0 && (
                <Badge variant="outline">{primary.rooms} vær.</Badge>
              )}
              {primary?.built_year > 0 && (
                <Badge variant="outline">{primary.built_year}</Badge>
              )}
              {primary?.energy_marking && (
                <Badge variant="outline">
                  {primary.energy_marking.toUpperCase()}
                </Badge>
              )}
            </div>
          </CardContent>
        </Card>

        {/* Card 2: Last sale price for this address */}
        <Card className="py-4">
          <CardHeader className="pb-1 px-4">
            <CardTitle className="text-xs text-muted-foreground flex items-center gap-1">
              <TrendingUp className="size-3" />
              Seneste salg
            </CardTitle>
          </CardHeader>
          <CardContent className="px-4">
            {lastSale ? (
              <>
                <p className="text-2xl font-bold">{formatDKK(lastSale.amount)}</p>
                <p className="text-xs text-muted-foreground">
                  {formatDate(lastSale.when)}
                  {primary?.building_size > 0 && (
                    <span>
                      {" "}
                      ({Math.round(lastSale.amount / primary.building_size).toLocaleString("da-DK")} kr/m²)
                    </span>
                  )}
                </p>
                {primarySales.length > 1 && (
                  <p className="text-xs text-muted-foreground mt-0.5">
                    {primarySales.length} salg i alt
                  </p>
                )}
              </>
            ) : (
              <p className="text-sm text-muted-foreground">Ingen salg fundet</p>
            )}
          </CardContent>
        </Card>

        {/* Card 3: Estimated value — sqm-based projection */}
        <Card className="py-4">
          <CardHeader className="pb-1 px-4">
            <CardTitle className="text-xs text-muted-foreground flex items-center gap-1">
              <Landmark className="size-3" />
              Estimeret værdi
            </CardTitle>
          </CardHeader>
          <CardContent className="px-4 space-y-2">
            {/* Sqm-based projection (matches the chart) */}
            {sqmProjectedValue ? (
              <div>
                <p className="text-2xl font-bold">
                  ~{formatDKK(sqmProjectedValue.value)}
                </p>
                <p className="text-xs text-muted-foreground">
                  {sqmProjectedValue.sqmPrice.toLocaleString("da-DK")} kr/m² × {primary.building_size} m²
                </p>
              </div>
            ) : latest ? (
              <div>
                <p className="text-2xl font-bold">
                  {formatDKK(latest[1].mean)}
                  <span className="text-sm font-normal text-muted-foreground"> /m²</span>
                </p>
              </div>
            ) : (
              <p className="text-muted-foreground">—</p>
            )}

            {/* Dingeo range as secondary info */}
            {valuation && valuation.mean > 0 && (
              <div className="border-t pt-1.5">
                <p className="text-xs text-muted-foreground">
                  Dingeo: {formatDKK(valuation.minVal)}–{formatDKK(valuation.maxVal)}
                </p>
              </div>
            )}
          </CardContent>
        </Card>

        {/* Card 4: Data overview */}
        <Card className="py-4">
          <CardHeader className="pb-1 px-4">
            <CardTitle className="text-xs text-muted-foreground flex items-center gap-1">
              <BarChart3 className="size-3" />
              Datagrundlag
            </CardTitle>
          </CardHeader>
          <CardContent className="px-4">
            <p className="text-2xl font-bold">{sales.length}</p>
            <p className="text-xs text-muted-foreground">
              salg fra {addrs.length} adresser
            </p>
            {sorted.length > 1 && (
              <p className="text-xs text-muted-foreground">
                {new Date(sorted[sorted.length - 1][0]).getFullYear()}–
                {new Date(sorted[0][0]).getFullYear()}
              </p>
            )}
          </CardContent>
        </Card>
      </div>

      {/* Dingeo estimates detail row */}
      {valuation && valuation.includedEvals?.length > 0 && (
        <Card className="py-3">
          <CardContent className="px-4">
            <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-5 gap-x-4 gap-y-2">
              {valuation.includedEvals.map((est) => (
                <div key={est.link}>
                  <p className="text-xs text-muted-foreground truncate">{est.name}</p>
                  <p className="text-sm font-semibold font-mono">{formatDKK(est.value)}</p>
                </div>
              ))}
            </div>
          </CardContent>
        </Card>
      )}
    </div>
  );
}
