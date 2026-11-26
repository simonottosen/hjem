import type { ProgressEvent, BoligaRelayOutcome, StoredLookup } from "./types";
import type { LonLat } from "./area";

// Thrown when the server no longer recognises a lookup id: it was replaced by
// a newer search or it aged out. Polling cannot recover from this, so it is a
// distinct type — the caller has to stop rather than retry.
export class SessionGoneError extends Error {
  constructor() {
    super("Søgningen er ikke længere aktiv. Prøv at søge igen.");
    this.name = "SessionGoneError";
  }
}

// What POST /api/lookup hands back. The id is public by necessity — it is in
// every progress URL — so the ingest token is what proves a later upload comes
// from the browser that started the lookup, and the two travel together.
export interface LookupSession {
  lookupId: string;
  ingestToken: string;
}

// Start a lookup job on the server (returns immediately) and return its id.
// previousLookupId, when we have one, tells the server to cancel our own
// earlier lookup — and only ours. A drawn area replaces the radius rather than
// joining it: the server refuses a body carrying both.
export async function startLookup(
  query: string,
  range: number,
  filter: number,
  previousLookupId: string | null,
  area?: LonLat[]
): Promise<LookupSession> {
  const resp = await fetch("/api/lookup", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      q: query,
      ...(area ? { area } : { ranges: [range] }),
      filter_below_std: filter,
      previous_lookup_id: previousLookupId ?? "",
    }),
  });

  if (!resp.ok) {
    const data = await resp.json().catch(() => ({}));
    console.error("[hjem] Lookup request failed:", { status: resp.status, error: data.error });
    throw new Error(data.error || `Server error ${resp.status}`);
  }

  const { lookup_id: lookupId, ingest_token: ingestToken } = await resp.json();
  if (!lookupId) {
    throw new Error("Serveren returnerede ikke et lookup-id");
  }
  return { lookupId, ingestToken } as LookupSession;
}

// Where an address is, for the map a search area is drawn on. Unlike the
// coordinates on Address, these are real latitude and longitude.
export interface LocatedAddress {
  address: string;
  lat: number;
  lon: number;
}

// Throws with the server's own Danish explanation — no match, or more than
// one — which the map shows as a sentence of its own, so a failure to reach
// the server at all is put in Danish here rather than surfacing the browser's
// own words.
export async function locateAddress(query: string): Promise<LocatedAddress> {
  const resp = await fetch(`/api/locate?q=${encodeURIComponent(query)}`).catch(() => {
    throw new Error("Kunne ikke kontakte serveren. Prøv igen.");
  });
  const data = await resp.json().catch(() => ({}));
  if (!resp.ok) {
    const msg: string = data.error || `Server error ${resp.status}`;
    // The server's errors are written to follow a colon, lowercase.
    throw new Error(msg.charAt(0).toUpperCase() + msg.slice(1));
  }
  return data;
}

// Hand the streets we fetched back to the lookup that asked for them. The
// server treats anything missing as failed and fetches it itself, so a failure
// here costs latency but never results — hence no throw.
export async function postBoligaIngest(
  session: LookupSession,
  outcome: BoligaRelayOutcome
): Promise<void> {
  try {
    const resp = await fetch("/api/boliga/ingest", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        lookup_id: session.lookupId,
        ingest_token: session.ingestToken,
        fetched: outcome.fetched,
        failed: outcome.failed,
      }),
    });
    if (!resp.ok) {
      console.warn("[hjem] Boliga ingest rejected:", { status: resp.status });
    }
  } catch (err) {
    console.warn("[hjem] Boliga ingest failed, server will refetch:", err);
  }
}

// Poll progress (returns progress + result when done)
export async function fetchProgress(lookupId: string): Promise<ProgressEvent> {
  const resp = await fetch(`/api/progress?id=${encodeURIComponent(lookupId)}`);
  if (resp.status === 404) {
    throw new SessionGoneError();
  }
  if (!resp.ok) {
    console.warn("[hjem] Progress poll failed:", { status: resp.status });
  }
  const data: ProgressEvent = await resp.json();
  if (data.warnings?.length) {
    console.warn("[hjem] Lookup warnings:", data.warnings);
  }
  return data;
}

// Fetch a finished lookup by the id in its link. Null when the server has
// none under that id — never stored, or older than it keeps them — which the
// caller answers by searching afresh, so it is not an error.
export async function fetchStoredResult(id: string): Promise<StoredLookup | null> {
  const resp = await fetch(`/api/result?id=${encodeURIComponent(id)}`);
  if (resp.status === 404) {
    return null;
  }
  if (!resp.ok) {
    throw new Error(`Server error ${resp.status}`);
  }
  return resp.json();
}

// Tell the server we have walked away from a lookup, so it stops holding one
// of its few concurrent slots for a result nobody will read. A beacon because
// this runs as the page is being closed: an ordinary fetch is cancelled along
// with the page, a beacon is queued by the browser and delivered afterwards.
// It cannot set a JSON content type on a string body, so the server accepts
// any.
export function cancelLookup(lookupId: string): void {
  navigator.sendBeacon("/api/cancel", JSON.stringify({ lookup_id: lookupId }));
}
