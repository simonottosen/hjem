import type { LookupResponse } from "./types";

export async function searchLookup(
  query: string,
  range: number,
  filter: number
): Promise<LookupResponse> {
  const resp = await fetch("/api/lookup", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      q: query,
      ranges: [range],
      filter_below_std: filter,
    }),
  });

  const data: LookupResponse = await resp.json();

  if (data.error) {
    throw new Error(data.error);
  }

  return data;
}
