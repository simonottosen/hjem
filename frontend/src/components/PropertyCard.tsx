import { useState } from "react";
import type { LookupResponse, Address, Sale } from "@/lib/types";
import { formatDKK, formatDate } from "@/lib/formatting";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
  DialogDescription,
} from "@/components/ui/dialog";
import { ScrollArea } from "@/components/ui/scroll-area";

interface PropertyCardProps {
  data: LookupResponse;
}

function PropertyDetail({
  addr,
  sales,
  isPrimary,
}: {
  addr: Address;
  sales: Sale[];
  isPrimary: boolean;
}) {
  return (
    <Dialog>
      <DialogTrigger asChild>
        <Card
          className={`py-3 cursor-pointer hover:shadow-md transition-shadow ${
            isPrimary ? "border-chart-1/50 bg-chart-1/5" : ""
          }`}
        >
          <CardHeader className="pb-1 px-4">
            <CardTitle className="text-xs truncate">
              {addr.full_txt}
            </CardTitle>
          </CardHeader>
          <CardContent className="px-4">
            <div className="flex flex-wrap gap-1">
              {addr.building_size > 0 && (
                <Badge variant="outline" className="text-[10px]">
                  {addr.building_size} m²
                </Badge>
              )}
              {addr.rooms > 0 && (
                <Badge variant="outline" className="text-[10px]">
                  {addr.rooms} vær.
                </Badge>
              )}
              <Badge variant="secondary" className="text-[10px]">
                {sales.length} salg
              </Badge>
            </div>
          </CardContent>
        </Card>
      </DialogTrigger>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{addr.full_txt}</DialogTitle>
          <DialogDescription>Ejendomsdetaljer og salgshistorik</DialogDescription>
        </DialogHeader>
        <div className="grid grid-cols-2 gap-2 text-sm">
          {addr.building_size > 0 && (
            <div>
              <span className="text-muted-foreground">Størrelse:</span>{" "}
              {addr.building_size} m²
            </div>
          )}
          {addr.built_year > 0 && (
            <div>
              <span className="text-muted-foreground">Byggeår:</span>{" "}
              {addr.built_year}
            </div>
          )}
          {addr.rooms > 0 && (
            <div>
              <span className="text-muted-foreground">Værelser:</span>{" "}
              {addr.rooms}
            </div>
          )}
          {addr.energy_marking && (
            <div>
              <span className="text-muted-foreground">Energimærke:</span>{" "}
              {addr.energy_marking.toUpperCase()}
            </div>
          )}
          {addr.monthly_owner_expense_dkk > 0 && (
            <div className="col-span-2">
              <span className="text-muted-foreground">Ejerudgift:</span>{" "}
              {formatDKK(addr.monthly_owner_expense_dkk)}/md
            </div>
          )}
        </div>
        {sales.length > 0 && (
          <div className="mt-2">
            <h4 className="text-sm font-semibold mb-2">Salgshistorik</h4>
            <ScrollArea className="h-[200px]">
              <div className="space-y-1">
                {sales
                  .sort(
                    (a, b) =>
                      new Date(b.when).getTime() -
                      new Date(a.when).getTime()
                  )
                  .map((s, i) => (
                    <div
                      key={i}
                      className="flex justify-between text-sm py-1 border-b last:border-0"
                    >
                      <span>{formatDate(s.when)}</span>
                      <span className="font-mono">{formatDKK(s.amount)}</span>
                    </div>
                  ))}
              </div>
            </ScrollArea>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

export function PropertyCard({ data }: PropertyCardProps) {
  const addrs = data.addresses ?? [];
  const sales = data.sales ?? [];

  // Group sales by address index
  const salesByAddr = new Map<number, Sale[]>();
  for (const s of sales) {
    const list = salesByAddr.get(s.addr_idx) ?? [];
    list.push(s);
    salesByAddr.set(s.addr_idx, list);
  }

  // Show primary first, then sort by number of sales descending
  const indices = addrs
    .map((_, i) => i)
    .sort((a, b) => {
      if (a === data.primary_idx) return -1;
      if (b === data.primary_idx) return 1;
      return (salesByAddr.get(b)?.length ?? 0) - (salesByAddr.get(a)?.length ?? 0);
    });

  return (
    <ScrollArea className="h-[400px]">
      <div className="grid grid-cols-1 sm:grid-cols-2 gap-2 pr-3">
        {indices.slice(0, 50).map((idx) => (
          <PropertyDetail
            key={idx}
            addr={addrs[idx]}
            sales={salesByAddr.get(idx) ?? []}
            isPrimary={idx === data.primary_idx}
          />
        ))}
      </div>
    </ScrollArea>
  );
}
