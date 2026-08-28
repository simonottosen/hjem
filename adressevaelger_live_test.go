package hjem

import (
	"os"
	"testing"
)

// Live smoke check against adressevaelger.dk. Skipped unless AV_LIVE=1.
func TestAVLiveSmoke(t *testing.T) {
	if os.Getenv("AV_LIVE") != "1" {
		t.Skip("set AV_LIVE=1 to run against the live service")
	}

	addrs, err := AVFuzzySearch{Query: "Rådhuspladsen 1, 1550 København V"}.Fetch()
	if err != nil {
		t.Fatalf("live Fetch: %v", err)
	}
	if len(addrs) != 1 {
		t.Fatalf("got %d addresses", len(addrs))
	}
	a := addrs[0]
	t.Logf("uuid=%s id=%q street=%q num=%q post=%q kommune=%q lat=%.6f lon=%.6f",
		a.DawaUUID, a.DawaID, a.StreetName, a.StreetNumber, a.PostalCode,
		a.MunicipalityCode, a.Longtitude, a.Latitude)

	for name, got := range map[string]string{
		"StreetName": a.StreetName, "StreetNumber": a.StreetNumber,
		"PostalCode": a.PostalCode, "MunicipalityCode": a.MunicipalityCode,
	} {
		if got == "" {
			t.Errorf("%s is empty — Boliga grouping would break", name)
		}
	}

	// Enrichment must reproduce the same fields via the batch endpoint.
	stub := []*Address{{DawaUUID: a.DawaUUID}}
	if err := avEnrichAddresses(stub); err != nil {
		t.Fatalf("live enrich: %v", err)
	}
	if stub[0].StreetName != a.StreetName || stub[0].PostalCode != a.PostalCode ||
		stub[0].MunicipalityCode != a.MunicipalityCode || stub[0].StreetNumber != a.StreetNumber {
		t.Errorf("enrichment disagrees with search: %+v vs %+v", stub[0], a)
	}
}

// TestAVLiveTransliteration is the live counterpart to TestAVStreetVariants: an
// address typed on a keyboard without Danish characters must still resolve.
// These all returned nothing before the re-spelling fallback existed.
func TestAVLiveTransliteration(t *testing.T) {
	if os.Getenv("AV_LIVE") != "1" {
		t.Skip("set AV_LIVE=1 to run against the live service")
	}

	for query, want := range map[string]string{
		"Norrebrogade 155, 2200 Kobenhavn N":  "Nørrebrogade 155, 2200 København N",
		"Osterbrogade 54, 2100 Kobenhavn O":   "Østerbrogade 54, 2100 København Ø",
		"Radhuspladsen 1, 1550 Kobenhavn V":   "Rådhuspladsen 1, 1550 København V",
		"Aboulevarden 1, 8000 Aarhus C":       "Åboulevarden 1, 8000 Aarhus C",
		"Slotsholmsgade 10, 1216 Kobenhavn K": "Slotsholmsgade 10, 1216 København K",
	} {
		addrs, err := AVFuzzySearch{Query: query}.Fetch()
		if err != nil {
			t.Errorf("%q: %v", query, err)
			continue
		}
		if len(addrs) != 1 {
			t.Errorf("%q: got %d addresses, want 1", query, len(addrs))
			continue
		}
		if addrs[0].DawaID != want {
			t.Errorf("%q resolved to %q, want %q", query, addrs[0].DawaID, want)
		}
	}
}

// TestAVLiveNedlagtRejected pins the one historical address confirmed against
// the live service. "Vestergade 1, 8000 Aarhus C" was renumbered to "1A", which
// is itself nedlagt (status 4) — /adresser/{id} answers 404 for it — so the
// correct outcome is a clear rejection rather than a resolved address.
//
// This doubles as the guard that Adressevask is actually being consulted:
// phonetic search returns nothing at all for this query, so reaching a nedlagt
// verdict is only possible via /vask/.
func TestAVLiveNedlagtRejected(t *testing.T) {
	if os.Getenv("AV_LIVE") != "1" {
		t.Skip("set AV_LIVE=1 to run against the live service")
	}

	_, err := AVFuzzySearch{Query: "Vestergade 1, 8000 Aarhus C"}.Fetch()
	if err == nil {
		t.Fatal("expected the nedlagt address to be rejected, got nil error")
	}
	t.Logf("rejected as expected: %v", err)
}

// TestAVLiveIntervalRejected pins the interval codes against the live service.
// Phonetic search returns nothing for a range of house numbers, so this reaches
// /vask/, which answers kode 800 with the *first* number in the range —
// "Christiansborg Slot 1". That is a different property from the one asked
// about, so it must be refused rather than silently valued.
func TestAVLiveIntervalRejected(t *testing.T) {
	if os.Getenv("AV_LIVE") != "1" {
		t.Skip("set AV_LIVE=1 to run against the live service")
	}

	_, err := AVFuzzySearch{Query: "Christiansborg Slot 1-5, 1218 København K"}.Fetch()
	if err == nil {
		t.Fatal("expected an interval address to be rejected, got nil error")
	}
	t.Logf("rejected as expected: %v", err)
}

// TestAVLiveHistoricalASCII is the intersection of the two blind spots: an
// address that has been renumbered *and* is typed without Danish characters.
// Phonetic search finds nothing for either the ASCII or the historical form, so
// resolving it depends entirely on the vask leg of the chain.
func TestAVLiveHistoricalASCII(t *testing.T) {
	if os.Getenv("AV_LIVE") != "1" {
		t.Skip("set AV_LIVE=1 to run against the live service")
	}

	const want = "Højgaardsvej 3B, 4760 Vordingborg"
	addrs, err := AVFuzzySearch{Query: "Hojgaardsvej 3, 4773 Stensved"}.Fetch()
	if err != nil {
		t.Fatalf("live Fetch: %v", err)
	}
	if len(addrs) != 1 {
		t.Fatalf("got %d addresses, want 1", len(addrs))
	}
	if addrs[0].DawaID != want {
		t.Errorf("resolved to %q, want %q", addrs[0].DawaID, want)
	}
}
