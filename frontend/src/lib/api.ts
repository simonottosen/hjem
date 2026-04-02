import type { ProgressEvent } from "./types";

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
    console.error("[hjem] Lookup request failed:", { status: resp.status, error: data.error });
    throw new Error(data.error || `Server error ${resp.status}`);
  }
}

// Poll progress (returns progress + result when done)
export async function fetchProgress(): Promise<ProgressEvent> {
  const resp = await fetch("/api/progress");
  if (!resp.ok) {
    console.warn("[hjem] Progress poll failed:", { status: resp.status });
  }
  const data: ProgressEvent = await resp.json();
  if (data.warnings?.length) {
    console.warn("[hjem] Lookup warnings:", data.warnings);
  }
  return data;
}
