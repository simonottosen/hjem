package hjem

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm"
)

type bbrUnit struct{ usage, status string }

// fakeBBR answers BBR_Enhed queries from units, keyed by DAR address id, and
// records which ids each request asked about.
type fakeBBR struct {
	mu    sync.Mutex
	asked [][]string
	units map[string][]bbrUnit
	fail  bool
}

var bbrInList = regexp.MustCompile(`adresseIdentificerer: \{ in: \[([^\]]*)\]`)

func newFakeBBR(t *testing.T, units map[string][]bbrUnit) *fakeBBR {
	t.Helper()
	f := &fakeBBR{units: units}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Query string }
		json.NewDecoder(r.Body).Decode(&req)

		f.mu.Lock()
		defer f.mu.Unlock()
		if f.fail {
			fmt.Fprint(w, `{"errors":[{"message":"BBR is down"}]}`)
			return
		}
		var ids []string
		if m := bbrInList.FindStringSubmatch(req.Query); m != nil {
			for _, q := range strings.Split(m[1], ", ") {
				id, _ := strconv.Unquote(q)
				ids = append(ids, id)
			}
		}
		f.asked = append(f.asked, ids)

		var nodes []map[string]any
		for _, id := range ids {
			for _, u := range f.units[id] {
				var usage any
				if u.usage != "" {
					usage = u.usage
				}
				nodes = append(nodes, map[string]any{"adresseIdentificerer": id, "enh020EnhedensAnvendelse": usage, "status": u.status})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"BBR_Enhed": map[string]any{
			"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
			"nodes":    nodes,
		}}})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("DATAFORDELER_API_KEY", "test-key")
	t.Setenv("BBR_GRAPHQL_URL", srv.URL)
	return f
}

func (f *fakeBBR) requests() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.asked...)
}

// seedBBRAddrs stores one address per id and returns them as loaded rows.
func seedBBRAddrs(t *testing.T, ids ...string) (*gorm.DB, []*Address) {
	t.Helper()
	db := newTestDB(t)
	NewDawaCacher(db) // migrates addresses
	var addrs []*Address
	for i, id := range ids {
		a := &Address{
			DawaUUID: id, DawaID: fmt.Sprintf("Testvej %d, 2650 Hvidovre", i+1),
			StreetName: "Testvej", StreetNumber: strconv.Itoa(i + 1), PostalCode: "2650", MunicipalityCode: "0167",
		}
		if err := db.Create(a).Error; err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		addrs = append(addrs, a)
	}
	return db, addrs
}

func reloadAddrs(t *testing.T, db *gorm.DB) map[string]*Address {
	t.Helper()
	var rows []*Address
	if err := db.Find(&rows).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	out := map[string]*Address{}
	for _, a := range rows {
		out[a.DawaUUID] = a
	}
	return out
}

// The codes are the ones read off properties of known kind through the live
// API (120 Glimvej, 131 Firhøj, 140 Flensborggade, 510 Rosenvænget), with the
// retired units BBR returned beside two of them.
func TestBBRKindFollowsUnitUsage(t *testing.T) {
	cases := map[string]struct {
		units []bbrUnit
		want  PropertyType
	}{
		"flat":                 {[]bbrUnit{{"140", "6"}}, PropertyApartment},
		"flat with old unit":   {[]bbrUnit{{"", "9"}, {"140", "6"}}, PropertyApartment},
		"house rebuilt":        {[]bbrUnit{{"120", "6"}, {"120", "10"}}, PropertyHouse},
		"terraced":             {[]bbrUnit{{"131", "6"}}, PropertySharedHouse},
		"summer house":         {[]bbrUnit{{"510", "6"}}, PropertyVacation},
		"flat above a shop":    {[]bbrUnit{{"320", "6"}, {"140", "6"}}, 0},
		"house and a flat":     {[]bbrUnit{{"120", "6"}, {"140", "6"}}, 0},
		"farm house":           {[]bbrUnit{{"110", "6"}}, 0},
		"current without code": {[]bbrUnit{{"", "6"}, {"140", "6"}}, 0},
		"no units in BBR":      {nil, 0},
		"only retired units":   {[]bbrUnit{{"140", "10"}}, 0},
	}

	units := map[string][]bbrUnit{}
	var ids []string
	for name, c := range cases {
		units[name] = c.units
		ids = append(ids, name)
	}
	newFakeBBR(t, units)
	db, addrs := seedBBRAddrs(t, ids...)

	if err := (&bbrCacher{db: db}).FillKinds(addrs); err != nil {
		t.Fatalf("FillKinds: %v", err)
	}

	stored := reloadAddrs(t, db)
	for name, c := range cases {
		a := stored[name]
		if a.BBRPropertyKind != c.want {
			t.Errorf("%s: stored kind %d, want %d", name, a.BBRPropertyKind, c.want)
		}
		// Unknown is an answer too: without the stamp every lookup would ask
		// again about the addresses BBR cannot type.
		if a.BBRCheckedAt.IsZero() {
			t.Errorf("%s: not marked as checked", name)
		}
	}
}

