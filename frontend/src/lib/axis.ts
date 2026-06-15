/**
 * Y-axis scaling helpers.
 *
 * Recharts defaults the value axis to `[0, 'auto']`, which wastes vertical
 * space when values cluster well above zero (e.g. kr/m² prices around 40.000).
 * `paddedYAxis` instead derives a domain from the visible data with ~10 %
 * breathing room, snapped to a "nice" rounded step so the axis can start at,
 * say, 35.000 rather than 0 — and returns matching round tick values.
 */

/** Rounds a number to a "nice" 1/2/5 × 10ⁿ value (Heckbert's algorithm). */
function niceNum(x: number, round: boolean): number {
  if (x <= 0) return 1;
  const exp = Math.floor(Math.log10(x));
  const f = x / Math.pow(10, exp);
  let nf: number;
  if (round) {
    if (f < 1.5) nf = 1;
    else if (f < 3) nf = 2;
    else if (f < 7) nf = 5;
    else nf = 10;
  } else {
    if (f <= 1) nf = 1;
    else if (f <= 2) nf = 2;
    else if (f <= 5) nf = 5;
    else nf = 10;
  }
  return nf * Math.pow(10, exp);
}

interface YAxisScale {
  domain: [number, number] | [number, string];
  ticks?: number[];
}

/**
 * Builds a padded, nicely-rounded Y domain + ticks from the data's min/max.
 * Falls back to Recharts' auto behaviour when there's no finite data. Never
 * dips below zero for non-negative data (prices can't be negative).
 */
export function paddedYAxis(min: number, max: number): YAxisScale {
  if (!isFinite(min) || !isFinite(max)) return { domain: [0, "auto"] };

  const span = max > min ? max - min : Math.abs(max) || 1;
  const pad = span * 0.1;
  const step = niceNum((span + 2 * pad) / 5, true);

  let lo = Math.floor((min - pad) / step) * step;
  const hi = Math.ceil((max + pad) / step) * step;
  if (min >= 0 && lo < 0) lo = 0;

  const ticks: number[] = [];
  for (let v = lo; v <= hi + step * 0.5; v += step) ticks.push(Math.round(v));

  return { domain: [lo, hi], ticks };
}
