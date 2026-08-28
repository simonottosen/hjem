package hjem

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Captured verbatim from a live GET /adresser/{id} response for
// "Rådhuspladsen 1, 1550 København V". Trimmed to the fields we consume, with
// the nesting preserved exactly.
const avAddressFixture = `{
  "status": "ok",
  "adresse": {
    "id_lokalid": "70865c44-d570-44e7-a6f5-6f7c90add725",
    "adressebetegnelse": "Rådhuspladsen 1, 1550 København V",
    "etagebetegnelse": null,
    "doerbetegnelse": null,
    "status": "3",
    "husnummer": {
      "id_lokalid": "0a3f507a-ec01-32b8-e044-0003ba298018",
      "husnummertekst": "1",
      "adgangsadressebetegnelse": "Rådhuspladsen 1, 1550 København V",
      "vejnavn": "Rådhuspladsen",
      "status": "3",
      "adgangspunkt": {
        "id_lokalid": "0a3f507a-ec01-32b8-e044-0003ba298018",
        "geometri": {
          "type": "Point",
          "crs": { "type": "name", "properties": { "name": "EPSG:25832" } },
          "coordinates": [724434.93, 6175755.61]
        },
        "koordinater": { "x": 724434.93, "y": 6175755.61 }
      },
      "postnummer": { "id_lokalid": "327f", "navn": "København V", "postnr": "1550" },
      "navngivenvej": { "id_lokalid": "db18", "vejnavn": "Rådhuspladsen" },
      "navngivenvejkommunedel": { "id_lokalid": "5c99", "kommune": "0101", "vejkode": "6152" }
    }
  }
}`

const avSearchFixture = `{
  "status": "ok",
  "beskrivelse": "",
  "fund": [
    {
      "type": "adresse",
      "id": "70865c44-d570-44e7-a6f5-6f7c90add725",
      "titel": "Rådhuspladsen 1, 1550 København V",
      "husnummerId": "0a3f507a-ec01-32b8-e044-0003ba298018"
    },
    {
      "type": "adresse",
      "id": "38111bb7-53b7-4a64-94fb-4dbf12f76b49",
      "titel": "Rådhuspladsen 1, st., 1550 København V",
      "husnummerId": "0a3f507a-ec01-32b8-e044-0003ba298018"
    }
  ]
}`

// avTestServer stands in for adressevaelger.dk and points ADRESSEVAELGER_URL at
// itself for the duration of the test.
func avTestServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	t.Setenv("ADRESSEVAELGER_URL", srv.URL)
}

func TestAVToAddressMapping(t *testing.T) {
	var resp avAddressResponse
	if err := json.Unmarshal([]byte(avAddressFixture), &resp); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}

	addr, err := avToAddress(resp.Adresse)
	if err != nil {
		t.Fatalf("avToAddress: %v", err)
	}

	// These four are the fields BoligaSalesFromAddrs groups on. If any regress to
	// empty, the Boliga lookup silently returns no sales.
	if addr.StreetName != "Rådhuspladsen" {
		t.Errorf("StreetName = %q, want %q", addr.StreetName, "Rådhuspladsen")
	}
	if addr.StreetNumber != "1" {
		t.Errorf("StreetNumber = %q, want %q", addr.StreetNumber, "1")
	}
	if addr.PostalCode != "1550" {
		t.Errorf("PostalCode = %q, want %q", addr.PostalCode, "1550")
	}
	if addr.MunicipalityCode != "0101" {
		t.Errorf("MunicipalityCode = %q, want %q", addr.MunicipalityCode, "0101")
	}

	if addr.DawaUUID != "70865c44-d570-44e7-a6f5-6f7c90add725" {
		t.Errorf("DawaUUID = %q", addr.DawaUUID)
	}
	if addr.DawaID != "Rådhuspladsen 1, 1550 København V" {
		t.Errorf("DawaID = %q", addr.DawaID)
	}
	if addr.Floor != nil {
		t.Errorf("Floor = %v, want nil", *addr.Floor)
	}
	if addr.Door != nil {
		t.Errorf("Door = %v, want nil", *addr.Door)
	}
}

