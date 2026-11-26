import { expect, it } from "vitest";
import {
  areaM2,
  checkArea,
  decodePolyline,
  encodePolyline,
  MAX_VERTICES,
  selfIntersects,
  simplify,
  trimLasso,
  type LonLat,
} from "./area";

const LAT = 55.671;
const LON = 12.548;
const M_PER_DEG = 111_320;

// An axis-aligned square of the given side in metres, its south-west corner
// at Flensborggade, as a [lon, lat] ring.
function square(sideM: number): LonLat[] {
  const dLat = sideM / M_PER_DEG;
  const dLon = sideM / (M_PER_DEG * Math.cos((LAT * Math.PI) / 180));
  return [
    [LON, LAT],
    [LON + dLon, LAT],
    [LON + dLon, LAT + dLat],
    [LON, LAT + dLat],
  ];
}

it("simplifies a noisy line but keeps its endpoints", () => {
  const noisy: [number, number][] = [];
  for (let x = 0; x <= 100; x++) noisy.push([x, x % 2 === 0 ? 0.3 : -0.3]);

  const out = simplify(noisy, 1);

  expect(out).toEqual([
    [0, 0.3],
    [100, 0.3],
  ]);
});

it("keeps a corner the tolerance cannot explain away", () => {
  const corner: [number, number][] = [
    [0, 0],
    [5, 0.1],
    [10, 0],
    [10, 5],
    [10.1, 10],
  ];

  expect(simplify(corner, 1)).toEqual([
    [0, 0],
    [10, 0],
    [10.1, 10],
  ]);
});

it("cuts a lasso that runs on past its start down to the loop", () => {
  // Set down left of the loop, round it, and on past where it began.
  const stroke: [number, number][] = [
    [0, 10],
    [100, 10],
    [100, 100],
    [10, 100],
    [10, -10],
  ];
  expect(selfIntersects(stroke)).toBe(true);

  const loop = trimLasso(stroke);

  expect(loop).toEqual([
    [10, 10],
    [100, 10],
    [100, 100],
    [10, 100],
  ]);
  expect(selfIntersects(loop)).toBe(false);
});

it("leaves a lasso that does not cross itself as drawn", () => {
  const stroke: [number, number][] = [
    [0, 0],
    [100, 0],
    [100, 100],
    [0, 100],
  ];
  expect(trimLasso(stroke)).toEqual(stroke);
});

it("does not halve a figure eight", () => {
  // Its one crossing, at (100, 50), is three tenths of the way round: too far
  // from either end to be an overshoot, so it stays a figure eight and is
  // refused as one.
  const stroke: [number, number][] = [
    [0, 40],
    [0, 100],
    [200, 0],
    [200, 100],
    [20, 10],
  ];
  expect(selfIntersects(trimLasso(stroke))).toBe(true);
});

it("tells a bow-tie from a square", () => {
  const [sw, se, ne, nw] = square(100);
  expect(selfIntersects([sw, se, ne, nw])).toBe(false);
  expect(selfIntersects([sw, ne, se, nw])).toBe(true);
  // A ring handed over closed is the same ring.
  expect(selfIntersects([sw, se, ne, nw, sw])).toBe(false);
});

it("measures a 100 m square near Copenhagen as about a hectare", () => {
  expect(areaM2(square(100))).toBeGreaterThan(9_900);
  expect(areaM2(square(100))).toBeLessThan(10_100);
  // Winding direction does not matter.
  expect(areaM2(square(100).reverse())).toBeGreaterThan(9_900);
});

it("decodes Google's reference polyline", () => {
  const ring = decodePolyline("_p~iF~ps|U_ulLnnqC_mqNvxq`@");
  expect(ring).not.toBeNull();
  const expected = [
    [-120.2, 38.5],
    [-120.95, 40.7],
    [-126.453, 43.252],
  ];
  expect(ring).toHaveLength(3);
  ring!.forEach(([lon, lat], i) => {
    expect(lon).toBeCloseTo(expected[i][0], 6);
    expect(lat).toBeCloseTo(expected[i][1], 6);
  });
  expect(encodePolyline(expected as LonLat[])).toBe("_p~iF~ps|U_ulLnnqC_mqNvxq`@");
});

it("round-trips a ring through a polyline", () => {
  const ring: LonLat[] = [
    [12.54801, 55.67102],
    [12.55493, 55.67011],
    [12.55312, 55.67533],
  ];
  expect(decodePolyline(encodePolyline(ring))).toEqual(ring);
});

it("refuses a polyline that is not one", () => {
  // Truncated mid-value, a character outside the alphabet, and an odd number
  // of values (a latitude with no longitude).
  expect(decodePolyline("_p~iF~ps|U_")).toBeNull();
  expect(decodePolyline("_p~iF ~ps|U")).toBeNull();
  expect(decodePolyline("_p~iF")).toBeNull();
});

it("accepts an ordinary area", () => {
  expect(checkArea(square(500))).toBeNull();
});

it("explains what is wrong with an area", () => {
  const [sw, se, ne, nw] = square(500);
  expect(checkArea([sw, ne, se, nw])).toBe("Området krydser sig selv — tegn igen");
  expect(checkArea(square(5))).toBe("Området er for lille");
  expect(checkArea([sw, se])).toBe("Området er for lille");
  // 2 km × 2 km is 4 km², over the π km² a one-kilometre radius covers.
  expect(checkArea(square(2000))).toBe("Området er for stort (4,0 km² af højst 3,1 km²)");
  // Just over the cap must not read as "3,1 of at most 3,1".
  expect(checkArea(square(1775))).toBe("Området er for stort (3,2 km² af højst 3,1 km²)");
  // Nor may one just under it, which the server, measuring in UTM, can find
  // just over.
  expect(checkArea(square(1770))).toBe("Området er for stort (3,2 km² af højst 3,1 km²)");

  const many: LonLat[] = [];
  for (let i = 0; i <= MAX_VERTICES; i++) {
    const t = (2 * Math.PI * i) / (MAX_VERTICES + 1);
    many.push([LON + 0.005 * Math.cos(t), LAT + 0.003 * Math.sin(t)]);
  }
  expect(checkArea(many)).toBe("Området har for mange punkter — tegn igen");

  expect(checkArea([[2.35, 48.85], [2.36, 48.85], [2.36, 48.86]])).toBe(
    "Området ligger uden for Danmark"
  );
});
