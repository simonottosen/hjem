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

interface SqMeterLineChartProps {
  data: LookupResponse;
}

const BLUE = "#4685e3";
const AMBER = "#ffb700";
const BAND_COLOR = "#4685e333";

export function SqMeterLineChart({ data }: SqMeterLineChartProps) {
  const { chartData, projectionKeys } = useMemo(() => {
    const globalEntries = Object.entries(data.sqmeters.global).sort(
      ([a], [b]) => new Date(a).getTime() - new Date(b).getTime()
    );

    // Build base data from global aggregations
    const yearMap: Record<
      string,
      Record<string, number | null | [number, number]>
    > = {};

    for (const [dateStr, agg] of globalEntries) {
      const year = new Date(dateStr).getFullYear().toString();
      yearMap[year] = {
        year: Number(year),
        mean: agg.mean,
        std: agg.std,
        n: agg.n,
        band: [agg.mean - agg.std, agg.mean + agg.std] as unknown as number,
      };
    }

    // Add projections
    const projKeys: string[] = [];
    if (data.sqmeters.projections) {
      data.sqmeters.projections.forEach((proj, idx) => {
        const key = `proj_${idx}`;
        projKeys.push(key);
        for (const [dateStr, value] of Object.entries(proj)) {
          const year = new Date(dateStr).getFullYear().toString();
          if (!yearMap[year]) {
            yearMap[year] = { year: Number(year) };
          }
          yearMap[year][key] = value;
        }
      });
    }

    const sorted = Object.values(yearMap).sort(
      (a, b) => (a.year as number) - (b.year as number)
    );

    return { chartData: sorted, projectionKeys: projKeys };
  }, [data.sqmeters]);

  const CustomTooltip = ({ active, payload, label }: any) => {
    if (!active || !payload?.length) return null;

    return (
      <div className="rounded-lg border bg-background px-3 py-2 text-xs shadow-lg space-y-1">
        <p className="font-semibold">{label}</p>
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
    <ResponsiveContainer width="100%" height={350}>
      <ComposedChart
        data={chartData}
        margin={{ top: 10, right: 10, bottom: 20, left: 10 }}
      >
        <CartesianGrid strokeDasharray="3 3" />
        <XAxis dataKey="year" fontSize={11} />
        <YAxis
          tickFormatter={(v) => (v / 1000).toFixed(0) + "k"}
          fontSize={11}
          label={{
            value: "kr/m²",
            angle: -90,
            position: "insideLeft",
            style: { fontSize: 11 },
          }}
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
        />
        <Line
          type="monotone"
          dataKey="mean"
          name="Gennemsnit"
          stroke={BLUE}
          strokeWidth={2}
          dot={{ r: 3 }}
          activeDot={{ r: 5 }}
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