// TestAVCoordinateQuirk pins the deliberately swapped storage convention: the
// Address struct keeps longitude in .Latitude and latitude in .Longtitude,
// matching dawa.go and datafordeler.go. Getting this backwards would place the
// radius search in the wrong hemisphere.
func TestAVCoordinateQuirk(t *testing.T) {
	var resp avAddressResponse
	if err := json.Unmarshal([]byte(avAddressFixture), &resp); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}

	addr, err := avToAddress(resp.Adresse)
	if err != nil {
		t.Fatalf("avToAddress: %v", err)
	}

	// EPSG:25832 (724434.93, 6175755.61) — Rådhuspladsen 1. Cross-checked against
	// an independent Snyder inverse-transverse-Mercator implementation, which
	// agrees to six decimal places.
	const wantLat, wantLon = 55.675627, 12.569578

	if math.Abs(addr.Longtitude-wantLat) > 0.001 {
		t.Errorf("Longtitude (holds latitude) = %.6f, want ~%.4f", addr.Longtitude, wantLat)
	}
	if math.Abs(addr.Latitude-wantLon) > 0.001 {
		t.Errorf("Latitude (holds longitude) = %.6f, want ~%.4f", addr.Latitude, wantLon)
	}

	// addrLatLon is what datafordeler.go uses to unpack the quirk; it must agree.
	lat, lon := addrLatLon(*addr)
	if math.Abs(lat-wantLat) > 0.001 || math.Abs(lon-wantLon) > 0.001 {
		t.Errorf("addrLatLon = (%.6f, %.6f), want ~(%.4f, %.4f)", lat, lon, wantLat, wantLon)
	}
}

// TestAVMissingAccessPointRejected guards the 0,0 case. inverseUTM32(0,0)
// returns a valid-looking point far off the coast of Africa, so silently
// accepting it would re-centre the whole radius search.
func TestAVMissingAccessPointRejected(t *testing.T) {
	var resp avAddressResponse
	if err := json.Unmarshal([]byte(avAddressFixture), &resp); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	resp.Adresse.Husnummer.Adgangspunkt.Koordinater.X = 0
	resp.Adresse.Husnummer.Adgangspunkt.Koordinater.Y = 0

	if _, err := avToAddress(resp.Adresse); err == nil {
		t.Fatal("expected an error for a missing access point, got nil")
	}
}

// TestAVFuzzySearchFetch drives the full two-call flow against a stub server and
// asserts the top-ranked candidate wins.
func TestAVFuzzySearchFetch(t *testing.T) {
	var searchHits, lookupHits int
	var gotTekst, gotPath string

	avTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path == "/adresser/soeg" {
			searchHits++
			gotTekst = r.URL.Query().Get("tekst")
			if r.URL.Query().Get("token") == "" {
				t.Error("search call is missing the mandatory token parameter")
			}
			w.Write([]byte(avSearchFixture))
			return
		}

		lookupHits++
		gotPath = r.URL.Path
		w.Write([]byte(avAddressFixture))
	})

	addrs, err := AVFuzzySearch{Query: "Rådhuspladsen 1"}.Fetch()
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if len(addrs) != 1 {
		t.Fatalf("got %d addresses, want exactly 1 (the best match)", len(addrs))
	}
	if gotTekst != "Rådhuspladsen 1" {
		t.Errorf("tekst = %q", gotTekst)
	}
	if searchHits != 1 || lookupHits != 1 {
		t.Errorf("calls: search=%d lookup=%d, want 1 and 1", searchHits, lookupHits)
	}
	// The first candidate must be the one looked up, not any later variant.
	if !strings.HasSuffix(gotPath, "/70865c44-d570-44e7-a6f5-6f7c90add725") {
		t.Errorf("looked up %q, want the top-ranked candidate", gotPath)
	}
	if addrs[0].StreetName != "Rådhuspladsen" || addrs[0].PostalCode != "1550" {
		t.Errorf("mapped address = %+v", addrs[0])
	}
}

