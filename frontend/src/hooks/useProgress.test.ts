// @vitest-environment jsdom

import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { useProgress } from "./useProgress";
import { fetchProgress } from "@/lib/api";
import type { LookupResponse, ProgressEvent } from "@/lib/types";

// Stubbed rather than driven through a fake server, because the race is about
// when a poll *resolves*, not what it resolves to: the test has to hold one
// request open across a second startPolling and settle it afterwards, which is
// exactly the ordering a real transport will not reproduce on demand.
vi.mock("@/lib/api", () => ({
  fetchProgress: vi.fn(),
  postBoligaIngest: vi.fn(),
  SessionGoneError: class SessionGoneError extends Error {},
}));

function deferred<T>() {
  let settle!: (value: T) => void;
  const promise = new Promise<T>((resolve) => {
    settle = resolve;
  });
  return { promise, settle };
}

function event(fields: Partial<ProgressEvent>): ProgressEvent {
  return {
    stage: "boliga_list",
    message: "",
    current: 0,
    total: 0,
    elapsed_ms: 0,
    ...fields,
  };
}

beforeEach(() => {
  // The hook installs a 2s poll interval that nothing ever tears down, so a
  // real clock would keep firing into the next test.
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

it("ignores a poll that resolves after its search was replaced", async () => {
  const stale = deferred<ProgressEvent>();
  const fresh = deferred<ProgressEvent>();
  vi.mocked(fetchProgress)
    .mockReturnValueOnce(stale.promise)
    .mockReturnValueOnce(fresh.promise);

  const { result } = renderHook(() => useProgress());
  const staleResult = vi.fn();
  const freshResult = vi.fn();

  // Clearing the interval is what stop() does about the *next* poll; this one
  // is already in flight and will resolve regardless.
  act(() => result.current.startPolling("old", staleResult, () => {}));
  act(() => result.current.startPolling("new", freshResult, () => {}));

  await act(async () => fresh.settle(event({ message: "new" })));
  expect(result.current.progress?.message).toBe("new");

  // A finished lookup, because that is the stale answer with the most reach:
  // "done" is the one stage that stops the polling and delivers a result.
  await act(async () =>
    stale.settle(event({ stage: "done", message: "old", result: {} as LookupResponse }))
  );

  expect(result.current.progress?.message).toBe("new");
  // Neither callback, and for different reasons. staleResult belongs to a
  // search nobody is watching. freshResult is the dangerous one: startPolling
  // overwrote the ref, so an unguarded poll would hand the live search a
  // result belonging to the abandoned one.
  expect(staleResult).not.toHaveBeenCalled();
  expect(freshResult).not.toHaveBeenCalled();
});
