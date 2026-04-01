import { useMemo } from "react";
import type { LookupResponse } from "@/lib/types";
import {
  computeSqmStats,
  computeProjections,
  computeCompsEstimate,
} from "@/lib/compute";

/**
 * Filters a LookupResponse by excluding certain address indices,
 * then recomputes sqm stats, projections, and comps estimate from
 * the filtered sales data. Returns the original data unchanged when
 * no addresses are excluded.
 */
export function useFilteredData(
  data: LookupResponse | null,
  excludedAddrs: Set<number>
): LookupResponse | null {
  return useMemo(() => {
    if (!data || !data.addresses || !data.sales) return data;
    if (excludedAddrs.size === 0) return data;

    const addresses = data.addresses;
    const primaryIdx = data.primary_idx;

    // Filter sales: remove excluded addresses (never exclude primary)
    const filteredSales = data.sales.filter(
      (s) => s.addr_idx === primaryIdx || !excludedAddrs.has(s.addr_idx)
    );

    // Recompute sqm stats from filtered sales
    const global = computeSqmStats(addresses, filteredSales, primaryIdx);

    // Recompute projections from primary sales + new global stats
    const primarySize = addresses[primaryIdx]?.building_size ?? 0;
    const primarySales = filteredSales.filter(
      (s) => s.addr_idx === primaryIdx
    );
    const projections = computeProjections(primarySales, global, primarySize);

    // Recompute comps estimate from filtered sales
    const compsEstimate = computeCompsEstimate(
      addresses[primaryIdx],
      addresses,
      filteredSales,
      global
    );

    return {
      ...data,
      sales: filteredSales,
      sqmeters: { global, projections },
      comps_estimate: compsEstimate,
    };
  }, [data, excludedAddrs]);
}
