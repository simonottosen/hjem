import { useMemo, useState } from "react";
import {
  ScatterChart,
  Scatter,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  Legend,
  ResponsiveContainer,
} from "recharts";
import type { LookupResponse } from "@/lib/types";
import { formatDKK, formatDate } from "@/lib/formatting";
import { useMediaQuery } from "@/hooks/useMediaQuery";
import { pickResolution, formatTick } from "@/lib/timeseries";
import { paddedYAxis } from "@/lib/axis";

interface PriceScatterChartProps {
  data: LookupResponse;
  /** Visible time window [startMs, endMs] from the shared zoom slider. */
  range: [number, number];
}

const AMBER = "#ffb700";
const BLUE = "#4685e3";

type ChartMode = "total" | "sqm";

export function PriceScatterChart({ data, range }: PriceScatterChartProps) {
  const [mode, setMode] = useState<ChartMode>("total");
  const isMobile = useMediaQuery("(max-width: 640px)");
  const chartHeight = isMobile ? 250 : 350;
  const axisFontSize = isMobile ? 10 : 11;
  const addrs = data.addresses ?? [];
  const sales = data.sales ?? [];

  const { primaryData, nearbyData } = useMemo(() => {
    const primary: Array<Record<string, unknown>> = [];
    const nearby: Array<Record<string, unknown>> = [];

    for (const s of sales) {
      const date = new Date(s.when).getTime();
      if (date < range[0] || date > range[1]) continue;

      const addr = addrs[s.addr_idx];
      const size = s.sq_meters > 0 ? s.sq_meters : (addr?.building_size ?? 0);
      const sqmPrice = size > 0 ? Math.round(s.amount / size) : null;

      const point = {
        date,
        amount: s.amount,
        sqmPrice,
        size,
        addr_idx: s.addr_idx,
      };
      if (s.addr_idx === data.primary_idx) {
        primary.push(point);
      } else {
        nearby.push(point);
      }
    }
    return { primaryData: primary, nearbyData: nearby };
  }, [sales, addrs, data.primary_idx, range]);

  const isSqm = mode === "sqm";
  const valueKey = isSqm ? "sqmPrice" : "amount";

  const yScale = useMemo(() => {
    let min = Infinity;
    let max = -Infinity;
    for (const p of [...primaryData, ...nearbyData]) {
      const v = p[valueKey] as number | null;
      if (v == null) continue;
      if (v < min) min = v;
      if (v > max) max = v;
    }
    return paddedYAxis(min, max);
  }, [primaryData, nearbyData, valueKey]);

  const CustomTooltip = ({ active, payload }: any) => {
    if (!active || !payload?.length) return null;
    const point = payload[0].payload;
    const addr = addrs[point.addr_idx];
    if (!addr) return null;

    return (
      <div className="rounded-lg border bg-background px-3 py-2 text-xs shadow-lg space-y-1">
        <p className="font-semibold">{addr.full_txt}</p>
        <p>Pris: {formatDKK(point.amount)}</p>
        {point.sqmPrice != null && (
          <p>m²-pris: {point.sqmPrice.toLocaleString("da-DK")} kr/m²</p>
        )}
        <p>Dato: {formatDate(new Date(point.date).toISOString())}</p>
        {point.size > 0 && <p>Størrelse: {point.size} m²</p>}
        {addr.built_year > 0 && <p>Byggeår: {addr.built_year}</p>}
        {addr.rooms > 0 && <p>Værelser: {addr.rooms}</p>}
      </div>
    );
  };

  const primaryAddr = addrs[data.primary_idx];

  return (
    <div>
      <div className="flex justify-end mb-2">
        <div className="inline-flex rounded-md bg-muted p-0.5 text-xs">
          <button
            onClick={() => setMode("total")}
            className={`px-4 py-2.5 sm:px-2.5 sm:py-1 rounded-sm transition-colors ${
              mode === "total"
                ? "bg-background text-foreground shadow-sm"
                : "text-muted-foreground hover:text-foreground"
            }`}
          >
            Salgspris
          </button>
          <button
            onClick={() => setMode("sqm")}
            className={`px-4 py-2.5 sm:px-2.5 sm:py-1 rounded-sm transition-colors ${
              mode === "sqm"
                ? "bg-background text-foreground shadow-sm"
                : "text-muted-foreground hover:text-foreground"
            }`}
          >
            kr/m²
          </button>
        </div>
      </div>
      <ResponsiveContainer width="100%" height={chartHeight}>
        <ScatterChart margin={{ top: 10, right: 10, bottom: 20, left: 10 }}>
          <CartesianGrid strokeDasharray="3 3" />
          <XAxis
            type="number"
            dataKey="date"
            domain={range}
            allowDataOverflow
            tickFormatter={(v) => formatTick(v, pickResolution(range[1] - range[0]))}
            name="Dato"
            fontSize={axisFontSize}
          />
          <YAxis
            type="number"
            dataKey={valueKey}
            domain={yScale.domain}
            ticks={yScale.ticks}
            allowDataOverflow
            tickFormatter={(v) =>
              isSqm
                ? (v / 1000).toFixed(0) + "k"
                : (v / 1000000).toFixed(1) + "M"
            }
            name={isSqm ? "kr/m²" : "Salgspris"}
            fontSize={axisFontSize}
            label={
              isMobile
                ? undefined
                : {
                    value: isSqm ? "Pris pr. m² (kr)" : "Salgspris (DKK)",
                    angle: -90,
                    position: "insideLeft",
                    style: { fontSize: 11 },
                  }
            }
          />
          <Tooltip content={<CustomTooltip />} />
          <Legend />
          <Scatter
            name="Lokalområdet"
            data={nearbyData}
            fill={BLUE}
            opacity={0.5}
            r={3}
            isAnimationActive={false}
          />
          <Scatter
            name={primaryAddr?.full_txt ?? "Søgt adresse"}
            data={primaryData}
            fill={AMBER}
            stroke="#000"
            strokeWidth={1}
            r={5}
            shape="diamond"
            isAnimationActive={false}
          />
        </ScatterChart>
      </ResponsiveContainer>
    </div>
  );
}
