// Fetching Boliga from the browser instead of from the server. Boliga is by
// far the heaviest outbound caller in a lookup — one paginated request per
// street, 5-50+ per search — so running it here spreads the load across users'
// own IPs rather than concentrating it on one server address.
//
// This is a dumb relay: it posts Boliga's `results` array back verbatim and
// interprets none of it. Every sale-matching, filtering and formatting
// decision stays in Go, so a client-fetched lookup and a server-fetched one
// cannot produce different valuations.

import type { BoligaTask, BoligaFetchResult, BoligaRelayOutcome } from "./types";

const BOLIGA_URL = "https://api.boliga.dk/api/v2/sold/search/results";

// Measured against api.boliga.dk: twelve back-to-back requests return 429 from
// about the seventh, while the same twelve spaced a second apart all return
// 200. Boliga serves a small burst and then throttles to roughly one request
// per second, so the *rate* is what has to be controlled. Running six at once
// spends the burst allowance immediately and 429s the rest of the lookup —
// measured at 21 of 24 streets handed straight back to the server.
const MIN_REQUEST_GAP_MS = 1000;

// Parallelism only hides round-trip latency here; the pacer above is what
// bounds the request rate. Two is enough to keep a request in flight while the
// previous response is being parsed.
const CONCURRENCY = 2;

// Must stay below boligaClientWait in api.go, which is how long the server
// waits before fetching the streets itself. Overrunning it is the worst
// outcome available: every street then gets fetched twice and several
// megabytes are uploaded for a lookup that has already moved on. Stopping
// early costs only the streets we did not reach, which the server was going to
// fetch anyway.
//
// At one request per second this covers roughly forty streets. Denser searches
// overrun it by design and leave the remainder to the server.
const BUDGET_MS = 45_000;

// Boliga rate-limits hard enough that the server's own transport
// (RetryRoundTripper in http.go) logs dozens of 429s per lookup and survives
// only by backing off 2s..32s. Without a ladder of its own the browser hands
// back almost every street the moment a user's IP is throttled — which the
// issue lists as an expected consequence of moving the traffic here. Shorter
// rungs than the server's, because the whole run is bounded by BUDGET_MS.
const RETRY_BACKOFF_MS = [1000, 2000, 4000];

// Thrown when fetch() itself rejects rather than returning a bad status: the
// browser never reached Boliga at all. An ad blocker, an extension, being
// offline, or Boliga withdrawing its permissive CORS all look identical here,
// and all of them will fail the next 50 tasks exactly as they failed this one.
// It is a distinct type so the caller can give up on the whole list instead.
//
// An abort arrives this way too, so callers must check the signal before
// concluding anything about Boliga.
class SystemicFailure extends Error {
  constructor(cause: unknown) {
    super(`Boliga unreachable from this browser: ${String(cause)}`);
    this.name = "SystemicFailure";
  }
}

// Mirrors BoligaPropertyRequest.Fetch in boliga.go, including which parameters
// are omitted when zero. TestServerBoligaQueryShape pins the Go side against
// this; changing one without the other makes client and server fetches return
// different sales for the same street.
function buildUrl(task: BoligaTask, page: number): string {
  const q = new URLSearchParams({ searchTab: "1", sort: "date-a" });

  if (task.zipcode > 0) {
    q.set("zipcodeFrom", String(task.zipcode));
    q.set("zipcodeTo", String(task.zipcode));
  }
  if (task.street !== "") {
    q.set("street", task.street);
  }
  if (task.municipality !== 0) {
    q.set("municipality", String(task.municipality));
  }
  q.set("page", String(page));

  return `${BOLIGA_URL}?${q}`;
}

// A pacer hands out request slots no closer together than MIN_REQUEST_GAP_MS,
// across every worker. One per run rather than a module-level clock, so an
// abandoned search cannot delay the one replacing it.
type Pacer = (signal: AbortSignal) => Promise<void>;

function newPacer(): Pacer {
  let nextSlot = 0;
  return async (signal) => {
    const now = Date.now();
    const slot = Math.max(now, nextSlot);
    nextSlot = slot + MIN_REQUEST_GAP_MS;
    if (slot > now) {
      await sleep(slot - now, signal);
    }
  };
}

// Resolves early when the run is abandoned, so a worker parked between slots
// cannot hold the whole relay past its deadline.
function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms);
    signal.addEventListener(
      "abort",
      () => {
        clearTimeout(timer);
        resolve();
      },
      { once: true }
    );
  });
}

