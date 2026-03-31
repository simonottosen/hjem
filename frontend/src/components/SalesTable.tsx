import { useState, useMemo } from "react";
import type { LookupResponse } from "@/lib/types";
import { formatDKK, formatDate, formatPct } from "@/lib/formatting";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { ArrowUpDown, ArrowUpRight, ArrowDownRight } from "lucide-react";

interface SalesTableProps {
  data: LookupResponse;
}

type SortKey = "address" | "amount" | "sqmPrice" | "vsAvg" | "date" | "size" | "year" | "rooms";
type SortDir = "asc" | "desc";

export function SalesTable({ data }: SalesTableProps) {
  const [sortKey, setSortKey] = useState<SortKey>("date");
  const [sortDir, setSortDir] = useState<SortDir>("desc");

  const addrs = data.addresses ?? [];
  const sales = data.sales ?? [];
  const primary = addrs[data.primary_idx];
  const primarySize = primary?.building_size ?? 0;

  // Build a year → mean sqm price lookup from global aggregations
  const yearMeanMap = useMemo(() => {
    const m: Record<number, number> = {};
    for (const [dateStr, agg] of Object.entries(data.sqmeters.global)) {
      const year = new Date(dateStr).getFullYear();
      m[year] = agg.mean;
    }
    return m;
  }, [data.sqmeters.global]);

  const sorted = useMemo(() => {
    const items = sales.map((s) => {
      const addr = addrs[s.addr_idx];
      const size = s.sq_meters > 0 ? s.sq_meters : (addr?.building_size ?? 0);
      const sqmPrice = size > 0 ? Math.round(s.amount / size) : null;

      // % difference from area average for that year
      const saleYear = new Date(s.when).getFullYear();
      const areaAvg = yearMeanMap[saleYear];
      const vsAvg =
        sqmPrice != null && areaAvg && areaAvg > 0
          ? ((sqmPrice - areaAvg) / areaAvg) * 100
          : null;

      return { ...s, addr, sqmPrice, size, vsAvg };
    });

    items.sort((a, b) => {
      let cmp = 0;
      switch (sortKey) {
        case "address":
          cmp = (a.addr?.full_txt ?? "").localeCompare(
            b.addr?.full_txt ?? "",
            "da"
          );
          break;
        case "amount":
          cmp = a.amount - b.amount;
          break;
        case "sqmPrice":
          cmp = (a.sqmPrice ?? 0) - (b.sqmPrice ?? 0);
          break;
        case "vsAvg":
          cmp = (a.vsAvg ?? 0) - (b.vsAvg ?? 0);
          break;
        case "date":
          cmp =
            new Date(a.when).getTime() - new Date(b.when).getTime();
          break;
        case "size":
          cmp = (a.size ?? 0) - (b.size ?? 0);
          break;
        case "year":
          cmp = (a.addr?.built_year ?? 0) - (b.addr?.built_year ?? 0);
          break;
        case "rooms":
          cmp = (a.addr?.rooms ?? 0) - (b.addr?.rooms ?? 0);
          break;
      }
      return sortDir === "asc" ? cmp : -cmp;
    });

    return items;
  }, [sales, addrs, sortKey, sortDir, yearMeanMap]);

  function toggleSort(key: SortKey) {
    if (sortKey === key) {
      setSortDir((d) => (d === "asc" ? "desc" : "asc"));
    } else {
      setSortKey(key);
      setSortDir("desc");
    }
  }

  function SortHeader({
    label,
    sortId,
  }: {
    label: string;
    sortId: SortKey;
  }) {
    return (
      <TableHead
        className="cursor-pointer select-none hover:bg-muted/50"
        onClick={() => toggleSort(sortId)}
      >
        <span className="inline-flex items-center gap-1">
          {label}
          <ArrowUpDown className="size-3 text-muted-foreground" />
        </span>
      </TableHead>
    );
  }

  return (
    <TooltipProvider>
      <div className="min-w-[800px] max-h-[700px] overflow-y-auto">
        <Table>
          <TableHeader className="sticky top-0 bg-card z-10">
            <TableRow>
              <SortHeader label="Adresse" sortId="address" />
              <SortHeader label="Salgspris" sortId="amount" />
              <SortHeader label="kr/m²" sortId="sqmPrice" />
              <SortHeader label="vs. gns." sortId="vsAvg" />
              <SortHeader label="Dato" sortId="date" />
              <SortHeader label="m²" sortId="size" />
              <SortHeader label="Byggeår" sortId="year" />
              <SortHeader label="Vær." sortId="rooms" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {sorted.map((s, i) => {
              const primaryValue =
                s.sqmPrice != null && primarySize > 0
                  ? s.sqmPrice * primarySize
                  : null;

              return (
                <TableRow
                  key={i}
                  className={
                    s.addr_idx === data.primary_idx
                      ? "bg-chart-1/10 hover:bg-chart-1/20"
                      : ""
                  }
                >
                  <TableCell className="max-w-[200px] truncate text-xs">
                    {s.addr?.full_txt ?? "—"}
                  </TableCell>
                  <TableCell className="font-mono text-xs">
                    {formatDKK(s.amount)}
                  </TableCell>
                  <TableCell className="font-mono text-xs text-muted-foreground">
                    {s.sqmPrice != null ? (
                      primaryValue != null ? (
                        <Tooltip>
                          <TooltipTrigger asChild>
                            <span className="cursor-help underline decoration-dotted underline-offset-2">
                              {s.sqmPrice.toLocaleString("da-DK")}
                            </span>
                          </TooltipTrigger>
                          <TooltipContent side="top" className="max-w-xs">
                            <p className="font-semibold text-xs">
                              {primary?.full_txt}
                            </p>
                            <p className="text-xs">
                              {s.sqmPrice.toLocaleString("da-DK")} kr/m² × {primarySize} m² ={" "}
                              <span className="font-bold">
                                {formatDKK(primaryValue)}
                              </span>
                            </p>
                          </TooltipContent>
                        </Tooltip>
                      ) : (
                        <span>{s.sqmPrice.toLocaleString("da-DK")}</span>
                      )
                    ) : (
                      "—"
                    )}
                  </TableCell>
                  <TableCell className="text-xs">
                    {s.vsAvg != null ? (
                      <span
                        className={`inline-flex items-center gap-0.5 font-medium ${
                          s.vsAvg >= 0 ? "text-emerald-600" : "text-red-500"
                        }`}
                      >
                        {s.vsAvg >= 0 ? (
                          <ArrowUpRight className="size-3" />
                        ) : (
                          <ArrowDownRight className="size-3" />
                        )}
                        {formatPct(s.vsAvg)}
                      </span>
                    ) : (
                      "—"
                    )}
                  </TableCell>
                  <TableCell className="text-xs">
                    {formatDate(s.when)}
                  </TableCell>
                  <TableCell className="text-xs">
                    {s.size || "—"}
                  </TableCell>
                  <TableCell className="text-xs">
                    {s.addr?.built_year || "—"}
                  </TableCell>
                  <TableCell className="text-xs">
                    {s.addr?.rooms || "—"}
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </div>
    </TooltipProvider>
  );
}