// TestAVFuzzySearchNoResults asserts an empty candidate list is not an error, so
// api.go's existing "no found address" branch is what the user sees.
func TestAVFuzzySearchNoResults(t *testing.T) {
	avTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","beskrivelse":"","fund":[]}`))
	})

	addrs, err := AVFuzzySearch{Query: "ikke en adresse"}.Fetch()
	if err != nil {
		t.Fatalf("empty results should not error, got %v", err)
	}
	if len(addrs) != 0 {
		t.Fatalf("got %d addresses, want 0", len(addrs))
	}
}

// TestAVFuzzySearchServerError covers the non-200 branch in avGet. It uses 404
// rather than 500 deliberately: DefaultClient's RetryRoundTripper retries 5xx
// and 429 with a 2s..32s backoff, which would stretch this test to over a
// minute without testing anything extra.
func TestAVFuzzySearchServerError(t *testing.T) {
	avTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	if _, err := (AVFuzzySearch{Query: "Rådhuspladsen 1"}).Fetch(); err == nil {
		t.Fatal("expected an error on a non-200 response, got nil")
	}
}

// TestAVRequestExcludesToken keeps the credential out of the DawaQueryCache
// primary key, which is derived from Request().URL.
func TestAVRequestExcludesToken(t *testing.T) {
	t.Setenv("ADRESSEVAELGER_TOKEN", "secret-token-value")

	req := AVFuzzySearch{Query: "Rådhuspladsen 1"}.Request()
	if req == nil {
		t.Fatal("Request() returned nil")
	}

	if strings.Contains(req.URL.String(), "secret-token-value") {
		t.Errorf("cache key URL leaks the token: %s", req.URL)
	}
	if req.URL.Query().Get("tekst") != "Rådhuspladsen 1" {
		t.Errorf("cache key is missing the query text: %s", req.URL)
	}
}

// avBatchFixture is a /adresser/?id_lokalids=… response, captured from the live
// service. Two records: one complete, one deliberately without an access point.
const avBatchFixture = `{
  "status": "ok",
  "adresser": [
    {
      "id_lokalid": "70865c44-d570-44e7-a6f5-6f7c90add725",
      "adressebetegnelse": "Rådhuspladsen 1, 1550 København V",
      "etagebetegnelse": null,
      "doerbetegnelse": null,
      "husnummer": {
        "id_lokalid": "0a3f507a-ec01-32b8-e044-0003ba298018",
        "husnummertekst": "1",
        "vejnavn": "Rådhuspladsen",
        "adgangspunkt": { "koordinater": { "x": 724434.93, "y": 6175755.61 } },
        "postnummer": { "navn": "København V", "postnr": "1550" },
        "navngivenvejkommunedel": { "kommune": "0101", "vejkode": "6152" }
      }
    },
    {
      "id_lokalid": "38111bb7-53b7-4a64-94fb-4dbf12f76b49",
      "adressebetegnelse": "Rådhuspladsen 1, st., 1550 København V",
      "etagebetegnelse": "st",
      "doerbetegnelse": null,
      "husnummer": {
        "id_lokalid": "0a3f507a-ec01-32b8-e044-0003ba298018",
        "husnummertekst": "1",
        "vejnavn": "Rådhuspladsen",
        "adgangspunkt": { "koordinater": { "x": 0, "y": 0 } },
        "postnummer": { "navn": "København V", "postnr": "1550" },
        "navngivenvejkommunedel": { "kommune": "0101", "vejkode": "6152" }
      }
    }
  ]
}`

