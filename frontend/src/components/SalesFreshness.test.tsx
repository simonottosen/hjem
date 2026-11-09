// @vitest-environment jsdom

import { afterEach, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { SalesFreshness } from "./SalesFreshness";

afterEach(cleanup);

it("states when the sales were fetched and why the newest may be missing", () => {
  render(<SalesFreshness fetchedAt="2026-11-02T12:40:12Z" />);
  const note = screen.getByText(/Salg hentet fra Boliga/);
  expect(note.textContent).toContain("2. nov. 2026");
  expect(note.textContent).toContain("tinglyst");
});

it("says nothing when there is no fetch date", () => {
  const { container } = render(<SalesFreshness />);
  expect(container.textContent).toBe("");
});
