import { useMemo } from "react";
import type { LookupResponse } from "@/lib/types";
import { formatDKK, formatDate, formatPct } from "@/lib/formatting";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Home, TrendingUp, Landmark, BarChart3, ArrowUpRight, ArrowDownRight } from "lucide-react";

interface MetricsRowProps {
  data: LookupResponse;
}

function PctBadge({ pct }: { pct: number }) {
  const positive = pct >= 0;
  return (
    <span
      className={`inline-flex items-center gap-0.5 text-xs font-medium ${
        positive ? "text-emerald-600" : "text-red-500"
      }`}
    >
      {positive ? <ArrowUpRight className="size-3" /> : <ArrowDownRight className="size-3" />}
      {formatPct(pct)}
    </span>
  );
}

export function MetricsRow({ data }: MetricsRowProps) {
  const addrs = data.addresses ?? [];
  const sales = data.sales ?? [];
  const primary = addrs[data.primary_idx];
  const valuation = data.valuation;
  const comps = data.comps_estimate;

  // Primary address sales (sorted newest first)
  const primarySales = useMemo(() => {
    return sales
      .filter((s) => s.addr_idx === data.primary_idx)
      .sort((a, b) => new Date(b.when).getTime() - new Date(a.when).getTime());
  }, [sales, data.primary_idx]);

  const lastSale = primarySales[0];
  const prevSale = primarySales[1];

  // % change between last two sales of primary address
  const salePctChange = useMemo(() => {
    if (!lastSale || !prevSale || prevSale.amount === 0) return null;
    return ((lastSale.amount - prevSale.amount) / prevSale.amount) * 100;
  }, [lastSale, prevSale]);

  // Latest year aggregation
  const globalEntries = Object.entries(data.sqmeters.global);
  const sorted = globalEntries.sort(
    ([a], [b]) => new Date(b).getTime() - new Date(a).getTime()
  );
  const latest = sorted[0];
  const prevYear = sorted[1];

  // Year-over-year sqm price change
  const yoyPctChange = useMemo(() => {
    if (!latest || !prevYear || prevYear[1].mean === 0) return null;
    return ((latest[1].mean - prevYear[1].mean) / prevYear[1].mean) * 100;
  }, [latest, prevYear]);

  // Sqm-based projected value
  const sqmProjectedValue = useMemo(() => {
    if (!primary?.building_size) return null;

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

    if (latest) {
      return {
        value: Math.round(latest[1].mean * primary.building_size),
        sqmPrice: latest[1].mean,
      };
    }

    return null;
  }, [data.sqmeters, primary, latest]);

  // % change from last sale to estimated value (simple)
  const estimatePctChange = useMemo(() => {
    if (!sqmProjectedValue || !lastSale || lastSale.amount === 0) return null;
    return ((sqmProjectedValue.value - lastSale.amount) / lastSale.amount) * 100;
  }, [sqmProjectedValue, lastSale]);

  // % change from last sale to comps estimate
  const compsPctChange = useMemo(() => {
    if (!comps || !lastSale || lastSale.amount === 0) return null;
    return ((comps.value - lastSale.amount) / lastSale.amount) * 100;
  }, [comps, lastSale]);

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

        {/* Card 2: Last sale price + % change from previous sale */}
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
                <div className="flex items-baseline gap-2">
                  <p className="text-2xl font-bold">{formatDKK(lastSale.amount)}</p>
                  {salePctChange != null && <PctBadge pct={salePctChange} />}
                </div>
                <p className="text-xs text-muted-foreground">
                  {formatDate(lastSale.when)}
                  {primary?.building_size > 0 && (
                    <span>
                      {" "}
                      ({Math.round(lastSale.amount / primary.building_size).toLocaleString("da-DK")} kr/m²)
                    </span>
                  )}
                </p>
                {prevSale && (
                  <p className="text-xs text-muted-foreground mt-0.5">
                    Forrige: {formatDKK(prevSale.amount)} ({formatDate(prevSale.when)})
                  </p>
                )}
              </>
            ) : (
              <p className="text-sm text-muted-foreground">Ingen salg fundet</p>
            )}
          </CardContent>
        </Card>

        {/* Card 3: Estimated value — comps primary, simple secondary */}
        <Card className="py-4">
          <CardHeader className="pb-1 px-4">
            <CardTitle className="text-xs text-muted-foreground flex items-center gap-1">
              <Landmark className="size-3" />
              Estimeret værdi
            </CardTitle>
          </CardHeader>
          <CardContent className="px-4 space-y-2">
            {/* Comps estimate (primary) */}
            {comps ? (
              <div>
                <div className="flex items-baseline gap-2">
                  <p className="text-2xl font-bold">
                    ~{formatDKK(comps.value)}
                  </p>
                  <Badge
                    variant={comps.confidence === "high" ? "default" : comps.confidence === "medium" ? "secondary" : "outline"}
                    className="text-[10px]"
                  >
                    {comps.confidence === "high" ? "Høj" : comps.confidence === "medium" ? "Middel" : "Lav"} tillid
                  </Badge>
                  {compsPctChange != null && <PctBadge pct={compsPctChange} />}
                </div>
                <p className="text-xs text-muted-foreground">
                  {formatDKK(comps.low)}–{formatDKK(comps.high)} ({comps.num_comps} sammenlignelige)
                </p>
              </div>
            ) : sqmProjectedValue ? (
              <div>
                <div className="flex items-baseline gap-2">
                  <p className="text-2xl font-bold">
                    ~{formatDKK(sqmProjectedValue.value)}
                  </p>
                  {estimatePctChange != null && <PctBadge pct={estimatePctChange} />}
                </div>
                <p className="text-xs text-muted-foreground">
                  {sqmProjectedValue.sqmPrice.toLocaleString("da-DK")} kr/m² × {primary.building_size} m²
                </p>
              </div>
            ) : (
              <p className="text-muted-foreground">—</p>
            )}

            {/* Simple m² estimate (secondary, when comps is primary) */}
            {comps && sqmProjectedValue && (
              <div className="border-t pt-1.5">
                <p className="text-xs text-muted-foreground">
                  Simpel m²: ~{formatDKK(sqmProjectedValue.value)}{" "}
                  ({sqmProjectedValue.sqmPrice.toLocaleString("da-DK")} kr/m²)
                </p>
              </div>
            )}

            {/* Dingeo range */}
            {valuation && valuation.mean > 0 && (
              <div className={comps && sqmProjectedValue ? "" : "border-t pt-1.5"}>
                <p className="text-xs text-muted-foreground">
                  Dingeo: {formatDKK(valuation.minVal)}–{formatDKK(valuation.maxVal)}
                </p>
              </div>
            )}
          </CardContent>
        </Card>

        {/* Card 4: Data overview + YoY sqm change */}
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
            {latest && (
              <div className="flex items-center gap-1.5 mt-1">
                <p className="text-xs text-muted-foreground">
                  m²-pris {new Date(latest[0]).getFullYear()}:
                </p>
                {yoyPctChange != null && <PctBadge pct={yoyPctChange} />}
              </div>
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
