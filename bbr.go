package hjem

import (
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"
)

// The comparables filter compares property types, and until now the only
// source of a type was a matched Boliga sale. A property that has never sold
// had none, so the filter skipped itself and valued an apartment against houses
// and apartments alike — on exactly the properties a valuation tool is most
// needed for. DAR carries no usage information at all (no anvendelse on
// DAR_Adresse or DAR_Husnummer, and no building or unit types on its
// endpoint), so the type comes from BBR, the building register, which the same
// Datafordeler API key reaches. Override the endpoint with BBR_GRAPHQL_URL.
const bbrGraphQLDefaultURL = "https://graphql.datafordeler.dk/BBR/v2"

// bbrRecheck is how long a BBR answer is trusted, the same year the address
// cache keeps a resolved address. A unit's usage changes on conversion or
// rebuild, not between searches, and an address BBR knew nothing about is
// rechecked when the year is up rather than on every lookup.
const bbrRecheck = 365 * 24 * time.Hour

// bbrQueryEnheder finds the units registered at a set of DAR addresses. BBR's
// adresseIdentificerer is DAR's address id — the DawaUUID every address
// already carries — so no further lookup is needed to make the join.
const bbrQueryEnheder = `query Enheder($after: String) {
  BBR_Enhed(first: %d, after: $after, registreringstid: "%s", virkningstid: "%s", where: { adresseIdentificerer: { in: [%s] } }) {
    pageInfo { hasNextPage endCursor }
    nodes { adresseIdentificerer enh020EnhedensAnvendelse status }
  }
}`

type bbrEnhedNode struct {
	Adresse string  `json:"adresseIdentificerer"`
	Usage   flexStr `json:"enh020EnhedensAnvendelse"`
	Status  flexStr `json:"status"`
}

type bbrEnhedResp struct {
	Enhed darConnection[bbrEnhedNode] `json:"BBR_Enhed"`
}

// bbrUsageKinds maps a unit's usage code (enh020) onto Boliga's four kinds.
// 120, 131, 140 and 510 were checked against properties of known kind through
// the live API; 121, 130 and 132 are the same terraced/semi-detached family in
// Danmarks Statistik's code list (ENH ANVENDELSEKODE). Everything else — farm
// houses, "tæt-lav" detached houses, institutions, commercial units — is left
// unknown on purpose: an unknown type keeps a property unfiltered, which is no
// worse than before, whereas a wrong one would filter out good comparables.
var bbrUsageKinds = map[string]PropertyType{
	"120": PropertyHouse,       // fritliggende enfamiliehus
	"121": PropertySharedHouse, // sammenbygget enfamiliehus
	"130": PropertySharedHouse, // række-, kæde- eller dobbelthus
	"131": PropertySharedHouse, // række- og kædehus
	"132": PropertySharedHouse, // dobbelthus
	"140": PropertyApartment,   // etagebolig
	"510": PropertyVacation,    // sommerhus
}

// bbrEndedStatus are the lifecycle statuses of units that no longer exist as
// registered. BBR returns them alongside the current unit — seen as a
// code-less unit with status 9 behind a flat, and a since-replaced house
// with status 10 — and they must not make an address look mixed-use.
var bbrEndedStatus = map[flexStr]bool{"9": true, "10": true, "11": true}

type bbrCacher struct {
	db *gorm.DB
}

// FillKinds gives every address that has no property type yet — none from a
// matched Boliga sale, and BBR not asked within bbrRecheck — the type BBR
// records for its units, and stores the answer so the next lookup need not ask
// again. A type from Boliga is never replaced: Address.PropertyKind prefers it.
//
// Several pointers can share one address row (nested ranges load each range
// separately), so every pointer is updated and each row written once.
func (c *bbrCacher) FillKinds(addrs []*Address) error {
	now := time.Now()

	var ids []string
	asked := map[string]bool{}
	for _, a := range addrs {
		if a.PropertyKind() != 0 || a.DawaUUID == "" || asked[a.DawaUUID] || now.Sub(a.BBRCheckedAt) < bbrRecheck {
			continue
		}
		asked[a.DawaUUID] = true
		ids = append(ids, a.DawaUUID)
	}
	if len(ids) == 0 {
		return nil
	}

	kinds, err := fetchBBRKinds(ids)
	if err != nil {
		return err
	}

	rows := map[PropertyType][]uint{}
	written := map[uint]bool{}
	var known int
	for _, a := range addrs {
		if !asked[a.DawaUUID] {
			continue
		}
		a.BBRPropertyKind, a.BBRCheckedAt = kinds[a.DawaUUID], now
		if !written[a.ID] {
			written[a.ID] = true
			rows[a.BBRPropertyKind] = append(rows[a.BBRPropertyKind], a.ID)
			if a.BBRPropertyKind != 0 {
				known++
			}
		}
	}
	log.Printf("BBR property types: %d of %d addresses known", known, len(written))

	// Only the two BBR columns, never the whole row: other lookups write the
	// same addresses concurrently, and a full-row write from this lookup's
	// copies would put back whatever they changed in between.
	for kind, ids := range rows {
		for _, batch := range chunk(ids, collapseBatch) {
			err := c.db.Model(&Address{}).Where("id IN ?", batch).
				Select("BBRPropertyKind", "BBRCheckedAt").
				Updates(&Address{BBRPropertyKind: kind, BBRCheckedAt: now}).Error
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// fetchBBRKinds returns the kind BBR implies for each address id that has one.
// An address is given a kind only if every unit still registered there has a
// usage code and all of them map to the same kind; a building mixing a flat
// with a shop, or with a code outside bbrUsageKinds, stays unknown.
func fetchBBRKinds(ids []string) (map[string]PropertyType, error) {
	endpoint, err := datafordelerEndpoint("BBR_GRAPHQL_URL", bbrGraphQLDefaultURL)
	if err != nil {
		return nil, err
	}
	// Bitemporal like DAR: the same "now" for both times selects the units as
	// currently registered and currently in effect.
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000000") + "Z"

	kinds := map[string]PropertyType{}
	mixed := map[string]bool{}
	for _, batch := range chunk(ids, darInLimit) {
		nodes, err := darPostAll(endpoint,
			fmt.Sprintf(bbrQueryEnheder, darMaxNodes, now, now, gqlIDList(batch)),
			func(r *bbrEnhedResp) *darConnection[bbrEnhedNode] { return &r.Enhed })
		if err != nil {
			return nil, fmt.Errorf("BBR units: %w", err)
		}
		for _, n := range nodes {
			if bbrEndedStatus[n.Status] || mixed[n.Adresse] {
				continue
			}
			k := bbrUsageKinds[string(n.Usage)]
			if prev, seen := kinds[n.Adresse]; k == 0 || (seen && prev != k) {
				mixed[n.Adresse] = true
				delete(kinds, n.Adresse)
				continue
			}
			kinds[n.Adresse] = k
		}
	}
	return kinds, nil
}
