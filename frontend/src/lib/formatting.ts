export function formatDKK(amount: number): string {
  return amount.toLocaleString("da-DK") + " kr";
}

export function formatDate(iso: string): string {
  const d = new Date(iso);
  return d.toLocaleDateString("da-DK", {
    year: "numeric",
    month: "short",
    day: "numeric",
  });
}

export function formatTime(ms: number): string {
  const secs = Math.floor(ms / 1000);
  if (secs < 60) return secs + "s";
  const mins = Math.floor(secs / 60);
  const remSecs = secs % 60;
  return mins + "m " + remSecs + "s";
}

export function formatPct(value: number): string {
  const sign = value >= 0 ? "+" : "";
  return sign + value.toFixed(1) + "%";
}

const errorTranslations: Record<string, string> = {
  "non-unique address":
    "Der findes flere adresser med den beskrivelse, vær mere præcis",
  "no found address": "Kunne ikke finde nogen adresser udfra den søgning",
  "forbindelsen":
    "Forbindelsen til serveren blev afbrudt. Prøv igen — det kan tage op til 5 minutter at hente alle salgsdata.",
  "load failed":
    "Forbindelsen til serveren blev afbrudt. Prøv igen — det kan tage op til 5 minutter at hente alle salgsdata.",
  "network":
    "Netværksfejl. Tjek din forbindelse og prøv igen.",
  "alle":
    "Alle gade-opslag fejlede. Prøv igen om et par minutter.",
  "429":
    "Boliga har midlertidigt blokeret forespørgsler. Prøv igen om et par minutter.",
  "rate limit":
    "Boliga har midlertidigt blokeret forespørgsler. Prøv igen om et par minutter.",
  "status 5":
    "Boliga-serveren har midlertidigt problemer. Prøv igen senere.",
};

export function translateError(error: string): string {
  const lower = error.toLowerCase();
  for (const [key, translation] of Object.entries(errorTranslations)) {
    if (lower.includes(key)) return translation;
  }
  return error;
}
