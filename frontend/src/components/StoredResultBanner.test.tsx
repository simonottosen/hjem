// @vitest-environment jsdom

import { afterEach, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { StoredResultBanner } from "./StoredResultBanner";

afterEach(cleanup);

it("says when a linked result was made", () => {
  render(<StoredResultBanner createdAt="2026-11-02T12:40:12Z" />);
  expect(screen.getByText(/Resultat fra/).textContent).toContain("2. nov. 2026");
});