// requestPage mirrors the server's retry policy: back off on 429 and 5xx,
// give up immediately on anything else. A 403 is a block rather than a queue,
// and waiting will not talk it round.
async function requestPage(
  task: BoligaTask,
  page: number,
  pace: Pacer,
  signal: AbortSignal
): Promise<Response> {
  for (let attempt = 0; ; attempt++) {
    await pace(signal);
    if (signal.aborted) {
      throw new Error(`${task.street} ${task.zipcode}: abandoned`);
    }

    let resp: Response;
    try {
      // No credentials, deliberately: Boliga answers with
      // access-control-allow-credentials, so the default would hand a third
      // party the user's cookies for a request that needs none.
      resp = await fetch(buildUrl(task, page), { credentials: "omit", signal });
    } catch (err) {
      throw new SystemicFailure(err);
    }

    if (resp.ok) {
      return resp;
    }

    const retryable = resp.status === 429 || resp.status >= 500;
    if (!retryable || attempt >= RETRY_BACKOFF_MS.length) {
      throw new Error(`${task.street} ${task.zipcode}: status ${resp.status}`);
    }
    await sleep(RETRY_BACKOFF_MS[attempt], signal);
  }
}

async function fetchStreet(
  task: BoligaTask,
  pace: Pacer,
  signal: AbortSignal
): Promise<unknown[]> {
  const sales: unknown[] = [];

  let page = 1;
  for (;;) {
    const body = await (await requestPage(task, page, pace, signal)).json();
    if (Array.isArray(body?.results)) {
      // Appended one at a time rather than spread: a spread passes every
      // element as a separate argument and blows the engine's argument limit
      // on a large enough page.
      for (const sale of body.results) {
        sales.push(sale);
      }
    }

    if (page >= (body?.meta?.totalPages ?? 0)) {
      return sales;
    }
    page += 1;
  }
}

// runBoligaTasks fetches every street it can within the budget and leaves the
// rest to the server. It never throws and never hangs: a lookup must still
// complete when the browser can contribute nothing.
export async function runBoligaTasks(
  tasks: BoligaTask[],
  cancel?: AbortSignal
): Promise<BoligaRelayOutcome> {
  if (tasks.length === 0) {
    return { fetched: [], failed: [] };
  }

  const ctrl = new AbortController();
  const { signal } = ctrl;
  const pace = newPacer();
  // One deadline across every request, not per request: a stalled socket and a
  // list too long to finish both end with the server covering the remainder,
  // and both must leave the streets we did get in `fetched` rather than
  // discarding the whole batch.
  const deadline = setTimeout(() => ctrl.abort(), BUDGET_MS);
  cancel?.addEventListener("abort", () => ctrl.abort(), { once: true });

  const fetched: BoligaFetchResult[] = [];
  const failed: BoligaTask[] = [];

  try {
    // Probe with one task before committing to the rest. A systemic failure
    // fails all of them identically, so discovering it on task 1 and handing
    // the whole list back beats making the user's browser attempt 50 doomed
    // requests.
    const [probe, ...rest] = tasks;
    try {
      fetched.push({ task: probe, sales: await fetchStreet(probe, pace, signal) });
    } catch (err) {
      if (signal.aborted) {
        return { fetched: [], failed: [] };
      }
      if (err instanceof SystemicFailure) {
        console.warn("[hjem] Boliga fetch unavailable, leaving it to the server:", err);
        return { fetched: [], failed: tasks };
      }
      console.warn("[hjem] Boliga street failed, server will refetch:", err);
      failed.push(probe);
    }

    let next = 0;
    const worker = async () => {
      while (!signal.aborted) {
        const i = next++;
        if (i >= rest.length) return;

        const task = rest[i];
        try {
          fetched.push({ task, sales: await fetchStreet(task, pace, signal) });
        } catch (err) {
          // Streets left unattempted are reported by saying nothing about
          // them, so an aborted one must not be recorded as refused.
          if (signal.aborted) return;
          // The server only learns how many streets we gave up on, so the
          // reason has to be visible somewhere. A SystemicFailure reaching
          // here is a connection dying mid-run rather than the page being
          // blocked outright, and the message says which.
          console.warn("[hjem] Boliga street failed, server will refetch:", err);
          failed.push(task);
        }
      }
    };

    await Promise.all(
      Array.from({ length: Math.min(CONCURRENCY, rest.length) }, worker)
    );

    return { fetched, failed };
  } finally {
    clearTimeout(deadline);
  }
}
