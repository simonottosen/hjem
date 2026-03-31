import { useState, type FormEvent } from "react";
import { Search, Info } from "lucide-react";
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

interface SearchFormProps {
  onSearch: (query: string, range: number, filter: number) => void;
  isLoading: boolean;
}

export function SearchForm({ onSearch, isLoading }: SearchFormProps) {
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState("1");
  const [range, setRange] = useState("250");

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (!query.trim()) return;
    onSearch(query, Number(range), Number(filter));
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-3">
      <div className="flex gap-2">
        <Input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Adresse..."
          className="flex-1"
          disabled={isLoading}
        />
        <Button type="submit" disabled={isLoading || !query.trim()}>
          <Search />
          Søg
        </Button>
      </div>
      <div className="flex gap-4">
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
                <TooltipContent side="top" className="max-w-[250px] text-xs">
                  Fjerner salg der afviger kraftigt fra gennemsnittet i området.
                  Jo lavere sigma-værdi, jo flere afvigende salg filtreres fra.
                  Vælg "Ingen" for at se alle salg.
                </TooltipContent>
              </Tooltip>
            </TooltipProvider>
          </label>
          <Select value={filter} onValueChange={setFilter}>
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
          <label className="text-xs text-muted-foreground font-medium">
            Område
          </label>
          <Select value={range} onValueChange={setRange}>
            <SelectTrigger className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="250">{"< 250m"}</SelectItem>
              <SelectItem value="500">{"< 500m"}</SelectItem>
              <SelectItem value="750">{"< 750m"}</SelectItem>
              <SelectItem value="1000">{"< 1000m"}</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>
    </form>
  );
}