// TestAVEnrichAddresses is the guard on the Boliga blocker. DARNearbySearch
// returns addresses with only id/betegnelse/floor/door/coords populated;
// BoligaSalesFromAddrs groups on {MunicipalityCode, StreetName, PostalCode}, so
// if enrichment stops filling those the sales lookup silently returns nothing.
func TestAVEnrichAddresses(t *testing.T) {
	var gotIDs string
	var calls int

	avTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotIDs = r.URL.Query().Get("id_lokalids")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(avBatchFixture))
	})

	// As produced by DARNearbySearch: ids and coordinates, nothing else.
	addrs := []*Address{
		{DawaUUID: "70865c44-d570-44e7-a6f5-6f7c90add725", Latitude: 12.5, Longtitude: 55.6},
		{DawaUUID: "38111bb7-53b7-4a64-94fb-4dbf12f76b49", Latitude: 12.5, Longtitude: 55.6},
	}

	if err := avEnrichAddresses(addrs); err != nil {
		t.Fatalf("avEnrichAddresses: %v", err)
	}

	if calls != 1 {
		t.Errorf("made %d batch calls, want 1", calls)
	}
	if !strings.Contains(gotIDs, "70865c44") || !strings.Contains(gotIDs, "38111bb7") {
		t.Errorf("id_lokalids = %q, want both ids", gotIDs)
	}

	for i, a := range addrs {
		if a.StreetName != "Rådhuspladsen" {
			t.Errorf("addrs[%d].StreetName = %q", i, a.StreetName)
		}
		if a.StreetNumber != "1" {
			t.Errorf("addrs[%d].StreetNumber = %q", i, a.StreetNumber)
		}
		if a.PostalCode != "1550" {
			t.Errorf("addrs[%d].PostalCode = %q", i, a.PostalCode)
		}
		if a.MunicipalityCode != "0101" {
			t.Errorf("addrs[%d].MunicipalityCode = %q", i, a.MunicipalityCode)
		}
	}

	// Coordinates supplied by DAR must survive enrichment untouched — DAR's are
	// projected from the access point and already verified against DAWA.
	for i, a := range addrs {
		if a.Latitude != 12.5 || a.Longtitude != 55.6 {
			t.Errorf("addrs[%d] coordinates overwritten: %.4f, %.4f", i, a.Latitude, a.Longtitude)
		}
	}
}

// TestAVEnrichKeepsRecordsWithoutAccessPoint pins the reason avToAddress and
// avMapFields are separate: a record with no access point still carries usable
// street and postal data, and dropping it would silently shrink the radius result.
func TestAVEnrichKeepsRecordsWithoutAccessPoint(t *testing.T) {
	avTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(avBatchFixture))
	})

	// No coordinates from DAR either, so nothing can backfill them.
	addrs := []*Address{{DawaUUID: "38111bb7-53b7-4a64-94fb-4dbf12f76b49"}}

	if err := avEnrichAddresses(addrs); err != nil {
		t.Fatalf("avEnrichAddresses: %v", err)
	}

	if addrs[0].StreetName != "Rådhuspladsen" || addrs[0].PostalCode != "1550" {
		t.Errorf("street data dropped for a record without an access point: %+v", addrs[0])
	}
	// inverseUTM32(0,0) lands off the coast of Africa; 0,0 must stay 0,0.
	if addrs[0].Latitude != 0 || addrs[0].Longtitude != 0 {
		t.Errorf("a missing access point was projected: %.6f, %.6f", addrs[0].Latitude, addrs[0].Longtitude)
	}
}

// TestAVEnrichBatches confirms large id sets are split, since the ids ride in
// the query string and the service answers an over-long request line with 431.
func TestAVEnrichBatches(t *testing.T) {
	var batchSizes []int

	avTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		ids := strings.Split(r.URL.Query().Get("id_lokalids"), ",")
		batchSizes = append(batchSizes, len(ids))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","adresser":[]}`))
	})

	addrs := make([]*Address, 250)
	for i := range addrs {
		addrs[i] = &Address{DawaUUID: fmt.Sprintf("id-%03d", i)}
	}

	if err := avEnrichAddresses(addrs); err != nil {
		t.Fatalf("avEnrichAddresses: %v", err)
	}

	if len(batchSizes) != 3 {
		t.Fatalf("250 ids produced %d batches (%v), want 3 of at most %d", len(batchSizes), batchSizes, avBatchSize)
	}
	for i, n := range batchSizes {
		if n > avBatchSize {
			t.Errorf("batch %d had %d ids, exceeding avBatchSize=%d", i, n, avBatchSize)
		}
	}
}

