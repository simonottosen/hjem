import type { Address, Sale, Aggregation, CompsEstimate } from "./types";

// --- Sqm Statistics (port of Go SalesStatistics, simplified) ---

export function computeSqmStats(
  addresses: Address[],
  sales: Sale[],
  primaryIdx: number
): Record<string, Aggregation> {
  // Group by year
  const groups: Record<number, number[]> = {};
  for (const s of sales) {
    const addr = addresses[s.addr_idx];
    const size = s.sq_meters > 0 ? s.sq_meters : addr?.building_size ?? 0;
    if (size === 0) continue;

    const year = new Date(s.when).getFullYear();
    const sqmPrice = s.amount / size;
    if (!groups[year]) groups[year] = [];
    groups[year].push(sqmPrice);
  }

  const result: Record<string, Aggregation> = {};
  for (const [yearStr, prices] of Object.entries(groups)) {
    if (prices.length === 0) continue;
    const agg = aggregation(prices);
    // Use ISO date key matching server format
    const key = `${yearStr}-01-01T00:00:00Z`;
    result[key] = agg;
  }

  return result;
}

function aggregation(prices: number[]): Aggregation {
  const n = prices.length;
  if (n === 0) return { mean: 0, std: 0, n: 0 };

  const sum = prices.reduce((a, b) => a + b, 0);
  const mean = sum / n;
  const variance =
    prices.reduce((a, p) => a + (p - mean) * (p - mean), 0) / n;
  const std = Math.sqrt(variance);

  return {
    mean: Math.round(mean),
    std: Math.round(std),
    n,
  };
}

// --- Projections (port of Go projection logic) ---

export function computeProjections(
  primarySales: Sale[],
  globalStats: Record<string, Aggregation>,
  buildingSize: number
): Array<Record<string, number>> {
  if (buildingSize === 0) return [];

  const projections: Array<Record<string, number>> = [];

  for (const s of primarySales) {
    const sqmPrice = s.amount / buildingSize;
    const saleYear = new Date(s.when).getFullYear();
    const saleYearKey = `${saleYear}-01-01T00:00:00Z`;
    const saleYearMean = globalStats[saleYearKey]?.mean;
    if (!saleYearMean || saleYearMean === 0) continue;

    const factor = sqmPrice / saleYearMean;
    const proj: Record<string, number> = {};

    for (const [dateStr, agg] of Object.entries(globalStats)) {
      if (agg.mean === 0) continue;
      const year = new Date(dateStr).getFullYear();
      if (year === saleYear) {
        proj[dateStr] = Math.round(sqmPrice);
      } else if (year > saleYear) {
        proj[dateStr] = Math.round(agg.mean * factor);
      }
    }

    projections.push(proj);
  }

  return projections;
}

// --- Comps Estimate (port of Go ComputeCompsEstimate) ---

const TIME_LAMBDA = 0.3;
const SIZE_SIGMA = 0.25;
const ROOM_SIGMA = 1.5;
const AGE_SIGMA = 20.0;
const DIST_HALF = 0.2; // km
const MAX_COMPS = 30;
const MIN_COMPS = 3;

function gaussian(x: number, sigma: number): number {
  return Math.exp(-(x * x) / (2 * sigma * sigma));
}

export function haversineKm(
  lat1: number,
  lon1: number,
  lat2: number,
  lon2: number
): number {
  const R = 6371.0;
  const dLat = ((lat2 - lat1) * Math.PI) / 180;
  const dLon = ((lon2 - lon1) * Math.PI) / 180;
  const lat1r = (lat1 * Math.PI) / 180;
  const lat2r = (lat2 * Math.PI) / 180;
  const a =
    Math.sin(dLat / 2) * Math.sin(dLat / 2) +
    Math.cos(lat1r) * Math.cos(lat2r) * Math.sin(dLon / 2) * Math.sin(dLon / 2);
  const c = 2 * Math.atan2(Math.sqrt(a), Math.sqrt(1 - a));
  return R * c;
}

function latestMean(
  globalStats: Record<string, Aggregation>
): number {
  let latest = "";
  let mean = 0;
  for (const [dateStr, agg] of Object.entries(globalStats)) {
    if (dateStr > latest && agg.mean > 0) {
      latest = dateStr;
      mean = agg.mean;
    }
  }
  return mean;
}

