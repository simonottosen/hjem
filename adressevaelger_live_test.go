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
