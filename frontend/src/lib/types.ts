export interface Address {
  full_txt: string;
  street_name: string;
  street_number: string;
  floor: string | null;
  door: string | null;
  zipcode: string;
  municipality_code: string;
  lat: number;
  long: number;
  building_size: number;
  property_size: number;
  basement_size: number;
  rooms: number;
  built_year: number;
  monthly_owner_expense_dkk: number;
  energy_marking: string;
}

export interface Sale {
  addr_idx: number;
  amount: number;
  sq_meters: number;
  rooms: number;
  build_year: number;
  when: string; // ISO 8601
}

export interface Aggregation {
  mean: number;
  std: number;
  n: number;
}

export interface SquareMeterPrices {
  global: Record<string, Aggregation>;
  projections: Array<Record<string, number>>;
}

export interface DingeoEstimate {
  name: string;
  link: string;
  value: number;
}

export interface DingeoValuation {
  adresseId: string;
  includedEvals: DingeoEstimate[];
  outlierEvals: DingeoEstimate[];
  minVal: number;
  maxVal: number;
  mean: number;
  standdev: number;
  countEvals: number;
}

export interface CompsEstimate {
  value: number;
  sqm_price: number;
  low: number;
  high: number;
  confidence: "high" | "medium" | "low";
  num_comps: number;
}

export interface LookupResponse {
  primary_idx: number;
  addresses: Address[] | null;
  sales: Sale[] | null;
  ranges: Record<number, number[]> | null;
  sqmeters: SquareMeterPrices;
  valuation?: DingeoValuation | null;
  comps_estimate?: CompsEstimate | null;
  warnings?: string[];
  error?: string;
}

export type ProgressStage =
  | "idle"
  | "dawa"
  | "boliga_client"
  | "boliga_list"
  | "boliga_properties"
  | "done"
  | "error";

// One street for the browser to fetch from Boliga. Mirrors
// BoligaPropertyRequest in boliga.go.
export interface BoligaTask {
  street: string;
  zipcode: number;
  municipality: number;
}

// What the browser posts back to /api/boliga/ingest. `sales` is Boliga's own
// results array, untouched — the client relays it rather than reading it, so
// client-fetched and server-fetched sales reach the same Go matcher.
export interface BoligaFetchResult {
  task: BoligaTask;
  sales: unknown[];
}

// `failed` holds streets Boliga actively refused. Streets we never reached are
// simply absent: the server treats anything it handed out and did not get back
// as its own work, so omission is already the safe default.
export interface BoligaRelayOutcome {
  fetched: BoligaFetchResult[];
  failed: BoligaTask[];
}

// The buildings one street query covers, as [lat, lon] pairs. Mirrors
// MapStreet in mapplan.go; the task is keyed identically to a BoligaTask so a
// completed fetch can reveal exactly the buildings it accounted for.
export interface MapStreet {
  task: BoligaTask;
  points: [number, number][];
}

// Everything the loading map draws. Mirrors MapPlan in mapplan.go.
export interface MapPlan {
  lat: number;
  lon: number;
  radius_m: number;
  streets: MapStreet[];
}

// A plan plus how much of it has been earned so far, keyed by streetKey(). The
// three states are kept apart deliberately: a street we fetched, a street
// Boliga refused us, and a street nobody has reached yet must not look alike.
export interface MapProgress {
  plan: MapPlan;
  done: Set<string>;
  failed: Set<string>;
  // The lookup has finished, so whatever the browser never reached was covered
  // by the server and can stop being drawn as pending.
  settled: boolean;
}

export interface ProgressEvent {
  stage: ProgressStage;
  message: string;
  current: number;
  total: number;
  elapsed_ms: number;
  warnings?: string[];
  result?: LookupResponse;
  // Present only while the stage is "boliga_client", so a poll that arrives
  // after the server stopped waiting cannot restart a relay.
  boliga_tasks?: BoligaTask[];
  // Present only on the two stages between the addresses being known and the
  // fetching starting. The client keeps the first copy it sees.
  map?: MapPlan;
}
