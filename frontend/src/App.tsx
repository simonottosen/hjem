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

  return (
    <div className="min-h-screen bg-background">
      <div className="mx-auto max-w-7xl px-4 py-6 space-y-6">
        <header>
          <h1 className="text-xl font-bold tracking-tight">Hjem</h1>
          <p className="text-sm text-muted-foreground">
            Et værktøj til prissætning af boliger
          </p>
        </header>

        <SearchForm onSearch={handleSearch} isLoading={isLoading} />

        {isLoading && <ProgressBar progress={progress} />}

        {!isLoading && error && <ErrorAlert error={error} />}

        {!isLoading &&
          !error &&
          data &&
          data.addresses &&
          data.addresses.length > 0 && (
            <DashboardLayout
              data={data}
              query={lastQuery}
              range={lastRange}
            />
          )}
      </div>
    </div>
  );
}
