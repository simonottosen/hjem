// The pacer and the budget are the two clocks this file is built around, so
// every test here runs on fake timers. A faithful run of the budget test alone
// would sit out the whole forty-five seconds it exists to check, and the two
// abort cases wait for that deadline as well. Same reasoning as
// TestPaceRoundTripperSpacesRequests in http_test.go, which asserts on slot
// arithmetic rather than on wall-clock round trips.

import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { runBoligaTasks } from "./boliga";
import type { BoligaTask } from "./types";

// Restated rather than exported from boliga.ts on purpose. A test importing
// the constants would agree with whatever the file currently says, and both
// numbers are constrained from outside it: the gap has to stay in step with
// hostRequestGap in http.go, and the budget has to stay below boligaClientWait
// in api.go. Changing either should fail here and send the reader to those.
const GAP_MS = 2200;
const BUDGET_MS = 45_000;

function streets(n: number): BoligaTask[] {
  return Array.from({ length: n }, (_, i) => ({
    street: `Gade ${i}`,
    zipcode: 2200,
    municipality: 101,
  }));
}

// A single page, so one street costs exactly one request slot and the slot
// arithmetic below stays about pacing rather than about pagination.
function onePage(): Response {
  return {
    ok: true,
    json: async () => ({ results: [], meta: { totalPages: 1 } }),
  } as unknown as Response;
}

beforeEach(() => {
  vi.useFakeTimers();
  // runBoligaTasks narrates every refused street and every swallowed reporting
  // error through console.warn, which the last case below asserts on and the
  // rest would only have to read past.
  vi.spyOn(console, "warn").mockImplementation(() => {});
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

it("spaces requests by the pacer gap across both workers", async () => {
  const at: number[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => {
      at.push(Date.now());
      return onePage();
    })
  );

  const run = runBoligaTasks(streets(5));
  await vi.advanceTimersByTimeAsync(5 * GAP_MS);
  await run;

  // Five streets across two concurrent workers, so the gaps only come out even
  // if the slot state is shared between them rather than held per worker.
  const gaps = at.slice(1).map((t, i) => t - at[i]);
  expect(gaps).toEqual([GAP_MS, GAP_MS, GAP_MS, GAP_MS]);
});

it("keeps the streets it already fetched when the budget runs out", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => onePage()));

  const tasks = streets(40);
  const run = runBoligaTasks(tasks);
  await vi.advanceTimersByTimeAsync(2 * BUDGET_MS);
  const outcome = await run;

  // One slot at t=0 and one every gap after it, up to the deadline. The list
  // is deliberately longer than that: the point of the budget is that a dense
  // search stops early, and the streets bought with the first forty-five
  // seconds have to survive the stop rather than be discarded with it.
  expect(outcome.fetched).toHaveLength(Math.floor(BUDGET_MS / GAP_MS) + 1);
  // Reaching the deadline is not Boliga refusing anything. Anything the server
  // sees in `failed` it counts as a miss and warns the user about.
  expect(outcome.failed).toEqual([]);
});

it("hands back the whole list when the probe cannot reach Boliga", async () => {
  // fetch() rejecting rather than answering is how an ad blocker, an offline
  // browser and a withdrawn CORS header all look, and all three would fail the
  // remaining streets identically.
  const fetchMock = vi.fn(async () => {
    throw new TypeError("Failed to fetch");
  });
  vi.stubGlobal("fetch", fetchMock);

  const tasks = streets(8);
  const run = runBoligaTasks(tasks);
  await vi.advanceTimersByTimeAsync(2 * BUDGET_MS);
  const outcome = await run;

  expect(outcome.fetched).toEqual([]);
  expect(outcome.failed).toEqual(tasks);
  // The assertion that distinguishes giving up from failing street by street:
  // either way the server ends up with all eight, but only one of them spares
  // the user's browser seven more doomed requests and their retry ladders.
  expect(fetchMock).toHaveBeenCalledTimes(1);
});

it("says nothing at all about streets it never attempted", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => onePage()));

  const tasks = streets(6);
  const reported: Array<[string, boolean]> = [];
  const cancel = new AbortController();
  const run = runBoligaTasks(tasks, cancel.signal, (task, ok) =>
    reported.push([task.street, ok])
  );

  // Far enough for the probe and the first worker's street, not far enough for
  // the second worker's.
  await vi.advanceTimersByTimeAsync(GAP_MS);
  cancel.abort();
  await vi.advanceTimersByTimeAsync(2 * BUDGET_MS);
  const outcome = await run;

  // Three states have to stay apart on the loading map: fetched, refused, and
  // nobody has reached it yet. Each worker is parked on the pacer when the
  // abort lands, so dropping the signal check would paint the street it was
  // holding red for a refusal that never happened.
  expect(reported).toEqual([
    [tasks[0].street, true],
    [tasks[1].street, true],
  ]);
  expect(outcome.failed).toEqual([]);
});

it("survives a reporting callback that throws", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => onePage()));

  const tasks = streets(3);
  const run = runBoligaTasks(tasks, undefined, () => {
    throw new Error("map blew up");
  });
  await vi.advanceTimersByTimeAsync(3 * GAP_MS);
  const outcome = await run;

  // Rejecting here is silent and expensive: the only caller logs the rejection
  // and never posts the ingest, so the server waits out its whole client
  // timeout and then refetches streets the browser is holding.
  expect(outcome.fetched).toHaveLength(3);
  // Once per street, not once in total. The probe is reported from one place
  // and the worker streets from another, and a guard covering only the first
  // would still lose the run on street two.
  expect(console.warn).toHaveBeenCalledTimes(3);
});
