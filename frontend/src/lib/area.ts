// Geometry for a search area the user draws by hand. Everything here is a
// guide for the page — the server re-checks the ring it is sent and has the
// last word — but one that errs strict, so that a drawing the page accepts is
// one the server accepts too, and the page can explain a refusal before the
// user waits on a round trip for it.
//
// Rings are [lon, lat] in real degrees, open (no closing duplicate) unless
// noted. Note the order: it is GeoJSON's and the API's, and the reverse of the
// transposed coordinates on Address.

export type LonLat = [number, number];
type Point = [number, number];

// The cap is the area of the largest radius search, so drawing never reaches
// further than a circle could.
export const MAX_AREA_M2 = Math.PI * 1000 * 1000;
export const MIN_AREA_M2 = 100;
// The server measures in UTM zone 32, whose scale grows away from 9°E, and on
// the ellipsoid; the sphere below comes out up to about 0.6% smaller across
// Denmark. Without a margin, an area drawn just under the cap would pass here
// and come back refused. Erring the other way only costs a sliver of area.
const PAGE_MAX_AREA_M2 = MAX_AREA_M2 * 0.99;
export const MAX_VERTICES = 200;

// The box the server accepts coordinates in. Bornholm is inside it.
const DK = { minLon: 7.5, maxLon: 15.5, minLat: 54.4, maxLat: 58.0 };

// WGS84's equatorial radius: 111 320 m per degree, as the progress map uses.
const EARTH_RADIUS_M = 6_378_137;
const M_PER_DEG = (EARTH_RADIUS_M * Math.PI) / 180;

// A ring may arrive closed — the API allows it, and a lasso ends where it
// started — and every check here wants it open.
function open(ring: LonLat[]): LonLat[] {
  const n = ring.length;
  if (n > 1 && ring[0][0] === ring[n - 1][0] && ring[0][1] === ring[n - 1][1]) {
    return ring.slice(0, -1);
  }
  return ring;
}

function distToSegment(p: Point, a: Point, b: Point): number {
  const dx = b[0] - a[0];
  const dy = b[1] - a[1];
  const len2 = dx * dx + dy * dy;
  // A segment of no length — a lasso whose ends meet is exactly that, at the
  // top level — measures to its one point.
  const t = len2 === 0 ? 0 : Math.max(0, Math.min(1, ((p[0] - a[0]) * dx + (p[1] - a[1]) * dy) / len2));
  return Math.hypot(p[0] - a[0] - t * dx, p[1] - a[1] - t * dy);
}

// Douglas-Peucker, on screen pixels so the tolerance means the same thing at
// every zoom. Keeps both endpoints. Iterative, since a long stroke is a few
// thousand points and recursion depth is the one thing it could run out of.
export function simplify(points: Point[], tolerancePx: number): Point[] {
  if (points.length <= 2) return points.slice();
  const keep = new Uint8Array(points.length);
  keep[0] = 1;
  keep[points.length - 1] = 1;
  const stack: [number, number][] = [[0, points.length - 1]];
  while (stack.length) {
    const [a, b] = stack.pop()!;
    let maxD = 0;
    let idx = -1;
    for (let i = a + 1; i < b; i++) {
      const d = distToSegment(points[i], points[a], points[b]);
      if (d > maxD) {
        maxD = d;
        idx = i;
      }
    }
    if (idx !== -1 && maxD > tolerancePx) {
      keep[idx] = 1;
      stack.push([a, idx], [idx, b]);
    }
  }
  return points.filter((_, i) => keep[i]);
}

function cross(o: Point, a: Point, b: Point): number {
  return (a[0] - o[0]) * (b[1] - o[1]) - (a[1] - o[1]) * (b[0] - o[0]);
}

function onSegment(p: Point, a: Point, b: Point): boolean {
  return (
    Math.min(a[0], b[0]) <= p[0] && p[0] <= Math.max(a[0], b[0]) &&
    Math.min(a[1], b[1]) <= p[1] && p[1] <= Math.max(a[1], b[1])
  );
}

// Touching counts: a ring that meets itself at a point is no more a single
// area than one that crosses.
function segmentsMeet(a: Point, b: Point, c: Point, d: Point): boolean {
  const d1 = cross(c, d, a);
  const d2 = cross(c, d, b);
  const d3 = cross(a, b, c);
  const d4 = cross(a, b, d);
  if (((d1 > 0 && d2 < 0) || (d1 < 0 && d2 > 0)) && ((d3 > 0 && d4 < 0) || (d3 < 0 && d4 > 0))) {
    return true;
  }
  return (
    (d1 === 0 && onSegment(a, c, d)) ||
    (d2 === 0 && onSegment(b, c, d)) ||
    (d3 === 0 && onSegment(c, a, b)) ||
    (d4 === 0 && onSegment(d, a, b))
  );
}

