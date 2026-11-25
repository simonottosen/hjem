import { useEffect, useRef, useState } from "react";
import type { StoredLookup } from "@/lib/types";
import { fetchStoredResult } from "@/lib/api";

// A search as a link carries it. Radius and filter are optional: a link
// typed or pasted by hand with only an address still works, on the form's
// defaults.
export interface SearchLink {
  query: string;
  range?: string;
  filter?: string;
}

// The options SearchForm offers. A link is text anyone can edit, and a value
// outside these would leave its select blank and send the server a radius or
// filter nobody could have picked, so anything else counts as absent.
const RANGES = ["250", "500", "750", "1000"];
const FILTERS = ["-1", "1", "2", "3"];

function option(value: string | null, allowed: string[]): string | undefined {
  return value !== null && allowed.includes(value) ? value : undefined;
}

export function readSearchLink(search: string): {
  lookupId: string | null;
  link: SearchLink | null;
} {
  const params = new URLSearchParams(search);
  const query = params.get("q")?.trim();
  return {
    lookupId: params.get("lookup") || null,
    link: query
      ? {
          query,
          range: option(params.get("r"), RANGES),
          filter: option(params.get("f"), FILTERS),
        }
      : null,
  };
}

// Replaced, not pushed: each search would otherwise leave a history entry
// that Back returns to without the page changing, since nothing here listens
// for navigation.
export function writeSearchLink(link: Required<SearchLink>, resultId?: string) {
  const params = new URLSearchParams();
  if (resultId) params.set("lookup", resultId);
  params.set("q", link.query);
  params.set("r", link.range);
  params.set("f", link.filter);
  window.history.replaceState(null, "", `?${params}`);
}

// Acts on the link the page was opened with, once. A stored result is handed
// to onStored; failing that — no id, or one the server no longer has — the
// address in the link is handed to onLink to search afresh, since whoever sent
// the link wanted that area looked at either way.
//
// Returns true while a stored result is being fetched, so the page can hold
// off a search of the user's own that the arriving result would overwrite.
export function useStoredResult(
  onStored: (stored: StoredLookup) => void,
  onLink: (link: SearchLink) => void
): boolean {
  const [pending, setPending] = useState(
    () => readSearchLink(window.location.search).lookupId !== null
  );

  // The handlers close over App state, so the latest ones are read at the
  // moment of calling rather than those from the first render.
  const handlers = useRef({ onStored, onLink });
  useEffect(() => {
    handlers.current = { onStored, onLink };
  });

  // A ref rather than effect cleanup: StrictMode runs this effect twice, and
  // without the guard a bare-address link would start two lookups. Cleanup
  // cannot stand in for it, since cancelling the first run's fetch and letting
  // the second redo it would still start the lookup twice in the other case.
  const started = useRef(false);
  useEffect(() => {
    if (started.current) return;
    started.current = true;

    const { lookupId, link } = readSearchLink(window.location.search);
    if (!lookupId) {
      if (link) handlers.current.onLink(link);
      return;
    }

    fetchStoredResult(lookupId)
      .catch((err) => {
        console.warn("[hjem] Stored result fetch failed:", err);
        return null;
      })
      .then((stored) => {
        setPending(false);
        if (stored) handlers.current.onStored(stored);
        else if (link) handlers.current.onLink(link);
      });
  }, []);

  return pending;
}
