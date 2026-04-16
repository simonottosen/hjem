import { useState, useCallback } from "react";
import { ExternalLink } from "lucide-react";
import { useSearch } from "@/hooks/useSearch";
import { useProgress } from "@/hooks/useProgress";
import { useFilteredData } from "@/hooks/useFilteredData";
import { SearchForm } from "@/components/SearchForm";
import { ProgressBar } from "@/components/ProgressBar";
import { ErrorAlert } from "@/components/ErrorAlert";
import { DashboardLayout } from "@/components/DashboardLayout";

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
  const { progress, startPolling, reset } = useProgress();

  // Form state lifted here so it survives layout changes
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState("1");
  const [range, setRange] = useState("250");
  const [hasSearched, setHasSearched] = useState(false);

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

  const hasResults =
    !isLoading && !error && filteredData?.addresses && filteredData.addresses.length > 0;

  const handleSearch = useCallback(() => {
    if (!query.trim()) return;
    setHasSearched(true);
    setExcludedAddrs(new Set());
    reset();
    search(query, Number(range), Number(filter), () => {
      startPolling(setResult, setSearchError);
    });
  }, [query, range, filter, search, reset, startPolling, setResult, setSearchError]);

  const searchFormProps = {
    query,
    filter,
    range,
    onQueryChange: setQuery,
    onFilterChange: setFilter,
    onRangeChange: setRange,
    onSearch: handleSearch,
    isLoading,
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

        {isLoading && <ProgressBar progress={progress} />}

        {!isLoading && error && <ErrorAlert error={error} />}

        {!isLoading && !error && data && (!data.addresses || data.addresses.length === 0) && (
          <div className="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800">
            Ingen sammenlignelige salg fundet i området. Prøv at søge med en større radius.
          </div>
        )}

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
