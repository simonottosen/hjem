// @vitest-environment jsdom

import { useState } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { SearchForm, type AreaMode } from "./SearchForm";
import type { LonLat } from "@/lib/area";

afterEach(cleanup);

const AREA: LonLat[] = [
  [12.548, 55.671],
  [12.555, 55.67],
  [12.553, 55.675],
];

function Harness({ initialMode = "radius", initialArea = null }: { initialMode?: AreaMode; initialArea?: LonLat[] | null }) {
  const [mode, setMode] = useState<AreaMode>(initialMode);
  const [area, setArea] = useState<LonLat[] | null>(initialArea);
  return (
    <SearchForm
      query="Flensborggade 40"
      filter="1"
      range="250"
      mode={mode}
      area={area}
      onQueryChange={vi.fn()}
      onFilterChange={vi.fn()}
      onRangeChange={vi.fn()}
      onModeChange={setMode}
      onAreaChange={setArea}
      onSearch={vi.fn()}
      isLoading={false}
    />
  );
}

const searchButton = () => screen.getByRole("button", { name: "Søg" }) as HTMLButtonElement;
const radiusSelect = () => screen.queryByRole("combobox", { name: "Søgeradius" });

it("swaps the radius for a map to draw on, and holds the search until there is an area", () => {
  render(<Harness />);
  expect(radiusSelect()).not.toBeNull();
  expect(searchButton().disabled).toBe(false);

  fireEvent.click(screen.getByRole("button", { name: "Tegn område" }));

  expect(radiusSelect()).toBeNull();
  expect(screen.getByRole("button", { name: "Vis på kort" })).toBeTruthy();
  expect(screen.getByText("Tegn et område på kortet")).toBeTruthy();
  expect(searchButton().disabled).toBe(true);
});

// An area drawn around one address says nothing about another's.
it("drops a drawn area when the address changes", () => {
  render(<Harness initialMode="area" initialArea={AREA} />);

  fireEvent.change(screen.getByPlaceholderText("Adresse..."), { target: { value: "Flensborggade 40 " } });
  expect(screen.getByText(/Område: 0,1\d km²/)).toBeTruthy();

  fireEvent.change(screen.getByPlaceholderText("Adresse..."), { target: { value: "Vestergade 1" } });
  expect(screen.getByText("Tegn et område på kortet")).toBeTruthy();
  expect(searchButton().disabled).toBe(true);
});

it("searches a drawn area once there is one, and gives the radius back on request", () => {
  render(<Harness initialMode="area" initialArea={AREA} />);
  expect(screen.getByText(/Område: 0,1\d km²/)).toBeTruthy();
  expect(searchButton().disabled).toBe(false);

  fireEvent.click(screen.getByRole("button", { name: "Radius" }));

  expect(radiusSelect()).not.toBeNull();
  expect(searchButton().disabled).toBe(false);
});
