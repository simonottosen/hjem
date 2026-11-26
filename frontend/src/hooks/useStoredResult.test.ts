// @vitest-environment jsdom

import { afterEach, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { readSearchLink, useStoredResult, writeSearchLink } from "./useStoredResult";
import type { StoredLookup } from "@/lib/types";
import { encodePolyline } from "@/lib/area";

afterEach(() => {
  vi.unstubAllGlobals();
  window.history.replaceState(null, "", "/");
});

function openAt(search: string) {
  window.history.replaceState(null, "", `/${search}`);
}

function stubFetch(status: number, body: unknown) {
  const fetch = vi.fn().mockResolvedValue({
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  });
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

const stored: StoredLookup = {
  query: "Flensborggade 40",
  ranges: [500],
  filter: 2,
  created_at: "2026-11-02T12:40:12Z",
  result: {
    primary_idx: 0,
    addresses: [],
    sales: [],
    ranges: null,
    sqmeters: { global: {}, projections: [] },
  },
};

it("hands over a stored result instead of searching again", async () => {
  openAt("?lookup=abc&q=Flensborggade+40&r=500&f=2");
  const fetch = stubFetch(200, stored);
  const onStored = vi.fn();
  const onLink = vi.fn();

  const { result } = renderHook(() => useStoredResult(onStored, onLink));
  expect(result.current).toBe(true);

  await waitFor(() => expect(onStored).toHaveBeenCalledWith(stored));
  expect(result.current).toBe(false);
  expect(onLink).not.toHaveBeenCalled();
  expect(fetch).toHaveBeenCalledWith("/api/result?id=abc");
});

// The server forgets results after thirty days; a link that outlives its
// result should still show the area it was about.
it("searches the linked address when the stored result is gone", async () => {
  openAt("?lookup=abc&q=Flensborggade+40&r=500&f=2");
  stubFetch(404, { error: "ukendt eller udløbet resultat" });
  const onStored = vi.fn();
  const onLink = vi.fn();

  const { result } = renderHook(() => useStoredResult(onStored, onLink));

  await waitFor(() =>
    expect(onLink).toHaveBeenCalledWith({ query: "Flensborggade 40", range: "500", filter: "2" })
  );
  expect(result.current).toBe(false);
  expect(onStored).not.toHaveBeenCalled();
});

it("searches a bare address link straight away, on the form's defaults", () => {
  openAt("?q=Flensborggade%2040");
  const fetch = stubFetch(200, {});
  const onLink = vi.fn();

  const { result } = renderHook(() => useStoredResult(vi.fn(), onLink));

  expect(result.current).toBe(false);
  expect(onLink).toHaveBeenCalledTimes(1);
  expect(onLink).toHaveBeenCalledWith({ query: "Flensborggade 40", range: undefined, filter: undefined });
  expect(fetch).not.toHaveBeenCalled();
});

it("does nothing on a page opened without a link", () => {
  const fetch = stubFetch(200, {});
  const onStored = vi.fn();
  const onLink = vi.fn();

  const { result } = renderHook(() => useStoredResult(onStored, onLink));

  expect(result.current).toBe(false);
  expect(onStored).not.toHaveBeenCalled();
  expect(onLink).not.toHaveBeenCalled();
  expect(fetch).not.toHaveBeenCalled();
});

it("ignores a radius or filter the form does not offer", () => {
  openAt("?q=Flensborggade%2040&r=abc&f=0");
  stubFetch(200, {});
  const onLink = vi.fn();

  renderHook(() => useStoredResult(vi.fn(), onLink));

  expect(onLink).toHaveBeenCalledWith({ query: "Flensborggade 40", range: undefined, filter: undefined });
});

// Flensborggade and the blocks east of it, about 0.1 km².
const AREA: [number, number][] = [
  [12.548, 55.671],
  [12.555, 55.67],
  [12.553, 55.675],
];

it("writes a drawn area into the link in place of the radius", () => {
  writeSearchLink({ query: "Flensborggade 40", range: "250", filter: "1", area: AREA });
  const params = new URLSearchParams(window.location.search);
  expect(params.get("r")).toBeNull();
  expect(params.get("a")).toBe(encodePolyline(AREA));

  expect(readSearchLink(window.location.search).link).toEqual({
    query: "Flensborggade 40",
    range: undefined,
    filter: "1",
    area: AREA,
  });
});

it("searches a linked drawn area", () => {
  openAt(`?q=Flensborggade%2040&a=${encodeURIComponent(encodePolyline(AREA))}&f=2`);
  stubFetch(200, {});
  const onLink = vi.fn();

  renderHook(() => useStoredResult(vi.fn(), onLink));

  expect(onLink).toHaveBeenCalledWith({ query: "Flensborggade 40", range: undefined, filter: "2", area: AREA });
});

// Garbage, and a well-formed polyline the server would refuse (a bow-tie),
// both leave the link a radius search rather than one that cannot run.
it("ignores a linked area that is not a usable one", () => {
  const [a, b, c] = AREA;
  const bowTie: [number, number][] = [a, c, b, [12.556, 55.676]];
  for (const bad of ["not-a-polyline", encodePolyline(bowTie)]) {
    expect(readSearchLink(`?q=Flensborggade%2040&r=500&a=${encodeURIComponent(bad)}`).link).toEqual({
      query: "Flensborggade 40",
      range: "500",
      filter: undefined,
      area: undefined,
    });
  }
});

it("writes a link that reads back as the same search", () => {
  writeSearchLink({ query: "Flensborggade 40, 4. tv", range: "750", filter: "-1" }, "abc");
  const params = new URLSearchParams(window.location.search);
  expect(params.get("lookup")).toBe("abc");
  expect(params.get("q")).toBe("Flensborggade 40, 4. tv");
  expect(params.get("r")).toBe("750");
  expect(params.get("f")).toBe("-1");
  expect(params.get("a")).toBeNull();
});
