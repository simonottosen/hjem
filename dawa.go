package hjem

// dawa.go keeps the DAWA name but no longer talks to DAWA — the service shut
// down on 1 Oct 2026, and address lookups now go to Adressevælgeren and
// Datafordeleren. The Dawa* names stay because renaming them would rewrite the
// database schema and the JSON wire format for no functional gain.

import (
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

var (
	ErrNotFoundAddr = errors.New("Unable to find address")
)

type Address struct {
	ID               uint    `json:"-" gorm:"primaryKey"`
	DawaUUID         string  `json:"dawa_uuid" gorm:"not null;uniqueIndex:idx_addresses_dawa_uuid,where:dawa_uuid <> ''"`
	DawaID           string  `json:"full_txt" gorm:"not null;unique"`
	StreetName       string  `json:"street_name" gorm:"not null"`
	StreetNumber     string  `json:"street_number" gorm:"not null"`
	Floor            *string `json:"floor"`
	Door             *string `json:"door"`
	PostalCode       string  `json:"zipcode" gorm:"not null"`
	MunicipalityCode string  `json:"municipality_code" gorm:"not null"`
	Latitude         float64 `json:"lat" gorm:"not null"`
	Longtitude       float64 `json:"long" gorm:"not null"`

	BoligaCollectedAt         time.Time    `json:"-"`
	BoligaPropertyKind        PropertyType `json:"-"`
	BoligaBuildingSize        int          `json:"building_size"`
	BoligaPropertySize        int          `json:"property_size"`
	BoligaBasementSize        int          `json:"basement_size"`
	BoligaRooms               int          `json:"rooms"`
	BoligaBuiltYear           int          `json:"built_year"`
	BoligaMonthlyOwnerExpense int          `json:"monthly_owner_expense_dkk"`
	BoligaEnergyMarking       string       `json:"energy_marking"`
}

func (addr Address) Short() string {
	s := fmt.Sprintf("%s %s", addr.StreetName, addr.StreetNumber)
	if addr.Floor != nil {
		s += fmt.Sprintf(", %s.", *addr.Floor)
	}

	if addr.Door != nil {
		s += fmt.Sprintf(" %s", *addr.Door)
	}

	return s
}

func (a Address) ToSlice() []string {
	var door string
	if a.Door != nil {
		door = *a.Door
	}
	var floor string
	if a.Floor != nil {
		floor = *a.Floor
	}

	return []string{
		a.DawaID,
		a.StreetName,
		a.StreetNumber,
		door,
		floor,
		a.PostalCode,
		strconv.Itoa(a.BoligaBuildingSize),
		strconv.Itoa(a.BoligaPropertySize),
		strconv.Itoa(a.BoligaBasementSize),
		strconv.Itoa(a.BoligaRooms),
		strconv.Itoa(a.BoligaBuiltYear),
		strconv.Itoa(a.BoligaMonthlyOwnerExpense),
	}
}

func (a Address) Headers() []string {
	return []string{
		"id",
		"street_name",
		"street_number",
		"door",
		"floor",
		"postal_code",
		"size",
		"property_size",
		"basement_size",
		"rooms",
		"built_year",
		"monthly_owner_expense_dkk",
	}
}

type dawaCacher struct {
	db        *gorm.DB
	maxAmount float64
}

type DawaQueryCache struct {
	Query     string `gorm:"primaryKey"`
	IDs       string `gorm:"not null"`
	CreatedAt time.Time
}

func NewDawaQueryCacheFromAddrs(req DawaRequest, addrs []*Address) DawaQueryCache {
	reqStr := fmt.Sprintf("%s", req.Request().URL)

	ids := make([]string, len(addrs))
	for i := 0; i < len(addrs); i++ {
		ids[i] = strconv.Itoa(int(addrs[i].ID))
	}

	return DawaQueryCache{
		Query: reqStr,
		IDs:   strings.Join(ids, ","),
	}
}

func (dqc DawaQueryCache) Identifiers() []int {
	ids := strings.Split(dqc.IDs, ",")

	uids := make([]int, len(ids))
	for i := 0; i < len(ids); i++ {
		id, _ := strconv.Atoi(ids[i])
		uids[i] = id
	}

	return uids
}

type DawaCacher interface {
	Do(DawaRequest) ([]*Address, error)
}

func NewDawaCacher(db *gorm.DB) *dawaCacher {
	db.AutoMigrate(&DawaQueryCache{})
	// The unique index on dawa_uuid is partial (`where dawa_uuid <> ''`) because
	// rows written before that column was populated all carry an empty string,
	// and a plain unique index would reject every deployment that has any.
	//
	// It still cannot be created on a database that already holds the duplicates
	// this index exists to prevent, which is every deployment that ran the old
	// DawaID keying. A failed migration is therefore the signal to collapse
	// them: doing it only on failure keeps the scan off the boot path of a
	// database that does not need it, and makes a fresh one — where the table
	// does not exist yet to be scanned — the ordinary case.
	if err := db.AutoMigrate(&Address{}); err != nil {
		log.Printf("Address migration incomplete: %v", err)

		kept, removed, cerr := collapseDuplicateUUIDs(db)
		switch {
		case cerr != nil:
			log.Printf("  Collapsing duplicate dawa_uuid rows failed: %v", cerr)
			log.Printf("  List them with: SELECT dawa_uuid, COUNT(*) c FROM addresses WHERE dawa_uuid <> '' GROUP BY dawa_uuid HAVING c > 1;")
		case removed == 0:
			log.Printf("  No duplicate dawa_uuid rows found; the failure is something else.")
		default:
			log.Printf("  Collapsed %d duplicate address row(s) onto %d surviving address(es).", removed, kept)
			if err := db.AutoMigrate(&Address{}); err != nil {
				log.Printf("  Address migration still incomplete: %v", err)
			}
		}
	}

	return &dawaCacher{
		maxAmount: 50.0,
		db:        db,
	}
}

// collapseBatch bounds the ids put into one IN clause, in line with the limits
// the DAR and Adressevælger batches use.
const collapseBatch = 200

// collapseDuplicateUUIDs merges address rows that describe the same DAR
// address onto a single row, and reports how many it kept and how many it
// removed.
//
// The rows exist because dedupe used to key on DawaID, a *formatted* address
// string: DAR's `adressebetegnelse` and DAWA's `betegnelse` disagree over a
// comma or a floor abbreviation, so the same physical address was inserted
// twice. Keying on DawaUUID stopped new ones appearing, but the rows already
// written stayed, and they are what the unique index trips over.
//
// Leaving them is not free. A duplicate is a second copy of an address that
// every lookup ignores, so whatever Boliga cached against it is refetched
// against the copy that is used — paying the rate limit twice for one address
// — and the index that would stop the next one from appearing cannot be
// created while they are there.
func collapseDuplicateUUIDs(db *gorm.DB) (kept, removed int, err error) {
	var dupUUIDs []string
	if err := db.Model(&Address{}).
		Where("dawa_uuid <> ''").
		Group("dawa_uuid").
		Having("COUNT(*) > 1").
		Pluck("dawa_uuid", &dupUUIDs).Error; err != nil {
		return 0, 0, err
	}
	if len(dupUUIDs) == 0 {
		return 0, 0, nil
	}

	// Announced before the work rather than after it: everything below runs on
	// the boot path, inside one transaction, and a server that is rewriting a
	// year of cached queries should not look like a server that has hung.
	log.Printf("  Collapsing %d duplicated dawa_uuid(s)...", len(dupUUIDs))

	// One transaction, so a database that fails partway through is left with
	// the duplicates it started with rather than with sales pointing at
	// addresses that no longer exist.
	err = db.Transaction(func(tx *gorm.DB) error {
		// Keyed on the discarded row's id, because the query cache stores row
		// ids and has to be rewritten after the rows themselves are gone.
		remap := map[uint]uint{}

		for _, uuids := range chunk(dupUUIDs, collapseBatch) {
			var rows []*Address
			if err := tx.Where("dawa_uuid IN ?", uuids).Order("dawa_uuid, id").Find(&rows).Error; err != nil {
				return err
			}

			for _, group := range groupByUUID(rows) {
				// The oldest copy survives because that is the one
				// safeCreateOrGetAddrs already resolves to, so collapsing
				// does not change which row a search returns.
				survivor, losers := group[0], group[1:]

				if err := adoptSales(tx, survivor, losers); err != nil {
					return err
				}
				for _, l := range losers {
					remap[l.ID] = survivor.ID
				}
				kept++
			}
		}

		if err := remapCachedQueryIDs(tx, remap); err != nil {
			return err
		}

		for _, ids := range chunk(keysOf(remap), collapseBatch) {
			// Any sales adopted above now carry the survivor's id, so they do
			// not match here and outlive the rows they were cached against.
			if err := tx.Where("addr_id IN ?", ids).Delete(&Sale{}).Error; err != nil {
				return err
			}
			res := tx.Where("id IN ?", ids).Delete(&Address{})
			if res.Error != nil {
				return res.Error
			}
			removed += int(res.RowsAffected)
		}

		return nil
	})
	if err != nil {
		return 0, 0, err
	}

	return kept, removed, nil
}

// adoptSales moves a discarded copy's cached Boliga data onto the row that
// survives, but only when the survivor has none of its own. Two warm copies
// cannot be merged: the sales are the same street's, so re-pointing them would
// list every sale under the address twice and weight it double in the comps.
func adoptSales(tx *gorm.DB, survivor *Address, losers []*Address) error {
	if !survivor.BoligaCollectedAt.IsZero() {
		return nil
	}
	donor := newestCollected(losers)
	if donor == nil {
		return nil
	}

	if err := tx.Model(&Sale{}).Where("addr_id = ?", donor.ID).
		Update("addr_id", survivor.ID).Error; err != nil {
		return err
	}
	adoptBoligaData(survivor, donor)

	return tx.Save(survivor).Error
}

// remapCachedQueryIDs rewrites the cached address lists to name the surviving
// rows. DawaQueryCache stores row ids and holds them for a year, and a lookup
// that reads it loads them with Find, which drops ids that no longer exist
// without complaining. Skipping this would quietly shrink every cached radius
// result that happened to include a duplicate, for the rest of its TTL.
func remapCachedQueryIDs(tx *gorm.DB, remap map[uint]uint) error {
	// Paged rather than read whole: a radius query in a dense area caches ten
	// thousand ids, and there is one row per distinct query made in the last
	// year. Paging on the primary key rather than an offset because rewriting
	// does not change it, so each page starts where the last one ended instead
	// of re-walking everything before it.
	const page = 50
	var last string
	for {
		var caches []DawaQueryCache
		if err := tx.Where("query > ?", last).Order("query").Limit(page).Find(&caches).Error; err != nil {
			return err
		}
		if len(caches) == 0 {
			return nil
		}
		last = caches[len(caches)-1].Query

		for _, c := range caches {
			ids, changed := remapIDList(c.IDs, remap)
			if !changed {
				continue
			}
			if err := tx.Model(&DawaQueryCache{}).Where("query = ?", c.Query).
				Update("ids", ids).Error; err != nil {
				return err
			}
		}
	}
}

// remapIDList rewrites one cached id list. It parses the list itself instead
// of going through DawaQueryCache.Identifiers, which maps anything unparseable
// to 0 — including the single empty field an empty IDs string splits into. A
// rewrite has to put back exactly what it did not recognise.
func remapIDList(list string, remap map[uint]uint) (string, bool) {
	var changed bool
	parts := strings.Split(list, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, p := range parts {
		if n, err := strconv.Atoi(p); err == nil {
			if survivor, ok := remap[uint(n)]; ok {
				p = strconv.Itoa(int(survivor))
				changed = true
			}
		}
		// A cached list that named both copies of an address now names the
		// survivor twice, which would weight it double in the valuation.
		if seen[p] {
			changed = true
			continue
		}
		seen[p] = true
		out = append(out, p)
	}

	return strings.Join(out, ","), changed
}

// groupByUUID splits rows already ordered by (dawa_uuid, id) into one slice
// per uuid, oldest row first.
func groupByUUID(rows []*Address) [][]*Address {
	var groups [][]*Address
	for i, row := range rows {
		if i > 0 && row.DawaUUID == rows[i-1].DawaUUID {
			groups[len(groups)-1] = append(groups[len(groups)-1], row)
			continue
		}
		groups = append(groups, []*Address{row})
	}

	return groups
}

func newestCollected(addrs []*Address) *Address {
	var newest *Address
	for _, a := range addrs {
		if a.BoligaCollectedAt.IsZero() {
			continue
		}
		if newest == nil || a.BoligaCollectedAt.After(newest.BoligaCollectedAt) {
			newest = a
		}
	}

	return newest
}

// adoptBoligaData copies the scraped fields and nothing else — the survivor
// keeps its own identity and its own row id. The list is manual, so a tenth
// Boliga* column on Address has to be added here too or it is silently lost
// whenever a collapse adopts a cache.
func adoptBoligaData(dst, src *Address) {
	dst.BoligaCollectedAt = src.BoligaCollectedAt
	dst.BoligaPropertyKind = src.BoligaPropertyKind
	dst.BoligaBuildingSize = src.BoligaBuildingSize
	dst.BoligaPropertySize = src.BoligaPropertySize
	dst.BoligaBasementSize = src.BoligaBasementSize
	dst.BoligaRooms = src.BoligaRooms
	dst.BoligaBuiltYear = src.BoligaBuiltYear
	dst.BoligaMonthlyOwnerExpense = src.BoligaMonthlyOwnerExpense
	dst.BoligaEnergyMarking = src.BoligaEnergyMarking
}

func (c dawaCacher) Do(req DawaRequest) ([]*Address, error) {
	reqStr := fmt.Sprintf("%s", req.Request().URL)

	var cache DawaQueryCache
	var performRequest bool
	if err := c.db.First(&cache, "query = ?", reqStr).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		performRequest = true
	}

	if time.Now().Sub(cache.CreatedAt) >= req.MaxAge() {
		performRequest = true
	}

	// Empty entries are no longer written (see below), but deployments carry
	// ones from before that change — including queries cached as "not found"
	// before the address fallback chain existed, which would now resolve.
	// Treating them as misses lets them self-heal instead of sitting out the
	// remainder of the request's maximum age.
	if cache.Query != "" && cache.IDs == "" {
		performRequest = true
	}

	if performRequest {
		if cache.Query != "" {
			if err := c.db.Delete(&cache).Error; err != nil {
				return nil, err
			}
		}

		addrs, err := req.Fetch()
		if err != nil {
			// Never cache a failed fetch. The entry would be empty and would be
			// served for the request's full MaxAge, turning a transient outage
			// into a permanent "no results" for that query.
			return nil, err
		}

		// Never cache an empty result either. "Not found" is not a durable fact
		// the way a resolved address is: a newly registered address appears in
		// the index later, and the resolution chain itself gains fallbacks over
		// time. Caching nothing for the full MaxAge would pin that answer for
		// months, and because the key is the request URL the user cannot force a
		// retry except by retyping the query differently. Re-resolving a miss
		// costs a handful of requests; being wrong for that long does not expire.
		if len(addrs) == 0 {
			return nil, nil
		}

		if err := c.safeCreateOrGetAddrs(addrs); err != nil {
			return nil, err
		}

		cache := NewDawaQueryCacheFromAddrs(req, addrs)
		if err := c.db.Create(&cache).Error; err != nil {
			return nil, err
		}

		return addrs, nil
	}

	var addrs []*Address
	ids := cache.Identifiers()
	r := int(math.Ceil(float64(len(ids)) / c.maxAmount))
	for i := 0; i < r; i++ {
		var tempAddrs []*Address
		start, end := int(c.maxAmount)*i, int(c.maxAmount)*(i+1)
		end = int(math.Min(float64(len(ids)), float64(end)))
		if err := c.db.Find(&tempAddrs, ids[start:end]).Error; err != nil {
			return nil, err
		}

		addrs = append(addrs, tempAddrs...)
	}

	return addrs, nil
}

func (c dawaCacher) safeCreateOrGetAddrs(addrs []*Address) error {
	n := float64(len(addrs))
	r := int(math.Ceil(n / c.maxAmount))

	// DawaUUID is the key, because DawaID is a formatted address string: the
	// smallest disagreement between DAR and DAWA over a comma or a floor
	// abbreviation made the same physical address look new and get inserted
	// twice. DawaID is only still matched for the rows that predate the column.
	byUUID := map[string]*Address{}
	byID := map[string]*Address{}
	for i := 0; i < r; i++ {
		start, end := int(c.maxAmount)*i, int(c.maxAmount)*(i+1)
		end = int(math.Min(n, float64(end)))

		var tempAddrs []*Address
		uuids := make([]string, 0, end-start)
		ids := make([]string, end-start)
		for j, a := range addrs[start:end] {
			ids[j] = a.DawaID
			if a.DawaUUID != "" {
				uuids = append(uuids, a.DawaUUID)
			}
		}

		if err := c.db.Where("dawa_uuid IN ? OR dawa_id IN ?", uuids, ids).Order("id").Find(&tempAddrs).Error; err != nil {
			return err
		}

		for j, _ := range tempAddrs {
			a := tempAddrs[j]
			if a.DawaUUID == "" {
				byID[a.DawaID] = a
				continue
			}

			// Ordered by ID, so a duplicate pair left behind by the old keying
			// resolves to its oldest copy on every lookup instead of flapping.
			if _, dup := byUUID[a.DawaUUID]; !dup {
				byUUID[a.DawaUUID] = a
			}
		}
	}

	var createAddrs []*Address
	for i, _ := range addrs {
		a := addrs[i]
		// byUUID never holds the empty key, so an address without a UUID falls
		// through to the pre-UUID rows on its own.
		exsts, ok := byUUID[a.DawaUUID]
		if !ok {
			exsts, ok = byID[a.DawaID]
		}
		if !ok {
			createAddrs = append(createAddrs, a)
			continue
		}

		// Backfill DawaUUID if the cached record is missing it
		if exsts.DawaUUID == "" && a.DawaUUID != "" {
			exsts.DawaUUID = a.DawaUUID
			c.db.Save(exsts)
		}

		addrs[i] = exsts
	}

	if err := c.db.CreateInBatches(&createAddrs, int(c.maxAmount)).Error; err != nil {
		return err
	}

	return nil
}

type DawaRequest interface {
	Request() *http.Request
	MaxAge() time.Duration
	Fetch() ([]*Address, error)
}
