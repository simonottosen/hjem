import { useState, useMemo } from "react";
import type { LookupResponse } from "@/lib/types";
import { formatDKK, formatDate } from "@/lib/formatting";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { ScrollArea } from "@/components/ui/scroll-area";
import { ArrowUpDown } from "lucide-react";

interface SalesTableProps {
  data: LookupResponse;
}

type SortKey = "address" | "amount" | "sqmPrice" | "date" | "size" | "year" | "rooms";
type SortDir = "asc" | "desc";

export function SalesTable({ data }: SalesTableProps) {
  const [sortKey, setSortKey] = useState<SortKey>("date");
  const [sortDir, setSortDir] = useState<SortDir>("desc");

  const addrs = data.addresses ?? [];
  const sales = data.sales ?? [];

  const sorted = useMemo(() => {
    const items = sales.map((s) => {
      const addr = addrs[s.addr_idx];
      const sqmPrice =
        addr?.building_size > 0
          ? Math.round(s.amount / addr.building_size)
          : null;
      return { ...s, addr, sqmPrice };
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
        case "date":
          cmp =
            new Date(a.when).getTime() - new Date(b.when).getTime();
          break;
        case "size":
          cmp = (a.addr?.building_size ?? 0) - (b.addr?.building_size ?? 0);
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
  }, [sales, addrs, sortKey, sortDir]);

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
    <ScrollArea className="h-[400px]">
      <Table>
        <TableHeader>
          <TableRow>
            <SortHeader label="Adresse" sortId="address" />
            <SortHeader label="Salgspris" sortId="amount" />
            <SortHeader label="kr/m²" sortId="sqmPrice" />
            <SortHeader label="Dato" sortId="date" />
            <SortHeader label="m²" sortId="size" />
            <SortHeader label="Byggeår" sortId="year" />
            <SortHeader label="Vær." sortId="rooms" />
          </TableRow>
        </TableHeader>
        <TableBody>
          {sorted.map((s, i) => (
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
                {s.sqmPrice != null
                  ? s.sqmPrice.toLocaleString("da-DK")
                  : "—"}
              </TableCell>
              <TableCell className="text-xs">
                {formatDate(s.when)}
              </TableCell>
              <TableCell className="text-xs">
                {s.addr?.building_size || "—"}
              </TableCell>
              <TableCell className="text-xs">
                {s.addr?.built_year || "—"}
              </TableCell>
              <TableCell className="text-xs">
                {s.addr?.rooms || "—"}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </ScrollArea>
  );
}
