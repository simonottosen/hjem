// @vitest-environment jsdom

import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import App from "./App";
import type { StoredLookup } from "@/lib/types";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.history.replaceState(null, "", "/");
});

// A link to a lookup that found nothing still says nothing about today: the
// area may have had sales since, so the page has to say how old it is.
it("says when a linked result with no sales was made", async () => {
  const stored: StoredLookup = {
    query: "Flensborggade 40",
    ranges: [250],
    filter: 1,
    created_at: "2026-11-02T12:40:12Z",
    result: {
      primary_idx: 0,
      addresses: [],
      sales: [],
      ranges: null,
      sqmeters: { global: {}, projections: [] },
    },
  };
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({ ok: true, status: 200, json: async () => stored })
  );
  window.history.replaceState(null, "", "/?lookup=abc&q=Flensborggade+40&r=250&f=1");

  render(<App />);

  await screen.findByText(/Ingen sammenlignelige salg fundet/);
  expect(screen.getByText(/Resultat fra/).textContent).toContain("2. nov. 2026");
});