// Every edge against every edge it does not share a corner with. Quadratic,
// which at 200 vertices is 20 000 cheap tests. Raw degrees are fine here:
// stretching one axis does not change whether two lines cross.
export function selfIntersects(ring: LonLat[]): boolean {
  const r = open(ring);
  const n = r.length;
  for (let i = 0; i < n; i++) {
    for (let j = i + 2; j < n; j++) {
      if (i === 0 && j === n - 1) continue;
      if (segmentsMeet(r[i], r[(i + 1) % n], r[j], r[(j + 1) % n])) return true;
    }
  }
  return false;
}

// Where along each segment ab crosses cd, as fractions [t on ab, u on cd],
// or null if they do not meet. Parallel segments give none: an overlap has no
// single point to cut at.
function crossingAt(a: Point, b: Point, c: Point, d: Point): [number, number] | null {
  const rx = b[0] - a[0];
  const ry = b[1] - a[1];
  const sx = d[0] - c[0];
  const sy = d[1] - c[1];
  const den = rx * sy - ry * sx;
  if (den === 0) return null;
  const qx = c[0] - a[0];
  const qy = c[1] - a[1];
  const t = (qx * sy - qy * sx) / den;
  const u = (qx * ry - qy * rx) / den;
  if (t < 0 || t > 1 || u < 0 || u > 1) return null;
  return [t, u];
}

// How much of a stroke, by length, may be cut off at each end.
const LASSO_TAIL_SHARE = 1 / 4;

// A lasso rarely ends exactly where it began: the finger runs on past the
// start, or sets down a little before the loop proper. Either way the stroke
// crosses itself near its ends, and taken as drawn the ring would be refused
// as self-crossing. This cuts the stroke at that crossing and keeps the loop
// between, the area the user plainly meant. Only a crossing within the first
// and last quarter of the stroke counts, so a figure eight, which crosses
// nearer the middle, is left whole to be refused rather than quietly halved.
// Of those, the cut that keeps the longest loop wins. Screen pixels in, as for
// simplify; a stroke with no such crossing comes back as it was.
export function trimLasso(points: Point[]): Point[] {
  const n = points.length;
  const along = [0];
  for (let k = 1; k < n; k++) {
    along.push(along[k - 1] + Math.hypot(points[k][0] - points[k - 1][0], points[k][1] - points[k - 1][1]));
  }
  const total = along[n - 1] ?? 0;
  const headEnd = total * LASSO_TAIL_SHARE;
  const tailStart = total * (1 - LASSO_TAIL_SHARE);
  let best: { i: number; j: number; at: Point; kept: number } | null = null;
  // Segment k runs from points[k] to points[k + 1]. The loop bounds only
  // prune; whether a crossing is near enough an end is decided on the
  // crossing itself, since one long segment can reach well past a quarter.
  for (let i = 0; i + 1 < n && along[i] <= headEnd; i++) {
    for (let j = n - 2; j >= i + 2 && along[j + 1] >= tailStart; j--) {
      const tu = crossingAt(points[i], points[i + 1], points[j], points[j + 1]);
      if (!tu) continue;
      const posI = along[i] + tu[0] * (along[i + 1] - along[i]);
      const posJ = along[j] + tu[1] * (along[j + 1] - along[j]);
      if (posI > headEnd || posJ < tailStart) continue;
      if (!best || posJ - posI > best.kept) {
        const [a, b] = [points[i], points[i + 1]];
        best = { i, j, at: [a[0] + tu[0] * (b[0] - a[0]), a[1] + tu[0] * (b[1] - a[1])], kept: posJ - posI };
      }
      // Walking j down from the end, the first crossing is this i's longest.
      break;
    }
  }
  if (!best) return points.slice();
  const loop = [best.at, ...points.slice(best.i + 1, best.j + 1)];
  return loop.length >= 3 ? loop : points.slice();
}

// Projected flat around the ring's own centre, which over a few kilometres is
// within a fraction of a percent of the true area (see PAGE_MAX_AREA_M2).
export function areaM2(ring: LonLat[]): number {
  const r = open(ring);
  if (r.length < 3) return 0;
  const lon0 = r.reduce((s, p) => s + p[0], 0) / r.length;
  const lat0 = r.reduce((s, p) => s + p[1], 0) / r.length;
  const kx = M_PER_DEG * Math.cos((lat0 * Math.PI) / 180);
  const xy = r.map(([lon, lat]): Point => [(lon - lon0) * kx, (lat - lat0) * M_PER_DEG]);
  let twice = 0;
  for (let i = 0; i < xy.length; i++) {
    const [x1, y1] = xy[i];
    const [x2, y2] = xy[(i + 1) % xy.length];
    twice += x1 * y2 - x2 * y1;
  }
  return Math.abs(twice) / 2;
}