// --- Fallback chain (issues #18, #19) ------------------------------------

// TestAVStreetVariants pins the re-spelling generator: street-name only, most
// conservative candidate first, case preserved, bounded.
func TestAVStreetVariants(t *testing.T) {
	t.Run("varies only the street name", func(t *testing.T) {
		got := avStreetVariants("Norrebrogade 155, 2200 Kobenhavn N")
		if len(got) == 0 {
			t.Fatal("no variants generated")
		}
		// The correct spelling must be tried first — it is the likeliest fix
		// and every later candidate costs another request.
		want := "Nørrebrogade 155, 2200 Kobenhavn N"
		if got[0] != want {
			t.Errorf("first variant = %q, want %q", got[0], want)
		}
		// "Kobenhavn" must be left alone: the postnummer already identifies the
		// town, and varying it would multiply the search space for nothing.
		for _, v := range got {
			if !strings.Contains(v, "Kobenhavn N") {
				t.Errorf("variant altered text after the house number: %q", v)
			}
		}
	})

	t.Run("preserves case", func(t *testing.T) {
		got := avStreetVariants("Osterbrogade 54, 2100 Kobenhavn O")
		if len(got) == 0 || !strings.HasPrefix(got[0], "Østerbrogade") {
			t.Fatalf("want a leading uppercase Ø, got %q", got)
		}
	})

	t.Run("handles digraphs", func(t *testing.T) {
		got := avStreetVariants("Noerrebrogade 155, 2200")
		if len(got) == 0 || got[0] != "Nørrebrogade 155, 2200" {
			t.Fatalf(`want "oe" collapsed to "ø" first, got %q`, got)
		}
	})

	t.Run("is bounded", func(t *testing.T) {
		got := avStreetVariants("Aaaaoooo 1, 1000 X")
		if len(got) > avMaxVariants {
			t.Errorf("generated %d variants, cap is %d", len(got), avMaxVariants)
		}
	})

	t.Run("no house number means all street", func(t *testing.T) {
		if got := avStreetVariants("Norrebrogade"); len(got) == 0 {
			t.Error("expected variants for a bare street name")
		}
	})
}

// avVaskMiss renders a no-match Adressevask response with the given code.
func avVaskMiss(kode int) string {
	return fmt.Sprintf(`{"vaskestatus":{"kode":%d,"tekst":"ingen match"},`+
		`"vaskeresultat":{"adresse_id_lokalid":null,"adressebetegnelse":null,"status":null},`+
		`"vaskeresultat_historisk":{"adressebetegnelse":null}}`, kode)
}

// avChainServer serves the fallback chain. /adresser/soeg answers a hit only
// for the queries in hits, and /vask/ only for those in vask — both keyed by the
// exact query string, so a test can pin which spelling each step was asked for.
// Anything else misses with missKode, and /adresser/{id} returns the fixture.
//
// Requests are recorded as paths, except /vask/ which records the address it was
// asked to wash, so tests can assert on the order and spelling of the chain.
func avChainServer(t *testing.T, hits, vask map[string]string, missKode int) *[]string {
	t.Helper()
	var calls []string

	avTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.URL.Path == "/adresser/soeg":
			calls = append(calls, r.URL.Path)
			tekst := r.URL.Query().Get("tekst")
			id, ok := hits[tekst]
			if !ok {
				w.Write([]byte(`{"status":"ok","beskrivelse":"","fund":[]}`))
				return
			}
			fmt.Fprintf(w, `{"status":"ok","fund":[{"type":"adresse","id":%q,"titel":%q}]}`, id, tekst)

		case r.URL.Path == "/vask/":
			adresse := r.URL.Query().Get("adresse")
			calls = append(calls, "/vask/?"+adresse)
			if body, ok := vask[adresse]; ok {
				w.Write([]byte(body))
				return
			}
			w.Write([]byte(avVaskMiss(missKode)))

		case strings.HasPrefix(r.URL.Path, "/adresser/"):
			calls = append(calls, r.URL.Path)
			w.Write([]byte(avAddressFixture))

		default:
			t.Errorf("unexpected request path %s", r.URL.Path)
		}
	})

	return &calls
}

