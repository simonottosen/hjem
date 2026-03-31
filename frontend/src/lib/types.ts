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

export interface LookupResponse {
  primary_idx: number;
  addresses: Address[] | null;
  sales: Sale[] | null;
  ranges: Record<number, number[]> | null;
  sqmeters: SquareMeterPrices;
  error?: string;
}

export type ProgressStage =
  | "idle"
  | "dawa"
  | "boliga_list"
  | "boliga_properties"
  | "done"
  | "error";

export interface ProgressEvent {
  stage: ProgressStage;
  message: string;
  current: number;
  total: number;
  elapsed_ms: number;
}
