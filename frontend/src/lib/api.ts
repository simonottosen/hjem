import type { ProgressEvent } from "./types";

// Thrown when the server no longer recognises a lookup id: it was replaced by
// a newer search or it aged out. Polling cannot recover from this, so it is a
// distinct type — the caller has to stop rather than retry.
export class SessionGoneError extends Error {
  constructor() {
    super("Søgningen er ikke længere aktiv. Prøv at søge igen.");
    this.name = "SessionGoneError";
  }
}

// Start a lookup job on the server (returns immediately) and return its id.
// previousLookupId, when we have one, tells the server to cancel our own
// earlier lookup — and only ours.
export async function startLookup(
  query: string,
  range: number,
  filter: number,
  previousLookupId: string | null
): Promise<string> {
  const resp = await fetch("/api/lookup", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      q: query,
      ranges: [range],
      filter_below_std: filter,
      previous_lookup_id: previousLookupId ?? "",
    }),
  });

  if (!resp.ok) {
    const data = await resp.json().catch(() => ({}));
    console.error("[hjem] Lookup request failed:", { status: resp.status, error: data.error });
    throw new Error(data.error || `Server error ${resp.status}`);
  }

  const { lookup_id: lookupId } = await resp.json();
  if (!lookupId) {
    throw new Error("Serveren returnerede ikke et lookup-id");
  }
  return lookupId as string;
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
