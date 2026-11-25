import { useState, type FormEvent } from "react";
import { Search, Info, Map as MapIcon } from "lucide-react";
import { cn } from "@/lib/utils";
import { areaM2, formatKm2, type LonLat } from "@/lib/area";
import { AreaPicker } from "./AreaPicker";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";

// How the area around the address is chosen: a circle of the radius picked
// below, or a shape drawn on a map.
export type AreaMode = "radius" | "area";

interface SearchFormProps {
  query: string;
  filter: string;
  range: string;
  mode: AreaMode;
  area: LonLat[] | null;
  onQueryChange: (value: string) => void;
  onFilterChange: (value: string) => void;
  onRangeChange: (value: string) => void;
  onModeChange: (value: AreaMode) => void;
  onAreaChange: (value: LonLat[] | null) => void;
  onSearch: () => void;
  isLoading: boolean;
}

export function SearchForm({
  query,
  filter,
  range,
  mode,
  area,
  onQueryChange,
  onFilterChange,
  onRangeChange,
  onModeChange,
  onAreaChange,
  onSearch,
  isLoading,
}: SearchFormProps) {
  // The address the map was opened for. Kept apart from the query so typing
  // does not re-locate on every keystroke. Numbered so that asking again for
  // the same address — after a failed lookup, say — opens a fresh map rather
  // than being a state update React skips.
  const [mapRequest, setMapRequest] = useState<{ query: string; n: number } | null>(null);
  const canSearch = !!query.trim() && (mode === "radius" || area !== null);

  // An area is drawn around one address. Searching it for another would
  // report that address's value from some other neighbourhood's sales, so a
  // different address means drawing again, on a map of the new one.
  function handleQueryChange(value: string) {
    if (value.trim() !== query.trim()) {
      setMapRequest(null);
      if (area) onAreaChange(null);
    }
    onQueryChange(value);
  }

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (!canSearch) return;
    // The progress map takes over from here, and two maps on one page is one
    // too many on a phone.
    setMapRequest(null);
    onSearch();
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-3">
      <div className="flex gap-2">
        <Input
          value={query}
          onChange={(e) => handleQueryChange(e.target.value)}
          placeholder="Adresse..."
          className="flex-1"
          disabled={isLoading}
        />
        <Button type="submit" disabled={isLoading || !canSearch}>
          <Search />
          Søg
        </Button>
      </div>
      <div className="flex flex-col sm:flex-row gap-2 sm:gap-4">
        <div className="flex-1 space-y-1">
          <label className="text-xs text-muted-foreground font-medium inline-flex items-center gap-1">
            Filtrering
            <TooltipProvider>
              <Tooltip>
                <TooltipTrigger asChild>
                  <span className="inline-flex items-center justify-center size-3.5 rounded-full bg-muted-foreground/20 text-muted-foreground cursor-help">
                    <Info className="size-2.5" />
                  </span>
                </TooltipTrigger>
                <TooltipContent className="max-w-[250px] text-xs">
                  Fjerner salg der afviger kraftigt fra gennemsnittet i området.
                  Jo lavere sigma-værdi, jo flere afvigende salg filtreres fra.
                  Vælg "Ingen" for at se alle salg.
                </TooltipContent>
              </Tooltip>
            </TooltipProvider>
          </label>
          <Select value={filter} onValueChange={onFilterChange}>
            <SelectTrigger className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="-1">Ingen</SelectItem>
              <SelectItem value="3">{"x < 3\u03C3"}</SelectItem>
              <SelectItem value="2">{"x < 2\u03C3"}</SelectItem>
              <SelectItem value="1">{"x < \u03C3"}</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div className="flex-1 space-y-1">
          <div className="flex items-center justify-between gap-2">
            <label className="text-xs text-muted-foreground font-medium">
              Område
            </label>
            <div className="inline-flex rounded-md bg-muted p-0.5 text-xs">
              {(["radius", "area"] as const).map((m) => (
                <button
                  key={m}
                  type="button"
                  aria-pressed={mode === m}
                  disabled={isLoading}
                  onClick={() => onModeChange(m)}
                  className={cn(
                    "rounded px-2 py-0.5 transition-colors disabled:opacity-50",
                    mode === m
                      ? "bg-background text-foreground shadow-xs"
                      : "text-muted-foreground hover:text-foreground"
                  )}
                >
                  {m === "radius" ? "Radius" : "Tegn område"}
                </button>
              ))}
            </div>
          </div>
          {mode === "radius" ? (
            <Select value={range} onValueChange={onRangeChange}>
              <SelectTrigger className="w-full" aria-label="Søgeradius">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="250">{"< 250m"}</SelectItem>
                <SelectItem value="500">{"< 500m"}</SelectItem>
                <SelectItem value="750">{"< 750m"}</SelectItem>
                <SelectItem value="1000">{"< 1000m"}</SelectItem>
              </SelectContent>
            </Select>
          ) : (
            <div className="flex items-center gap-2">
              <Button
                type="button"
                variant="outline"
                disabled={isLoading || !query.trim()}
                // Untrimmed, as the lookup sends it: the server caches the
                // address under exactly this text, and the lookup finds it there.
                onClick={() => setMapRequest((r) => ({ query, n: (r?.n ?? 0) + 1 }))}
              >
                <MapIcon />
                Vis på kort
              </Button>
              <span className="min-w-0 truncate text-sm text-muted-foreground">
                {area ? `Område: ${formatKm2(areaM2(area))}` : "Tegn et område på kortet"}
              </span>
            </div>
          )}
        </div>
      </div>
      {mode === "area" && mapRequest !== null && (
        <AreaPicker key={mapRequest.n} query={mapRequest.query} area={area} onAreaChange={onAreaChange} />
      )}
    </form>
  );
}
