import { useState, useCallback } from "react";
import { useSearch } from "@/hooks/useSearch";
import { useProgress } from "@/hooks/useProgress";
import { SearchForm } from "@/components/SearchForm";
import { ProgressBar } from "@/components/ProgressBar";
import { ErrorAlert } from "@/components/ErrorAlert";
import { DashboardLayout } from "@/components/DashboardLayout";

export default function App() {
  const { data, isLoading, error, search } = useSearch();
  const { progress, connect, reset } = useProgress();
  const [lastQuery, setLastQuery] = useState("");
  const [lastRange, setLastRange] = useState(250);
  const hasResults = !isLoading && !error && data?.addresses && data.addresses.length > 0;

  const handleSearch = useCallback(
    (query: string, range: number, filter: number) => {
      setLastQuery(query);
      setLastRange(range);
      reset();
      connect();
      search(query, range, filter);
    },
    [search, connect, reset]
  );

  // Before any search: centered landing page
  if (!hasResults && !isLoading && !error) {
    return (
      <div className="min-h-screen bg-background flex items-center justify-center px-4">
        <div className="w-full max-w-md space-y-6 -mt-20">
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
          <SearchForm onSearch={handleSearch} isLoading={isLoading} />
        </div>
      </div>
    );
  }

  // After search: compact header with results
  return (
    <div className="min-h-screen bg-background">
      <div className="mx-auto max-w-7xl px-4 py-4 space-y-4">
        <div className="flex flex-col sm:flex-row sm:items-start gap-4">
          <div className="shrink-0">
            <h1 className="text-lg font-bold tracking-tight">Hjem</h1>
          </div>
          <div className="flex-1">
            <SearchForm onSearch={handleSearch} isLoading={isLoading} />
          </div>
        </div>

        {isLoading && <ProgressBar progress={progress} />}

        {!isLoading && error && <ErrorAlert error={error} />}

        {hasResults && (
          <DashboardLayout
            data={data!}
            query={lastQuery}
            range={lastRange}
          />
        )}
      </div>
    </div>
  );
}