// The ring as a link and the server will see it: rounded to the polyline's
// five decimals (about a metre), so that what is checked here, what is sent
// and what a link reads back are the same ring. Rounding can make neighbours
// coincide, and the server wants distinct vertices.
export function roundRing(ring: LonLat[]): LonLat[] {
  const out: LonLat[] = [];
  for (const [lon, lat] of ring) {
    const p: LonLat = [Math.round(lon * 1e5) / 1e5, Math.round(lat * 1e5) / 1e5];
    const last = out[out.length - 1];
    if (last && last[0] === p[0] && last[1] === p[1]) continue;
    out.push(p);
  }
  return open(out);
}

export function bbox(ring: LonLat[]): [LonLat, LonLat] {
  let minLon = Infinity;
  let minLat = Infinity;
  let maxLon = -Infinity;
  let maxLat = -Infinity;
  for (const [lon, lat] of ring) {
    minLon = Math.min(minLon, lon);
    maxLon = Math.max(maxLon, lon);
    minLat = Math.min(minLat, lat);
    maxLat = Math.max(maxLat, lat);
  }
  return [
    [minLon, minLat],
    [maxLon, maxLat],
  ];
}

// Google's encoded polyline at precision 5: the shortest way to fit a ring of
// up to 200 points into a link, and one other tools can read. Pairs go
// latitude first, as the format has it.
function encodeValue(v: number): string {
  let n = v < 0 ? ~(v << 1) : v << 1;
  let s = "";
  while (n >= 0x20) {
    s += String.fromCharCode((0x20 | (n & 0x1f)) + 63);
    n >>= 5;
  }
  return s + String.fromCharCode(n + 63);
}

export function encodePolyline(ring: LonLat[]): string {
  let prevLat = 0;
  let prevLon = 0;
  let out = "";
  for (const [lon, lat] of ring) {
    const la = Math.round(lat * 1e5);
    const lo = Math.round(lon * 1e5);
    out += encodeValue(la - prevLat) + encodeValue(lo - prevLon);
    prevLat = la;
    prevLon = lo;
  }
  return out;
}

// Null for anything that is not a well-formed polyline. It arrives in a link,
// which anyone can edit, so malformed input is expected rather than a bug.
export function decodePolyline(str: string): LonLat[] | null {
  const values: number[] = [];
  let i = 0;
  while (i < str.length) {
    let result = 0;
    let shift = 0;
    let b: number;
    do {
      if (i >= str.length) return null;
      b = str.charCodeAt(i++) - 63;
      if (b < 0 || b > 63) return null;
      result |= (b & 0x1f) << shift;
      shift += 5;
      if (shift > 30) return null;
    } while (b >= 0x20);
    values.push(result & 1 ? ~(result >> 1) : result >> 1);
  }
  if (values.length % 2) return null;

  const ring: LonLat[] = [];
  let lat = 0;
  let lon = 0;
  for (let k = 0; k < values.length; k += 2) {
    lat += values[k];
    lon += values[k + 1];
    ring.push([lon / 1e5, lat / 1e5]);
  }
  return ring;
}

function km2(m2: number, decimals: number): string {
  return (m2 / 1e6).toFixed(decimals).replace(".", ",");
}

// For the form's status line. Two decimals under a square kilometre, where one
// would round a typical neighbourhood to nothing.
export function formatKm2(m2: number): string {
  return `${km2(m2, m2 < 1e6 ? 2 : 1)} km²`;
}

// Why the server would refuse this ring, in words for the user — or null if it
// would not. Ordered so the cheapest and most fundamental faults are named
// first: there is no point measuring a ring that crosses itself.
export function checkArea(ring: LonLat[]): string | null {
  const r = open(ring);
  if (r.some(([lon, lat]) => !Number.isFinite(lon) || !Number.isFinite(lat))) {
    return "Området er ugyldigt — tegn igen";
  }
  // Simplification keeps a drawing under this; a hand-edited link need not be.
  if (r.length > MAX_VERTICES) return "Området har for mange punkter — tegn igen";
  if (r.length < 3) return "Området er for lille";
  if (r.some(([lon, lat]) => lon < DK.minLon || lon > DK.maxLon || lat < DK.minLat || lat > DK.maxLat)) {
    return "Området ligger uden for Danmark";
  }
  if (selfIntersects(r)) return "Området krydser sig selv — tegn igen";
  const m2 = areaM2(r);
  if (m2 < MIN_AREA_M2) return "Området er for lille";
  if (m2 > PAGE_MAX_AREA_M2) {
    // At one decimal the cap reads 3,1, and so would an area a sliver over
    // it; shown that way the refusal would make no sense, so an area over the
    // cap is never shown as less than the next tenth up.
    const shown = Math.max(Math.round(m2 / 1e5), Math.floor(MAX_AREA_M2 / 1e5) + 1) * 1e5;
    return `Området er for stort (${km2(shown, 1)} km² af højst ${km2(MAX_AREA_M2, 1)} km²)`;
  }
  return null;
}