// A type from a matched Boliga sale is what Boliga's own listings call the
// property, so it is kept and BBR is not asked. Nor is an address asked twice:
// not within one lookup, where nested ranges hand the same row over twice, and
// not on the next lookup, which loads the stored answer — including the
// answer "BBR cannot type this one", which would otherwise be asked again on
// every lookup that covers it.
func TestBBRAsksOnlyForUnknownTypesAndOnlyOnce(t *testing.T) {
	f := newFakeBBR(t, map[string][]bbrUnit{
		"sold-flat":  {{"120", "6"}}, // BBR disagrees; Boliga's answer stands
		"never-sold": {{"140", "6"}},
		"shop":       {{"320", "6"}},
	})
	db, addrs := seedBBRAddrs(t, "sold-flat", "never-sold", "shop")
	addrs[0].BoligaPropertyKind = PropertyApartment
	db.Save(addrs[0])
	twin := *addrs[1]

	c := &bbrCacher{db: db}
	if err := c.FillKinds(append(addrs, &twin)); err != nil {
		t.Fatalf("FillKinds: %v", err)
	}

	if reqs := f.requests(); len(reqs) != 1 || strings.Join(reqs[0], ",") != "never-sold,shop" {
		t.Fatalf("asked BBR %v, want one request for never-sold and shop only", reqs)
	}
	if addrs[0].PropertyKind() != PropertyApartment {
		t.Errorf("Boliga's type was replaced: got %d", addrs[0].PropertyKind())
	}
	if addrs[1].PropertyKind() != PropertyApartment || twin.PropertyKind() != PropertyApartment {
		t.Errorf("the two copies of never-sold got %d and %d, want %d", addrs[1].PropertyKind(), twin.PropertyKind(), PropertyApartment)
	}

	stored := reloadAddrs(t, db)
	if err := c.FillKinds([]*Address{stored["sold-flat"], stored["never-sold"], stored["shop"]}); err != nil {
		t.Fatalf("second FillKinds: %v", err)
	}
	if reqs := f.requests(); len(reqs) != 1 {
		t.Errorf("the next lookup asked BBR again: %v", reqs)
	}
}

// A failed BBR request leaves nothing marked as checked, so the next lookup
// asks again instead of filtering on "unknown" for a year.
func TestBBRFailureMarksNothingChecked(t *testing.T) {
	f := newFakeBBR(t, map[string][]bbrUnit{"flat": {{"140", "6"}}})
	f.fail = true
	db, addrs := seedBBRAddrs(t, "flat")

	if err := (&bbrCacher{db: db}).FillKinds(addrs); err == nil {
		t.Fatal("FillKinds reported success while BBR was failing")
	}
	if a := reloadAddrs(t, db)["flat"]; !a.BBRCheckedAt.IsZero() || a.BBRPropertyKind != 0 {
		t.Errorf("stored kind %d, checked %v after a failed request; want neither", a.BBRPropertyKind, a.BBRCheckedAt)
	}
}

// The point of the change: a never-sold flat, typed by BBR, is no longer
// compared against houses — and a neighbour whose type nobody knows is still
// kept rather than guessed at.
func TestFilterUsesBBRTypeWhenNoSaleGaveOne(t *testing.T) {
	primary := &Address{DawaID: "primary", BBRPropertyKind: PropertyApartment}
	house := &Address{DawaID: "house", BBRPropertyKind: PropertyHouse}
	soldFlat := &Address{DawaID: "sold flat", BoligaPropertyKind: PropertyApartment}
	bbrFlat := &Address{DawaID: "bbr flat", BBRPropertyKind: PropertyApartment}
	unknown := &Address{DawaID: "unknown"}
	addrs := []*Address{primary, house, soldFlat, bbrFlat, unknown}
	sales := make([][]Sale, len(addrs))

	kept, _ := FilterAddressesByProperty(primary.PropertyKind(), addrs, sales)

	var got []string
	for _, a := range kept {
		got = append(got, a.DawaID)
	}
	if want := "primary,sold flat,bbr flat,unknown"; strings.Join(got, ",") != want {
		t.Errorf("kept %v, want %s", got, want)
	}
}

// BBR is asked only about the addresses the filter decides on: the searched
// property, and neighbours with sales. The rest are dropped from the response
// whatever their type, and a dense radius holds thousands of them.
func TestLookupAsksBBROnlyAboutPropertiesThatMatter(t *testing.T) {
	f := newFakeBBR(t, map[string][]bbrUnit{"primary": {{"140", "6"}}, "sold": {{"140", "6"}}})
	db, addrs := seedBBRAddrs(t, "primary", "sold", "never-sold")
	s := &server{bbr: &bbrCacher{db: db}}

	s.fillPropertyKinds(addrs, [][]Sale{nil, {testSale(3_000_000, 2025)}, nil})

	if reqs := f.requests(); len(reqs) != 1 || strings.Join(reqs[0], ",") != "primary,sold" {
		t.Errorf("asked BBR %v, want one request for primary and sold", reqs)
	}
}
