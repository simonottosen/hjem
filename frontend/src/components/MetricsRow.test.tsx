// @vitest-environment jsdom

import { afterEach, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MetricsRow } from "./MetricsRow";
import type { LookupResponse } from "@/lib/types";

// Explicit because this project does not run vitest with `globals`, so
// testing-library never installs its own afterEach and renders would otherwise
// pile up in one body — every assertion after the first would match twice.
afterEach(cleanup);

function response(fields: Partial<LookupResponse>): LookupResponse {
  return {
    primary_idx: 0,
    addresses: [
      {
        full_txt: "Testvej 1, 2100 København Ø",
        street_name: "Testvej",
        street_number: "1",
        floor: null,
        door: null,
        zipcode: "2100",
        municipality_code: "101",
        lat: 55.7,
        long: 12.58,
        building_size: 100,
        property_size: 0,
        basement_size: 0,
        rooms: 4,
        built_year: 1930,
        monthly_owner_expense_dkk: 0,
        energy_marking: "c",
      },
    ],
    sales: [],
    ranges: null,
    sqmeters: { global: {}, projections: [] },
    ...fields,
  };
}

// Three outcomes, three renderings. A failed fetch used to be indistinguishable
// from a property with no public valuation, because both simply omitted the
// card — so a Dingeo outage looked like an ordinary result.
it("says the public valuation was unavailable rather than omitting it", () => {
  render(<MetricsRow data={response({ valuation_unavailable: true })} />);

  expect(screen.getByText("Ikke tilgængelig")).toBeDefined();
  expect(screen.getByText("Gns. offentlige vurderinger")).toBeDefined();
});

it("omits the public valuation when the property genuinely has none", () => {
  render(<MetricsRow data={response({})} />);

  expect(screen.queryByText("Ikke tilgængelig")).toBeNull();
  expect(screen.queryByText("Gns. offentlige vurderinger")).toBeNull();
});

it("shows the valuation when there is one", () => {
  const data = response({
    valuation: {
      adresseId: "x",
      includedEvals: [{ name: "Skat", link: "a", value: 4_000_000 }],
      outlierEvals: [],
      minVal: 4_000_000,
      maxVal: 4_000_000,
      mean: 4_000_000,
      standdev: 0,
      countEvals: 1,
    },
  });
  render(<MetricsRow data={data} />);

  expect(screen.getByText("Gns. offentlige vurderinger")).toBeDefined();
  expect(screen.queryByText("Ikke tilgængelig")).toBeNull();
});
