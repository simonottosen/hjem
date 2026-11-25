// @vitest-environment jsdom

import { afterEach, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { useStoredResult, writeSearchLink } from "./useStoredResult";
import type { StoredLookup } from "@/lib/types";

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

it("writes a link that reads back as the same search", () => {
  writeSearchLink({ query: "Flensborggade 40, 4. tv", range: "750", filter: "-1" }, "abc");
  const params = new URLSearchParams(window.location.search);
  expect(params.get("lookup")).toBe("abc");
  expect(params.get("q")).toBe("Flensborggade 40, 4. tv");
  expect(params.get("r")).toBe("750");
  expect(params.get("f")).toBe("-1");
});
