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

const errorTranslations: Record<string, string> = {
  "non-unique address":
    "Der findes flere adresser med den beskrivelse, vær mere præcis",
  "no found address": "Kunne ikke finde nogen adresser udfra den søgning",
};

export function translateError(error: string): string {
  const lower = error.toLowerCase();
  for (const [key, translation] of Object.entries(errorTranslations)) {
    if (lower.includes(key)) return translation;
  }
  return error;
}
