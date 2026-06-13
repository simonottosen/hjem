import { useMemo } from "react";
import {
  ComposedChart,
  Line,
  Area,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  Legend,
  ResponsiveContainer,
} from "recharts";
import type { LookupResponse } from "@/lib/types";
import { useMediaQuery } from "@/hooks/useMediaQuery";
import {
  pickResolution,
  enumerateBuckets,
  bucketSqmStats,
  projectionAnchors,
  interpolateAt,
  formatBucket,
  formatTick,
} from "@/lib/timeseries";

interface SqMeterLineChartProps {
  data: LookupResponse;
  /** Visible time window [startMs, endMs] from the shared zoom slider. */
  range: [number, number];
}

const BLUE = "#4685e3";
const AMBER = "#ffb700";
const BAND_COLOR = "#4685e333";

export function SqMeterLineChart({ data, range }: SqMeterLineChartProps) {
  const isMobile = useMediaQuery("(max-width: 640px)");
  const chartHeight = isMobile ? 250 : 350;
  const axisFontSize = isMobile ? 10 : 11;

  const res = pickResolution(range[1] - range[0]);

  const { chartData, projectionKeys } = useMemo(() => {
    const addresses = data.addresses ?? [];
    const sales = data.sales ?? [];

    // Real kr/m² buckets at the zoomed resolution (gaps bridged by the line).
    const stats = bucketSqmStats(addresses, sales, res);

    // Yearly projection anchors, interpolated onto the same fine grid.
    const projections = data.sqmeters.projections ?? [];
    const anchors = projections.map(projectionAnchors);
    const projKeys = projections.map((_, idx) => `proj_${idx}`);

    const buckets = enumerateBuckets(range[0], range[1], res);
    const rows = buckets.map((t) => {
      const row: Record<string, number | null | [number, number]> = { t };
      const agg = stats.get(t);
      if (agg) {
        row.mean = agg.mean;
        row.std = agg.std;
        row.n = agg.n;
        row.band = [agg.mean - agg.std, agg.mean + agg.std];
      }
      anchors.forEach((a, idx) => {
        row[projKeys[idx]] = interpolateAt(a, t);
      });
      return row;
    });

    return { chartData: rows, projectionKeys: projKeys };
  }, [data.addresses, data.sales, data.sqmeters.projections, range, res]);

  const CustomTooltip = ({ active, payload, label }: any) => {
    if (!active || !payload?.length) return null;

    return (
      <div className="rounded-lg border bg-background px-3 py-2 text-xs shadow-lg space-y-1">
        <p className="font-semibold">{formatBucket(Number(label), res)}</p>
        {payload.map((entry: any, i: number) => {
          if (entry.dataKey === "band" || entry.value == null) return null;
          if (entry.dataKey === "mean") {
            const point = entry.payload;
            return (
              <div key={i}>
                <p style={{ color: entry.color }}>
                  Gennemsnit: {entry.value?.toLocaleString("da-DK")} kr/m²
                  {point.std ? ` ±${point.std.toLocaleString("da-DK")}` : ""}
                </p>
                {point.n && (
                  <p className="text-muted-foreground">
                    Antal salg: {point.n}
                  </p>
                )}
              </div>
            );
          }
          if (entry.dataKey?.startsWith("proj_")) {
            return (
              <p key={i} style={{ color: entry.color }}>
                Projektion: {entry.value?.toLocaleString("da-DK")} kr/m²
              </p>
            );
          }
          return null;
        })}
      </div>
    );
  };

  return (
    <ResponsiveContainer width="100%" height={chartHeight}>
      <ComposedChart
        data={chartData}
        margin={{ top: 10, right: 10, bottom: 20, left: 10 }}
      >
        <CartesianGrid strokeDasharray="3 3" />
        <XAxis
          dataKey="t"
          type="number"
          domain={range}
          allowDataOverflow
          scale="time"
          tickFormatter={(v) => formatTick(v, res)}
          fontSize={axisFontSize}
        />
        <YAxis
          tickFormatter={(v) => (v / 1000).toFixed(0) + "k"}
          fontSize={axisFontSize}
          label={
            isMobile
              ? undefined
              : {
                  value: "kr/m²",
                  angle: -90,
                  position: "insideLeft",
                  style: { fontSize: 11 },
                }
          }
        />
        <Tooltip content={<CustomTooltip />} />
        <Legend />
        <Area
          type="monotone"
          dataKey="band"
          fill={BAND_COLOR}
          stroke="none"
          legendType="none"
          tooltipType="none"
          connectNulls
        />
        <Line
          type="monotone"
          dataKey="mean"
          name="Gennemsnit"
          stroke={BLUE}
          strokeWidth={2}
          dot={{ r: 3 }}
          activeDot={{ r: 5 }}
          connectNulls
        />
        {projectionKeys.map((key, idx) => (
          <Line
            key={key}
            type="monotone"
            dataKey={key}
            name={`Salg ${idx + 1}`}
            stroke={AMBER}
            strokeWidth={1.5}
            strokeDasharray="5 2"
            dot={false}
            connectNulls
          />
        ))}
      </ComposedChart>
    </ResponsiveContainer>
  );
}
