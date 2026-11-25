// @vitest-environment jsdom

import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { act, cleanup, renderHook } from "@testing-library/react";
import { useSearch } from "./useSearch";
import type { LookupResponse } from "@/lib/types";

// Driven through the real cancelLookup so the test pins what reaches the
// server — the endpoint and the body — not merely that a helper was called.
let sendBeacon: ReturnType<typeof vi.fn>;

beforeEach(() => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({
      ok: true,
      status: 202,
      json: async () => ({ lookup_id: "abc123", ingest_token: "s3cret" }),
    })
  );
  sendBeacon = vi.fn().mockReturnValue(true);
  Object.defineProperty(navigator, "sendBeacon", { value: sendBeacon, configurable: true });
});

// Unmounted explicitly: without vitest globals, Testing Library does not clean
// up on its own, and a hook left mounted from one test keeps its pagehide
// listener answering the next test's event.
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

async function startSearch() {
  const hook = renderHook(() => useSearch());
  await act(() => hook.result.current.search("Vestergade 1", 250, 0, () => {}));
  return hook;
}

function closeTab() {
  window.dispatchEvent(new Event("pagehide"));
}

it("cancels a running lookup when the tab is closed", async () => {
  await startSearch();

  closeTab();

  expect(sendBeacon).toHaveBeenCalledTimes(1);
  const [url, body] = sendBeacon.mock.calls[0];
  expect(url).toBe("/api/cancel");
  expect(JSON.parse(body)).toEqual({ lookup_id: "abc123" });
});

it("leaves a finished lookup alone", async () => {
  const { result } = await startSearch();

  act(() => result.current.setResult({} as LookupResponse));
  closeTab();

  expect(sendBeacon).not.toHaveBeenCalled();
});

it("leaves a failed lookup alone", async () => {
  const { result } = await startSearch();

  act(() => result.current.setSearchError("Ukendt fejl"));
  closeTab();

  expect(sendBeacon).not.toHaveBeenCalled();
});

it("stops listening once unmounted", async () => {
  const { unmount } = await startSearch();

  unmount();
  closeTab();

  expect(sendBeacon).not.toHaveBeenCalled();
});