function closestYearMean(
  globalStats: Record<string, Aggregation>,
  targetYear: number
): number {
  let bestDist = Infinity;
  let bestMean = 0;
  for (const [dateStr, agg] of Object.entries(globalStats)) {
    const y = new Date(dateStr).getFullYear();
    const d = Math.abs(y - targetYear);
    if (d < bestDist && agg.mean > 0) {
      bestDist = d;
      bestMean = agg.mean;
    }
  }
  return bestMean;
}

export function computeCompsEstimate(
  primary: Address,
  addresses: Address[],
  sales: Sale[],
  globalStats: Record<string, Aggregation>
): CompsEstimate | null {
  if (!primary || primary.building_size === 0) return null;

  const lm = latestMean(globalStats);
  if (lm === 0) return null;

  const now = Date.now();
  const primarySize = primary.building_size;
  const primaryRooms = primary.rooms;
  const primaryBuildYear = primary.built_year;
  const primaryLat = primary.lat;
  const primaryLon = primary.long;

  interface Candidate {
    adjustedSqmPrice: number;
    weight: number;
  }

  const candidates: Candidate[] = [];

  for (const s of sales) {
    if (s.addr_idx === 0) continue; // skip primary

    const addr = addresses[s.addr_idx];
    const size = s.sq_meters > 0 ? s.sq_meters : addr?.building_size ?? 0;
    if (size === 0) continue;

    const sqmPrice = s.amount / size;
    if (sqmPrice <= 0) continue;

    // Market-adjust
    const saleYear = new Date(s.when).getFullYear();
    const saleYearKey = `${saleYear}-01-01T00:00:00Z`;
    let saleYearMean = globalStats[saleYearKey]?.mean ?? 0;
    if (saleYearMean <= 0) {
      saleYearMean = closestYearMean(globalStats, saleYear);
      if (saleYearMean <= 0) continue;
    }
    const adjusted = sqmPrice * (lm / saleYearMean);

    // Weights
    const yearsAgo =
      (now - new Date(s.when).getTime()) / (1000 * 60 * 60 * 24 * 365.25);
    const wTime = Math.exp(-TIME_LAMBDA * yearsAgo);

    const sizeDiffPct = (size - primarySize) / primarySize;
    const wSize = gaussian(sizeDiffPct, SIZE_SIGMA);

    const saleRooms = s.rooms > 0 ? s.rooms : addr?.rooms ?? 0;
    let wRooms = 1.0;
    if (primaryRooms > 0 && saleRooms > 0) {
      wRooms = gaussian(saleRooms - primaryRooms, ROOM_SIGMA);
    }

    const saleBuildYear = s.build_year > 0 ? s.build_year : addr?.built_year ?? 0;
    let wAge = 1.0;
    if (primaryBuildYear > 0 && saleBuildYear > 0) {
      wAge = gaussian(saleBuildYear - primaryBuildYear, AGE_SIGMA);
    }

    const dist = haversineKm(primaryLat, primaryLon, addr?.lat ?? 0, addr?.long ?? 0);
    const wDist = 1.0 / (1.0 + dist / DIST_HALF);

    const weight = wTime * wSize * wRooms * wAge * wDist;
    if (weight < 1e-6) continue;

    candidates.push({ adjustedSqmPrice: adjusted, weight });
  }

  if (candidates.length < MIN_COMPS) return null;

  // Top N by weight
  candidates.sort((a, b) => b.weight - a.weight);
  const top = candidates.slice(0, MAX_COMPS);

  let sumW = 0;
  let sumWP = 0;
  for (const c of top) {
    sumW += c.weight;
    sumWP += c.weight * c.adjustedSqmPrice;
  }
  if (sumW === 0) return null;

  const estSqm = sumWP / sumW;

  let sumWVar = 0;
  for (const c of top) {
    const diff = c.adjustedSqmPrice - estSqm;
    sumWVar += c.weight * diff * diff;
  }
  const wStd = Math.sqrt(sumWVar / sumW);

  const value = Math.round(estSqm * primarySize);
  const low = Math.max(0, Math.round((estSqm - wStd) * primarySize));
  const high = Math.round((estSqm + wStd) * primarySize);

  const nComps = top.length;
  const rangePct = (high - low) / value;

  let confidence: "high" | "medium" | "low" = "low";
  if (nComps >= 15 && sumW > 3.0 && rangePct < 0.3) {
    confidence = "high";
  } else if (nComps >= 8 && sumW > 1.5 && rangePct < 0.5) {
    confidence = "medium";
  }

  return {
    value,
    sqm_price: Math.round(estSqm),
    low,
    high,
    confidence,
    num_comps: nComps,
  };
}
