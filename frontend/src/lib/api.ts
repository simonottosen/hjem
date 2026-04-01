import type { LookupResponse, ProgressEvent } from "./types";

// Start a lookup job on the server (returns immediately)
export async function startLookup(
  query: string,
  range: number,
  filter: number
): Promise<void> {
  const resp = await fetch("/api/lookup", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      q: query,
      ranges: [range],
      filter_below_std: filter,
    }),
  });

  if (!resp.ok) {
    const data = await resp.json().catch(() => ({}));
    throw new Error(data.error || `Server error ${resp.status}`);
  }
}

// Poll progress (returns progress + result when done)
export interface ProgressWithResult extends ProgressEvent {
  result?: LookupResponse;
}

export async function fetchProgress(): Promise<ProgressWithResult> {
  const resp = await fetch("/api/progress");
  return resp.json();
}