// avCountVask returns how many /vask/ calls were recorded.
func avCountVask(calls []string) int {
	var n int
	for _, p := range calls {
		if strings.HasPrefix(p, "/vask/") {
			n++
		}
	}
	return n
}

// TestAVTransliterationFallback is the #19 regression guard: an address typed
// without Danish characters must still resolve.
func TestAVTransliterationFallback(t *testing.T) {
	const id = "70865c44-d570-44e7-a6f5-6f7c90add725"
	calls := avChainServer(t, map[string]string{
		// Only the correctly spelled query hits, as the live service behaves.
		"Rådhuspladsen 1, 1550 Kobenhavn V": id,
	}, nil, avVaskNoStreetInPost)

	addrs, err := AVFuzzySearch{Query: "Radhuspladsen 1, 1550 Kobenhavn V"}.Fetch()
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(addrs) != 1 {
		t.Fatalf("got %d addresses, want 1", len(addrs))
	}
	if addrs[0].StreetName != "Rådhuspladsen" {
		t.Errorf("StreetName = %q, want %q", addrs[0].StreetName, "Rådhuspladsen")
	}

	// It must not have needed Adressevask for a mere spelling problem.
	if n := avCountVask(*calls); n != 0 {
		t.Errorf("made %d /vask/ calls when a re-spelling already matched", n)
	}
}

// TestAVVaskFallback is the #18 regression guard: a historical designation that
// phonetic search cannot find must resolve through Adressevask.
func TestAVVaskFallback(t *testing.T) {
	const id = "70865c44-d570-44e7-a6f5-6f7c90add725"
	vask := fmt.Sprintf(`{
	  "vaskestatus":{"kode":1000,"tekst":"Eksakt match"},
	  "vaskeresultat":{"adresse_id_lokalid":%q,"adressebetegnelse":"Rådhuspladsen 1, 1550 København V","status":3},
	  "vaskeresultat_historisk":{"adressebetegnelse":"Gammel Plads 1, 1550 København V"}
	}`, id)

	const query = "Gammel Plads 1, 1550 København V"

	// No soeg query hits, forcing the chain all the way to /vask/.
	calls := avChainServer(t, nil,
		map[string]string{query: vask}, avVaskNoStreetInPost)

	addrs, err := AVFuzzySearch{Query: query}.Fetch()
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(addrs) != 1 {
		t.Fatalf("got %d addresses, want 1", len(addrs))
	}

	if n := avCountVask(*calls); n != 1 {
		t.Errorf("made %d /vask/ calls, want exactly 1 (the original query)", n)
	}
}

// TestAVVaskNedlagtRejected covers the explicit product rule: a decommissioned
// address is an error, not a result. The lookup must be skipped entirely —
// /adresser/{id} answers 404 for a nedlagt address, so continuing would replace
// a meaningful message with an opaque one.
func TestAVVaskNedlagtRejected(t *testing.T) {
	vask := `{
	  "vaskestatus":{"kode":1000,"tekst":"Eksakt match"},
	  "vaskeresultat":{"adresse_id_lokalid":"0a3f50c4-a051-32b8-e044-0003ba298018",
	                   "adressebetegnelse":"Vestergade 1A, 8000 Aarhus C","status":4},
	  "vaskeresultat_historisk":{"adressebetegnelse":"Vestergade 1, 8000 Aarhus C"}
	}`
	calls := avChainServer(t, nil,
		map[string]string{"Vestergade 1, 8000 Aarhus C": vask}, avVaskNoStreetInPost)

	_, err := AVFuzzySearch{Query: "Vestergade 1, 8000 Aarhus C"}.Fetch()
	if err == nil {
		t.Fatal("expected a nedlagt address to be rejected, got nil error")
	}
	if !strings.Contains(err.Error(), "nedlagt") {
		t.Errorf("error should name the cause, got %q", err)
	}

	for _, p := range *calls {
		if strings.HasPrefix(p, "/adresser/") && p != "/adresser/soeg" {
			t.Errorf("looked up a nedlagt address at %s; it should have been rejected first", p)
		}
	}
}

