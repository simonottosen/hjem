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
import { ArrowUpDown, ArrowUpRight, ArrowDownRight, Info, ChevronDown, ChevronUp } from "lucide-react";

interface SalesTableProps {
  data: LookupResponse;
  excludedAddrs: Set<number>;
  onToggleExcluded: (addrIdx: number) => void;
}

type SortKey = "address" | "amount" | "sqmPrice" | "vsAvg" | "date" | "size" | "year" | "rooms";
type SortDir = "asc" | "desc";

interface SortedSale {
  addr_idx: number;
  amount: number;
  sq_meters: number;
  rooms: number;
  build_year: number;
  when: string;
  addr: NonNullable<LookupResponse["addresses"]>[number] | undefined;
  sqmPrice: number | null;
  size: number;
  vsAvg: number | null;
  excluded: boolean;
}

export function SalesTable({
  data,
  excludedAddrs,
  onToggleExcluded,
}: SalesTableProps) {
  const [sortKey, setSortKey] = useState<SortKey>("date");
  const [sortDir, setSortDir] = useState<SortDir>("desc");
  const [expandedCards, setExpandedCards] = useState<Set<number>>(new Set());

  const addrs = data.addresses ?? [];
  const sales = data.sales ?? [];
  const primary = addrs[data.primary_idx];
  const primarySize = primary?.building_size ?? 0;

  // Build a year -> mean sqm price lookup from global aggregations
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

      const saleYear = new Date(s.when).getFullYear();
      const areaAvg = yearMeanMap[saleYear];
      const vsAvg =
        sqmPrice != null && areaAvg && areaAvg > 0
          ? ((sqmPrice - areaAvg) / areaAvg) * 100
          : null;

      const excluded = excludedAddrs.has(s.addr_idx);

      return { ...s, addr, sqmPrice, size, vsAvg, excluded };
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
  }, [sales, addrs, sortKey, sortDir, yearMeanMap, excludedAddrs]);

  function toggleSort(key: SortKey) {
    if (sortKey === key) {
      setSortDir((d) => (d === "asc" ? "desc" : "asc"));
    } else {
      setSortKey(key);
      setSortDir("desc");
    }
  }

  function toggleCardExpanded(index: number) {
    setExpandedCards((prev) => {
      const next = new Set(prev);
      if (next.has(index)) {
        next.delete(index);
      } else {
        next.add(index);
      }
      return next;
    });
  }

  function SortHeader({
    label,
    sortId,
    info,
  }: {
    label: string;
    sortId: SortKey;
    info?: string;
  }) {
    return (
      <TableHead
        className="cursor-pointer select-none hover:bg-muted/50"
        onClick={() => toggleSort(sortId)}
      >
        <span className="inline-flex items-center gap-1">
          {label}
          {info && (
            <Tooltip>
              <TooltipTrigger asChild>
                <span
                  className="inline-flex items-center justify-center size-3.5 rounded-full bg-muted-foreground/20 text-muted-foreground cursor-help"
                  onClick={(e) => e.stopPropagation()}
                >
                  <Info className="size-2.5" />
                </span>
              </TooltipTrigger>
              <TooltipContent side="top" className="max-w-[220px] text-xs font-normal">
                {info}
              </TooltipContent>
            </Tooltip>
          )}
          <ArrowUpDown className="size-3 text-muted-foreground" />
        </span>
      </TableHead>
    );
  }

  function VsAvgBadge({ vsAvg }: { vsAvg: number | null }) {
    if (vsAvg == null) return <span className="text-muted-foreground">—</span>;
    return (
      <span
        className={`inline-flex items-center gap-0.5 font-medium ${
          vsAvg >= 0 ? "text-emerald-600" : "text-red-500"
        }`}
      >
        {vsAvg >= 0 ? (
          <ArrowUpRight className="size-3" />
        ) : (
          <ArrowDownRight className="size-3" />
        )}
        {formatPct(vsAvg)}
      </span>
    );
  }

  function MobileCard({ s, index }: { s: SortedSale; index: number }) {
    const isPrimary = s.addr_idx === data.primary_idx;
    const expanded = expandedCards.has(index);

    return (
      <div
        className={`rounded-lg border p-3 ${
          isPrimary
            ? "bg-chart-1/10 border-chart-1/30"
            : s.excluded
              ? "opacity-40"
              : "bg-card"
        }`}
      >
        {/* Top row: checkbox + address */}
        <div className="flex items-start gap-3">
          <div className="pt-0.5 shrink-0">
            {!isPrimary ? (
              <input
                type="checkbox"
                checked={!s.excluded}
                onChange={() => onToggleExcluded(s.addr_idx)}
                className="size-5 rounded border-input accent-primary cursor-pointer"
              />
            ) : (
              <div className="size-5" />
            )}
          </div>
          <div className="flex-1 min-w-0">
            <p className={`text-sm font-medium truncate ${s.excluded ? "line-through" : ""}`}>
              {s.addr?.full_txt ?? "—"}
            </p>
            <p className="text-xs text-muted-foreground mt-0.5">
              {formatDate(s.when)}
            </p>
          </div>
        </div>

        {/* Key metrics row */}
        <div className="flex items-center gap-4 mt-2 ml-8">
          <div>
            <p className="text-xs text-muted-foreground">Pris</p>
            <p className="text-sm font-mono font-medium">{formatDKK(s.amount)}</p>
          </div>
          <div>
            <p className="text-xs text-muted-foreground">kr/m²</p>
            <p className="text-sm font-mono">
              {s.sqmPrice != null ? s.sqmPrice.toLocaleString("da-DK") : "—"}
            </p>
          </div>
          <div>
            <p className="text-xs text-muted-foreground">vs. gns.</p>
            <p className="text-sm"><VsAvgBadge vsAvg={s.vsAvg} /></p>
          </div>
        </div>

        {/* Expand/collapse button */}
        <button
          type="button"
          onClick={() => toggleCardExpanded(index)}
          className="flex items-center gap-1 text-xs text-muted-foreground mt-2 ml-8 hover:text-foreground transition-colors"
        >
          {expanded ? <ChevronUp className="size-3" /> : <ChevronDown className="size-3" />}
          {expanded ? "Skjul detaljer" : "Vis detaljer"}
        </button>

        {/* Expanded details */}
        {expanded && (
          <div className="grid grid-cols-3 gap-x-4 gap-y-1 mt-2 ml-8 text-xs">
            <div>
              <span className="text-muted-foreground">m²</span>
              <p>{s.size || "—"}</p>
            </div>
            <div>
              <span className="text-muted-foreground">Byggeår</span>
              <p>{s.addr?.built_year || "—"}</p>
            </div>
            <div>
              <span className="text-muted-foreground">Vær.</span>
              <p>{s.addr?.rooms || "—"}</p>
            </div>
          </div>
        )}
      </div>
    );
  }

  // Mobile sort selector
  function MobileSortSelector() {
    const sortOptions: { key: SortKey; label: string }[] = [
      { key: "date", label: "Dato" },
      { key: "amount", label: "Salgspris" },
      { key: "sqmPrice", label: "kr/m\u00B2" },
      { key: "vsAvg", label: "vs. gns." },
      { key: "address", label: "Adresse" },
      { key: "size", label: "m\u00B2" },
      { key: "year", label: "Bygge\u00E5r" },
      { key: "rooms", label: "V\u00E6r." },
    ];

    return (
      <div className="flex items-center gap-2 mb-3">
        <span className="text-xs text-muted-foreground">Sorter:</span>
        <select
          value={sortKey}
          onChange={(e) => {
            setSortKey(e.target.value as SortKey);
            setSortDir("desc");
          }}
          className="text-xs bg-card border rounded px-2 py-1"
        >
          {sortOptions.map((opt) => (
            <option key={opt.key} value={opt.key}>{opt.label}</option>
          ))}
        </select>
        <button
          type="button"
          onClick={() => setSortDir((d) => (d === "asc" ? "desc" : "asc"))}
          className="text-xs text-muted-foreground hover:text-foreground transition-colors"
        >
          <ArrowUpDown className="size-3.5" />
        </button>
      </div>
    );
  }

  return (
    <TooltipProvider>
      {/* Mobile: card layout */}
      <div className="md:hidden max-h-[70vh] overflow-y-auto">
        <MobileSortSelector />
        <div className="space-y-2">
          {sorted.map((s, i) => (
            <MobileCard key={i} s={s} index={i} />
          ))}
        </div>
      </div>

      {/* Desktop: table layout */}
      <div className="hidden md:block max-h-[660px] overflow-y-auto">
        <Table>
          <TableHeader className="sticky top-0 bg-card z-10">
            <TableRow>
              <TableHead className="w-[32px]" />
              <SortHeader label="Adresse" sortId="address" />
              <SortHeader label="Salgspris" sortId="amount" />
              <SortHeader label="kr/m²" sortId="sqmPrice" info="Salgspris divideret med boligens størrelse i kvadratmeter." />
              <SortHeader label="vs. gns." sortId="vsAvg" info="Procentvis afvigelse fra områdets gennemsnitlige m²-pris det år boligen blev solgt." />
              <SortHeader label="Dato" sortId="date" />
              <SortHeader label="m²" sortId="size" />
              <SortHeader label="Byggeår" sortId="year" />
              <SortHeader label="Vær." sortId="rooms" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {sorted.map((s, i) => {
              const isPrimary = s.addr_idx === data.primary_idx;
              const primaryValue =
                s.sqmPrice != null && primarySize > 0
                  ? s.sqmPrice * primarySize
                  : null;

              return (
                <TableRow
                  key={i}
                  className={
                    isPrimary
                      ? "bg-chart-1/10 hover:bg-chart-1/20"
                      : s.excluded
                        ? "opacity-40"
                        : ""
                  }
                >
                  <TableCell className="w-[32px] px-1">
                    {!isPrimary && (
                      <input
                        type="checkbox"
                        checked={!s.excluded}
                        onChange={() => onToggleExcluded(s.addr_idx)}
                        className="size-3.5 rounded border-input accent-primary cursor-pointer"
                      />
                    )}
                  </TableCell>
                  <TableCell
                    className={`max-w-[200px] truncate text-xs ${
                      s.excluded ? "line-through" : ""
                    }`}
                  >
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
                    <VsAvgBadge vsAvg={s.vsAvg} />
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
