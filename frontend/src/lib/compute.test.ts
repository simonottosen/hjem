// The mirror of TestCompsDistanceIsIsotropic in comps_test.go. This file
// recomputes comps in the browser from the same payload the server computed
// them from, so the two have to weight distance the same way or one address
// gets two valuations depending on which one the UI happens to show.

import { expect, it } from "vitest";
import { computeCompsEstimate } from "./compute";
import type { Address, Sale } from "./types";

const ORIGIN_LAT = 55.6761; // Copenhagen
const ORIGIN_LON = 12.5683;
const METRE = 1 / 111195; // degrees of latitude
// A degree of longitude is shorter than a degree of latitude by cos(lat).
const EAST_METRE = METRE / Math.cos((ORIGIN_LAT * Math.PI) / 180);

// Crossed over, because `lat` carries the x the server put there and `long`
// carries the y. Writing them straight through would let the test agree with a
// reader that takes the field names at face value.
function at(lat: number, lon: number): Address {
  return {
    full_txt: "", street_name: "", street_number: "", floor: null, door: null,
    zipcode: "1666", municipality_code: "101",
    lat: lon, long: lat,
    building_size: 100, property_size: 0, basement_size: 0,
    rooms: 3, built_year: 1932,
    monthly_owner_expense_dkk: 0, energy_marking: "",
  };
}

function sale(addr_idx: number, amount: number): Sale {
  return { addr_idx, amount, sq_meters: 100, rooms: 3, build_year: 1932,
    when: "2024-05-01T00:00:00Z" };
}

// Four comps the same distance away, two along each axis and priced
// differently by axis. Distance is the only weight that can separate them, so
// an isotropic one has to return the midpoint. Reading the coordinates in the
// order they are named stretches east-west by 1.77x and lands at 38,167.
it("weights comps by true distance, not by one stretched along a single axis", () => {
  const addresses = [
    at(ORIGIN_LAT, ORIGIN_LON),
    at(ORIGIN_LAT + 250 * METRE, ORIGIN_LON),
    at(ORIGIN_LAT - 250 * METRE, ORIGIN_LON),
    at(ORIGIN_LAT, ORIGIN_LON + 250 * EAST_METRE),
    at(ORIGIN_LAT, ORIGIN_LON - 250 * EAST_METRE),
  ];
  const sales = [
    sale(1, 3_000_000), sale(2, 3_000_000), // 30,000 kr/m², north and south
    sale(3, 5_000_000), sale(4, 5_000_000), // 50,000 kr/m², east and west
  ];
  // One priced year, equal to neither side, so the market adjustment is the
  // identity for every comp and cannot skew the average.
  const globalStats = { "2024-01-01T00:00:00Z": { mean: 40000, std: 0, n: 4 } };

  const est = computeCompsEstimate(addresses[0], addresses, sales, globalStats);

  expect(est).not.toBeNull();
  expect(est!.sqm_price).toBeGreaterThan(39600);
  expect(est!.sqm_price).toBeLessThan(40400);
});
