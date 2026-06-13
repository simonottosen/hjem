import {
  startOfYear,
  startOfMonth,
  startOfISOWeek,
  startOfDay,
  addYears,
  addMonths,
  addWeeks,
  addDays,
  format,
} from "date-fns";
import { da } from "date-fns/locale";
import type { Address, Sale, Aggregation } from "./types";

/**
 * Time-series helpers for the zoomable charts.
 *
 * The backend only aggregates kr/m² statistics yearly, but the raw `sales`
 * carry real dates. When the user zooms the shared time-range slider into a
 * narrow window we re-bucket those raw sales at a finer resolution (month →
 * week → day) so the kr/m² line shows real data where sales exist. Gaps
 * between real buckets are bridged by line interpolation in the chart, and the
 * yearly projection anchors are linearly interpolated to the same fine grid.
 */

export type Resolution = "year" | "month" | "week" | "day";

const DAY = 24 * 60 * 60 * 1000;

/**
 * Picks a bucket resolution from the visible span. Thresholds are chosen so
 * the number of buckets across the window stays bounded (a few hundred at most).
 */
export function pickResolution(spanMs: number): Resolution {
  const days = spanMs / DAY;
  if (days > 6 * 365) return "year";
  if (days > 540) return "month"; // ~1.5 years
  if (days > 120) return "week"; // ~4 months
  return "day";
}

/** Start-of-period timestamp for a date at the given resolution. */
export function bucketStart(ms: number, res: Resolution): number {
  const d = new Date(ms);
  switch (res) {
    case "year":
      return startOfYear(d).getTime();
    case "month":
      return startOfMonth(d).getTime();
    case "week":
      return startOfISOWeek(d).getTime();
    case "day":
      return startOfDay(d).getTime();
  }
}

function addPeriod(ms: number, res: Resolution): number {
  const d = new Date(ms);
  switch (res) {
    case "year":
      return addYears(d, 1).getTime();
    case "month":
      return addMonths(d, 1).getTime();
    case "week":
      return addWeeks(d, 1).getTime();
    case "day":
      return addDays(d, 1).getTime();
  }
}

/** Inclusive list of bucket-start timestamps covering [startMs, endMs]. */
export function enumerateBuckets(
  startMs: number,
  endMs: number,
  res: Resolution
): number[] {
  const out: number[] = [];
  let t = bucketStart(startMs, res);
  // Guard against pathological inputs producing huge arrays.
  for (let i = 0; t <= endMs && i < 2000; i++) {
    out.push(t);
    t = addPeriod(t, res);
  }
  return out;
}

/**
 * Aggregates kr/m² statistics from raw sales into buckets at `res`.
 * Returns a map keyed by bucket-start timestamp. Mirrors the backend's
 * per-sqm pricing (sale sqm, falling back to the address building size).
 */
export function bucketSqmStats(
  addresses: Address[],
  sales: Sale[],
  res: Resolution
): Map<number, Aggregation> {
  const groups = new Map<number, number[]>();
  for (const s of sales) {
    const addr = addresses[s.addr_idx];
    const size = s.sq_meters > 0 ? s.sq_meters : addr?.building_size ?? 0;
    if (size === 0) continue;

    const key = bucketStart(new Date(s.when).getTime(), res);
    const arr = groups.get(key);
    const price = s.amount / size;
    if (arr) arr.push(price);
    else groups.set(key, [price]);
  }

  const result = new Map<number, Aggregation>();
  for (const [key, prices] of groups) {
    const n = prices.length;
    const mean = prices.reduce((a, b) => a + b, 0) / n;
    const variance =
      prices.reduce((a, p) => a + (p - mean) * (p - mean), 0) / n;
    result.set(key, {
      mean: Math.round(mean),
      std: Math.round(Math.sqrt(variance)),
      n,
    });
  }
  return result;
}

interface Anchor {
  t: number;
  v: number;
}

/** Sorted (time, value) anchors from a yearly projection map. */
export function projectionAnchors(proj: Record<string, number>): Anchor[] {
  return Object.entries(proj)
    .map(([dateStr, v]) => ({ t: new Date(dateStr).getTime(), v }))
    .sort((a, b) => a.t - b.t);
}

/**
 * Linearly interpolates a value at time `t` from sorted anchors. Returns null
 * before the first anchor (no data to extrapolate backwards) and holds the
 * last anchor value after the final anchor, so a projection line extends flat
 * through the current period rather than truncating at the last yearly point.
 */
export function interpolateAt(anchors: Anchor[], t: number): number | null {
  if (anchors.length === 0) return null;
  if (t < anchors[0].t) return null;
  if (t >= anchors[anchors.length - 1].t) return anchors[anchors.length - 1].v;
  for (let i = 0; i < anchors.length - 1; i++) {
    const a = anchors[i];
    const b = anchors[i + 1];
    if (t >= a.t && t <= b.t) {
      if (b.t === a.t) return a.v;
      const frac = (t - a.t) / (b.t - a.t);
      return Math.round(a.v + frac * (b.v - a.v));
    }
  }
  return anchors[anchors.length - 1].v;
}

/** Axis/tooltip label for a bucket timestamp at the given resolution. */
export function formatBucket(ms: number, res: Resolution): string {
  const d = new Date(ms);
  switch (res) {
    case "year":
      return format(d, "yyyy");
    case "month":
      return format(d, "MMM yy", { locale: da });
    case "week":
    case "day":
      return format(d, "d. MMM yy", { locale: da });
  }
}

/** Short axis-tick label (less verbose than the tooltip label). */
export function formatTick(ms: number, res: Resolution): string {
  const d = new Date(ms);
  switch (res) {
    case "year":
      return format(d, "yyyy");
    case "month":
      return format(d, "MMM yy", { locale: da });
    case "week":
    case "day":
      return format(d, "d/M", { locale: da });
  }
}
