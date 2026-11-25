import { useState, useCallback } from "react";
import { ExternalLink } from "lucide-react";
import { useSearch } from "@/hooks/useSearch";
import { useProgress } from "@/hooks/useProgress";
import { useFilteredData } from "@/hooks/useFilteredData";
import { useStoredResult, writeSearchLink, type SearchLink } from "@/hooks/useStoredResult";
import { SearchForm } from "@/components/SearchForm";
import { ProgressBar } from "@/components/ProgressBar";
import { ProgressMap } from "@/components/ProgressMap";
import { ErrorAlert } from "@/components/ErrorAlert";
import { DashboardLayout } from "@/components/DashboardLayout";
import { StoredResultBanner } from "@/components/StoredResultBanner";

function Footer() {
  return (
    <footer className="border-t mt-8 py-4 text-center text-xs text-muted-foreground">
      <p>
        Lavet af Simon Ottosen, baseret på det oprindelige arbejde af Thomas Panum
      </p>
      <a
        href="https://github.com/simonottosen/hjem"
        target="_blank"
        rel="noopener noreferrer"
        className="inline-flex items-center gap-1 mt-1 hover:text-foreground transition-colors"
      >
        <ExternalLink className="size-3" />
        github.com/simonottosen/hjem
      </a>
    </footer>
  );
}

export default function App() {
  const { data, isLoading, error, search, setResult, setSearchError } = useSearch();
  const { progress, map, startPolling, reset } = useProgress();

  // Form state lifted here so it survives layout changes
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState("1");
  const [range, setRange] = useState("250");
  const [hasSearched, setHasSearched] = useState(false);
  // When the result on screen was made, if it came from a link rather than a
  // search run just now.
  const [storedAt, setStoredAt] = useState<string | null>(null);

  // Address exclusion state
  const [excludedAddrs, setExcludedAddrs] = useState<Set<number>>(new Set());

  const toggleExcluded = useCallback((addrIdx: number) => {
    setExcludedAddrs((prev) => {
      const next = new Set(prev);
      if (next.has(addrIdx)) {
        next.delete(addrIdx);
      } else {
        next.add(addrIdx);
      }
      return next;
    });
  }, []);

  // Filtered + recomputed data
  const filteredData = useFilteredData(data, excludedAddrs);

  // The searched address is always present in `addresses` — it is what
  // primary_idx points at, whether or not it has ever been sold — so the
  // address count no longer tells us whether the lookup found anything.
  // Sales do. Judged on the unfiltered response so that excluding every
  // neighbour narrows the dashboard rather than replacing it with a
  // "nothing found" message.
  const foundNoSales = !!data && (data.sales?.length ?? 0) === 0;
  const hasResults = !isLoading && !error && !!filteredData && !foundNoSales;

  // Takes the search as arguments rather than reading the form state, which a
  // link has only just asked React to set.
  const runSearch = useCallback(
    (link: Required<SearchLink>) => {
      setHasSearched(true);
      setExcludedAddrs(new Set());
      setStoredAt(null);
      reset();
      // The address goes into the URL now, so a reload mid-lookup searches
      // again; the stored id joins it once there is one.
      writeSearchLink(link);
      search(link.query, Number(link.range), Number(link.filter), (session) => {
        startPolling(
          session,
          (result) => {
            setResult(result);
            writeSearchLink(link, result.result_id);
          },
          setSearchError
        );
      });
    },
    [search, reset, startPolling, setResult, setSearchError]
  );

  const handleSearch = useCallback(() => {
    if (!query.trim()) return;
    runSearch({ query, range, filter });
  }, [query, range, filter, runSearch]);

  const linkPending = useStoredResult(
    (stored) => {
      setQuery(stored.query);
      setRange(String(stored.ranges[0] ?? range));
      setFilter(String(stored.filter));
      setHasSearched(true);
      setStoredAt(stored.created_at);
      setResult(stored.result);
    },
    (link) => {
      const full = { query: link.query, range: link.range ?? range, filter: link.filter ?? filter };
      setQuery(full.query);
      setRange(full.range);
      setFilter(full.filter);
      runSearch(full);
    }
  );

  const searchFormProps = {
    query,
    filter,
    range,
    onQueryChange: setQuery,
    onFilterChange: setFilter,
    onRangeChange: setRange,
    onSearch: handleSearch,
    isLoading: isLoading || linkPending,
  };

  // Before first search ever: centered landing page
  if (!hasSearched) {
    return (
      <div className="min-h-screen bg-background flex items-center justify-center px-4">
        <div className="w-full max-w-md space-y-6 -mt-10 sm:-mt-20">
          <div className="text-center space-y-2">
            <h1 className="text-3xl font-bold tracking-tight">Hjem</h1>
            <p className="text-muted-foreground">
              Prisanalyse af danske boliger baseret på historiske salgsdata fra Boliga
              og vurderinger fra Dingeo.
            </p>
            <p className="text-sm text-muted-foreground">
              Indtast en adresse for at se salgspriser, kvadratmeterpriser
              og estimeret værdi for boligen og lokalområdet.
            </p>
          </div>
          <SearchForm {...searchFormProps} />
          {linkPending && (
            <p className="text-center text-sm text-muted-foreground">Henter gemt resultat...</p>
          )}
          <Footer />
        </div>
      </div>
    );
  }

  // After search: compact header with results/loading/error
  return (
    <div className="min-h-screen bg-background">
      <div className="mx-auto max-w-7xl px-4 py-4 space-y-4">
        <div className="flex flex-col sm:flex-row sm:items-start gap-4">
          <div className="shrink-0">
            <h1 className="text-lg font-bold tracking-tight">Hjem</h1>
          </div>
          <div className="flex-1">
            <SearchForm {...searchFormProps} />
          </div>
        </div>

        {isLoading && (
          // The map sits above the bar rather than behind it: its attribution
          // control lives in its own bottom-right corner and would land on top
          // of the ETA.
          <div className="space-y-2">
            <ProgressMap map={map} />
            <ProgressBar progress={progress} />
          </div>
        )}

        {!isLoading && error && <ErrorAlert error={error} />}

        {!isLoading && !error && foundNoSales && (
          <div className="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800">
            Ingen sammenlignelige salg fundet i området. Prøv at søge med en større radius.
          </div>
        )}

        {hasResults && storedAt && <StoredResultBanner createdAt={storedAt} />}

        {hasResults && (
          <DashboardLayout
            data={filteredData!}
            rawData={data!}
            query={query}
            range={Number(range)}
            excludedAddrs={excludedAddrs}
            onToggleExcluded={toggleExcluded}
          />
        )}

        <Footer />
      </div>
    </div>
  );
}