// TestAVStatusOnSearchPath covers the lifecycle rule on the search path, which
// previously had no status check at all. DAR exposes exactly 2, 3, 4 and 5 for
// an address, so all four are pinned: the two live ones resolve, and the two
// terminal ones are refused rather than valued as if they were there.
func TestAVStatusOnSearchPath(t *testing.T) {
	for status, wantErr := range map[string]bool{
		"2": false, // foreløbig — under construction, but real
		"3": false, // gældende
		"4": true,  // nedlagt — was real, since removed
		"5": true,  // henlagt — provisional, abandoned, never real
	} {
		t.Run("status "+status, func(t *testing.T) {
			avTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/adresser/soeg" {
					w.Write([]byte(`{"status":"ok","fund":[{"type":"adresse","id":"abc","titel":"Vestergade 1A"}]}`))
					return
				}
				w.Write([]byte(strings.Replace(avAddressFixture, `"status": "3"`, `"status": "`+status+`"`, 1)))
			})

			_, err := AVFuzzySearch{Query: "Vestergade 1A, 8000 Aarhus C"}.Fetch()
			if wantErr && err == nil {
				t.Fatalf("status %s should be rejected, got nil error", status)
			}
			if !wantErr && err != nil {
				t.Fatalf("status %s is a usable address, got error %v", status, err)
			}
		})
	}
}

// TestAVVaskHenlagtRejected pins the same rule on the vask path. Status 5 is
// reachable there in a way it is not through search: a henlagt address was
// never current, so it can carry a betegnelse that only Adressevask knows.
func TestAVVaskHenlagtRejected(t *testing.T) {
	const query = "Nyvej 7, 4000 Roskilde"
	vask := `{
	  "vaskestatus":{"kode":1000,"tekst":"Eksakt match"},
	  "vaskeresultat":{"adresse_id_lokalid":"9c1d",
	                   "adressebetegnelse":"Nyvej 7, 4000 Roskilde","status":5},
	  "vaskeresultat_historisk":{"adressebetegnelse":null}
	}`
	calls := avChainServer(t, nil, map[string]string{query: vask}, avVaskNoStreetInPost)

	_, err := AVFuzzySearch{Query: query}.Fetch()
	if err == nil {
		t.Fatal("expected a henlagt address to be rejected, got nil error")
	}
	if !strings.Contains(err.Error(), "henlagt") {
		t.Errorf("error should name the cause, got %q", err)
	}

	for _, p := range *calls {
		if strings.HasPrefix(p, "/adresser/") && p != "/adresser/soeg" {
			t.Errorf("looked up a henlagt address at %s; it should have been rejected first", p)
		}
	}
}

// TestAVVaskIntervalRejected covers the interval codes. Adressevask answers a
// range like "Christiansborg Slot 1-5" with a single boundary house number,
// which is a different property from the one asked about — returning it would
// attribute the neighbourhood's sales to whichever end of the range DAR happens
// to hold. The user is told to narrow the search instead.
func TestAVVaskIntervalRejected(t *testing.T) {
	for _, kode := range []int{800, 700} {
		t.Run(fmt.Sprintf("kode %d", kode), func(t *testing.T) {
			const query = "Christiansborg Slot 1-5, 1218 København K"
			vask := fmt.Sprintf(`{
			  "vaskestatus":{"kode":%d,"tekst":"husnummer i interval"},
			  "vaskeresultat":{"adresse_id_lokalid":"afc3",
			                   "adressebetegnelse":"Christiansborg Slot 1, 1218 København K","status":3},
			  "vaskeresultat_historisk":{"adressebetegnelse":null}
			}`, kode)
			calls := avChainServer(t, nil, map[string]string{query: vask}, avVaskNoStreetInPost)

			_, err := AVFuzzySearch{Query: query}.Fetch()
			if err == nil {
				t.Fatal("expected an interval match to be rejected, got nil error")
			}

			for _, p := range *calls {
				if strings.HasPrefix(p, "/adresser/") && p != "/adresser/soeg" {
					t.Errorf("looked up the interval boundary at %s instead of rejecting it", p)
				}
			}
		})
	}
}

// TestAVVaskVariantRetry is the combined #18/#19 case: an address that is both
// historical and typed without Danish characters. Search cannot see the
// historical name, and Adressevask does not recognise every ASCII spelling — so
// neither fallback resolves it alone, and the variants have to reach vask too.
func TestAVVaskVariantRetry(t *testing.T) {
	const id = "70865c44-d570-44e7-a6f5-6f7c90add725"
	vask := fmt.Sprintf(`{
	  "vaskestatus":{"kode":900,"tekst":"Vejnavn tilnærmet ift. stavevariationer"},
	  "vaskeresultat":{"adresse_id_lokalid":%q,
	                   "adressebetegnelse":"Nørrebrogade 155, 2200 København N","status":3},
	  "vaskeresultat_historisk":{"adressebetegnelse":"Nørrebrogade 155, 2200 Kobenhavn N"}
	}`, id)

	// Nothing hits search at all, and vask answers only the respelled street —
	// the ASCII original misses with the code that says the street was the
	// problem.
	calls := avChainServer(t, nil,
		map[string]string{"Nørrebrogade 155, 2200 Kobenhavn N": vask}, avVaskNoStreetInPost)

	addrs, err := AVFuzzySearch{Query: "Norrebrogade 155, 2200 Kobenhavn N"}.Fetch()
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(addrs) != 1 {
		t.Fatalf("got %d addresses, want 1", len(addrs))
	}

	// The original must be washed before any variant, so the common case stays
	// one request.
	if n := avCountVask(*calls); n < 2 {
		t.Errorf("only %d /vask/ calls; variants never reached vask", n)
	}
	if got := (*calls)[len(*calls)-2]; got != "/vask/?Nørrebrogade 155, 2200 Kobenhavn N" {
		t.Errorf("last vask call was %q, want the respelled street", got)
	}
}

// TestAVVaskVariantRetrySkipped guards the cost of the above. Only -800 ("street
// not in that postal district") can be fixed by respelling the street; every
// other failure is about the postnummer or the house number, so retrying
// variants would multiply requests on a query that cannot succeed.
func TestAVVaskVariantRetrySkipped(t *testing.T) {
	for _, kode := range []int{-300, -900, -1000} {
		t.Run(fmt.Sprintf("kode %d", kode), func(t *testing.T) {
			calls := avChainServer(t, nil, nil, kode)

			if _, err := (AVFuzzySearch{Query: "Norrebrogade 155, 2200 Kobenhavn N"}).Fetch(); err != nil {
				t.Fatalf("a miss should not error, got %v", err)
			}
			if n := avCountVask(*calls); n != 1 {
				t.Errorf("made %d /vask/ calls for kode %d, want 1 — variants cannot help here", n, kode)
			}
		})
	}
}

// TestAVChainExhausted confirms a genuine miss is still not an error: api.go
// renders an empty result as "no found address".
func TestAVChainExhausted(t *testing.T) {
	avChainServer(t, nil, nil, -1000)

	addrs, err := AVFuzzySearch{Query: "ikke en adresse overhovedet"}.Fetch()
	if err != nil {
		t.Fatalf("an exhausted chain should not error, got %v", err)
	}
	if len(addrs) != 0 {
		t.Fatalf("got %d addresses, want 0", len(addrs))
	}
}
