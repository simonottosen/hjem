// Command dar-usage-probe answers one open question: does Datafordeleren's DAR
// carry anything describing what a property *is* — house, apartment, terraced —
// or does that live only in a separate register such as BBR?
//
// It matters because the comparables filter can only exclude mismatched
// property types for addresses that have already sold: the type is read off a
// matched Boliga sale, so a never-sold property gets compared against houses and
// apartments alike. Sourcing the type from DAR instead would fix that, but the
// three DAR queries the app runs select only ids, positions and designations,
// and nobody has seen the rest of the schema.
//
// The probe deliberately guesses no field names. It asks the server for its own
// schema, then dumps *every* argument-free scalar field of a real address
// record, so a usage field appears in the output with its actual code value
// instead of being inferred. The codes are the point: Boliga encodes exactly
// four kinds (house, shared house, apartment, vacation), and whether DAR's
// categories can be mapped onto those four is only decidable by looking at the
// values, not the field names.
//
//	DATAFORDELER_API_KEY=xxxx go run ./cmd/dar-usage-probe -addr "Rådhuspladsen 1, 1550 København V"
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	hjem "github.com/tpanum/hjem"
)

// usageWords are the Danish stems a building- or unit-usage field would plausibly
// be named after. Deliberately narrow: "type" and "art" appear all over a schema
// this size and would bury the signal under bitemporal and status plumbing.
var usageWords = []string{"anvendelse", "benyttelse", "bygning", "enhed", "bolig", "bbr", "ejendom", "ejerlejlighed"}

// walkedTypes are the entities the app already queries per address. A usage
// field on one of these is a one-line change; a usage field anywhere else means
// at least another round-trip, and possibly another register.
var walkedTypes = []string{"DAR_Adresse", "DAR_Husnummer"}

// GraphQL introspection returns a field's type as a chain of wrappers
// (NON_NULL → LIST → NON_NULL → named), and the chain has to be unrolled by
// hand in the query because there is no recursion in GraphQL. Four levels
// covers every shape DAR could serve for a scalar.
const typeRefSelection = `kind name ofType { kind name ofType { kind name ofType { kind name } } }`

func main() {
	addr := flag.String("addr", "Rådhuspladsen 1, 1550 København V", "address to inspect")
	flag.Parse()

	if os.Getenv("DATAFORDELER_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "DATAFORDELER_API_KEY is not set — this probe talks to the live Datafordeler DAR API and cannot run without a key.")
		fmt.Fprintln(os.Stderr, "Usage: DATAFORDELER_API_KEY=xxxx go run ./cmd/dar-usage-probe -addr \"Rådhuspladsen 1, 1550 København V\"")
		os.Exit(1)
	}

	sch, err := introspect()
	if err != nil {
		fmt.Printf("=== 1. SCHEMA INTROSPECTION ===\n\nfailed: %v\n\n", err)
		fmt.Println("Introspection appears to be disabled on this endpoint. Falling back to named guesses:")
		fmt.Println("GraphQL validates a query before executing it, so \"Cannot query field X on type Y\"")
		fmt.Println("proves absence even though the query would fail later for other reasons.")
		guess()
		fmt.Println("\n=== INTERPRETATION ===")
		fmt.Println("Only the guesses above are evidence. Any guess that did NOT produce a")
		fmt.Println("\"Cannot query field\" error names a real field — chase it from there.")
		return
	}
	hits := usageFields(sch)
	reportSchema(sch, hits)
	walk(sch, *addr)
	interpret(hits)
}

// ---------------------------------------------------------------------------
// Introspection
// ---------------------------------------------------------------------------

type typeRef struct {
	Kind   string   `json:"kind"`
	Name   string   `json:"name"`
	OfType *typeRef `json:"ofType"`
}

// base unwraps NON_NULL/LIST wrappers down to the named type.
func (t typeRef) base() typeRef {
	for t.OfType != nil {
		t = *t.OfType
	}
	return t
}

func (t typeRef) String() string {
	b := t.base()
	return b.Name + " (" + b.Kind + ")"
}

type schemaField struct {
	Name string                  `json:"name"`
	Args []struct{ Name string } `json:"args"`
	Type typeRef                 `json:"type"`
}

type schemaType struct {
	Name   string        `json:"name"`
	Fields []schemaField `json:"fields"`
}

type schema struct {
	Schema struct {
		QueryType struct{ Fields []schemaField } `json:"queryType"`
		Types     []schemaType                   `json:"types"`
	} `json:"__schema"`
}

func (s *schema) typeByName(name string) *schemaType {
	for i := range s.Schema.Types {
		if s.Schema.Types[i].Name == name {
			return &s.Schema.Types[i]
		}
	}
	return nil
}

func introspect() (*schema, error) {
	q := fmt.Sprintf(`query {
  __schema {
    queryType { fields { name } }
    types { name fields { name args { name } type { %s } } }
  }
}`, typeRefSelection)

	raw, err := hjem.DARRawQuery(q, nil)
	if err != nil {
		return nil, err
	}
	var s schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func reportSchema(s *schema, hits []usageHit) {
	fmt.Printf("=== 1. SCHEMA INTROSPECTION ===\n\n%d types exposed.\n\n", len(s.Schema.Types))

	roots := make([]string, 0, len(s.Schema.QueryType.Fields))
	for _, f := range s.Schema.QueryType.Fields {
		roots = append(roots, f.Name)
	}
	sort.Strings(roots)
	fmt.Printf("Query root fields (%d) — i.e. every register this endpoint serves:\n  %s\n\n",
		len(roots), strings.Join(roots, "\n  "))

	fmt.Println("--- types whose NAME matches a usage word ---")
	found := false
	for _, t := range s.Schema.Types {
		if !matchesUsage(t.Name) {
			continue
		}
		found = true
		fmt.Printf("%s:\n", t.Name)
		for _, f := range t.Fields {
			fmt.Printf("  %s: %s\n", f.Name, f.Type)
		}
	}
	if !found {
		fmt.Println("(none)")
	}

	fmt.Println("\n--- fields whose NAME matches a usage word, on any type ---")
	if len(hits) == 0 {
		fmt.Println("(none)")
	}
	for _, h := range hits {
		fmt.Printf("  %s.%s: %s\n", h.Type, h.Field, h.Kind)
	}
}

type usageHit struct{ Type, Field, Kind string }

func usageFields(s *schema) []usageHit {
	var out []usageHit
	for _, t := range s.Schema.Types {
		for _, f := range t.Fields {
			if matchesUsage(f.Name) {
				out = append(out, usageHit{t.Name, f.Name, f.Type.String()})
			}
		}
	}
	// Sorted so two runs against the same schema diff cleanly; DAR returns
	// types in no documented order.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Field < out[j].Field
	})
	return out
}

func matchesUsage(name string) bool {
	l := strings.ToLower(name)
	for _, w := range usageWords {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Walk a real address
// ---------------------------------------------------------------------------

// walk resolves an address and dumps the complete scalar record of each entity
// the app already traverses. Selecting every field rather than a guessed subset
// is the whole point: whatever DAR knows about this property shows up with its
// real value, including fields nobody thought to ask for.
func walk(s *schema, addr string) {
	fmt.Print("\n\n=== 2. WALK FROM A REAL ADDRESS ===\n\n")

	centres, err := hjem.AVFuzzySearch{Query: addr}.Fetch()
	if err != nil {
		fmt.Printf("address lookup failed: %v\n", err)
		return
	}
	if len(centres) == 0 {
		fmt.Printf("no address match for %q\n", addr)
		return
	}
	c := centres[0]
	fmt.Printf("centre: %s\n  DAR adresse id_lokalId: %s\n", c.DawaID, c.DawaUUID)

	data := dumpEntity(s, "DAR_Adresse", c.DawaUUID)
	husID := firstField(data, "DAR_Adresse", "husnummer")
	if husID == "" {
		fmt.Println("\nno husnummer id on the address record; stopping the walk here.")
		return
	}
	dumpEntity(s, "DAR_Husnummer", husID)
}

// dumpEntity queries one entity by id, selecting every argument-free scalar or
// enum field. Fields taking arguments are skipped because supplying them blind
// would turn a missing argument into a query error and hide the rest of the
// record; object-valued fields are skipped because DAR models relationships as
// flat id strings, so an object here is geometry or bitemporal metadata.
func dumpEntity(s *schema, entity, id string) json.RawMessage {
	t := s.typeByName(entity)
	if t == nil {
		fmt.Printf("\n--- %s ---\nnot present in the schema\n", entity)
		return nil
	}

	var sel []string
	for _, f := range t.Fields {
		b := f.Type.base()
		if len(f.Args) == 0 && (b.Kind == "SCALAR" || b.Kind == "ENUM") {
			sel = append(sel, f.Name)
		}
	}

	q := fmt.Sprintf(`query($id:String!){ %s(where:{ id_lokalId:{ eq:$id } }){ nodes { %s } } }`,
		entity, strings.Join(sel, " "))
	data, err := hjem.DARRawQuery(q, map[string]any{"id": id})
	if err != nil {
		fmt.Printf("\n--- %s (%d scalar fields) ---\nquery failed: %v\n", entity, len(sel), err)
		return nil
	}
	fmt.Printf("\n--- %s (%d scalar fields) ---\n%s\n", entity, len(sel), pretty(data))
	return data
}

// ---------------------------------------------------------------------------
// Fallback when introspection is disabled
// ---------------------------------------------------------------------------

func guess() {
	guesses := []string{
		`query{ DAR_Adresse(first:1){ nodes { anvendelse } } }`,
		`query{ DAR_Husnummer(first:1){ nodes { anvendelse } } }`,
		`query{ DAR_Husnummer(first:1){ nodes { bygning } } }`,
		`query{ DAR_Bygning(first:1){ nodes { id_lokalId } } }`,
		`query{ BBR_Bygning(first:1){ nodes { id_lokalId } } }`,
		`query{ BBR_Enhed(first:1){ nodes { id_lokalId } } }`,
	}
	for _, q := range guesses {
		fmt.Printf("\n> %s\n", q)
		if data, err := hjem.DARRawQuery(q, nil); err != nil {
			fmt.Printf("  %v\n", err)
		} else {
			fmt.Printf("  OK: %s\n", pretty(data))
		}
	}
}

// ---------------------------------------------------------------------------
// Interpretation
// ---------------------------------------------------------------------------

func interpret(hits []usageHit) {
	fmt.Println("\n\n=== 3. INTERPRETATION ===")

	var onWalked []string
	for _, h := range hits {
		if slices.Contains(walkedTypes, h.Type) {
			onWalked = append(onWalked, h.Type+"."+h.Field)
		}
	}

	switch {
	case len(onWalked) > 0:
		fmt.Println("SCOPE: select one more field in an existing query.")
		fmt.Printf("%s already carries %s; the app queries these entities per address\n", strings.Join(walkedTypes, " / "), strings.Join(onWalked, ", "))
		fmt.Println("today, so the fix is adding the field to the selection set in datafordeler.go.")
	case len(hits) > 0:
		fmt.Println("SCOPE: a further DAR entity hop, not a new register.")
		fmt.Printf("Nothing usage-like sits on %s, but %d usage-like field(s) exist elsewhere in\n", strings.Join(walkedTypes, "/"), len(hits))
		fmt.Println("the schema (listed in section 1). Check section 2's raw dumps for a foreign key")
		fmt.Println("pointing at the owning type; if there is none, the join has to come from elsewhere.")
	default:
		fmt.Println("SCOPE: integrate a separate register.")
		fmt.Printf("No type or field in this schema matches any of %v. DAR models addresses,\n", usageWords)
		fmt.Println("not buildings, so property type would have to come from BBR or another source.")
	}

	fmt.Println()
	fmt.Println("Mapping check: Boliga encodes exactly four kinds — house(1), sharedhouse(2),")
	fmt.Println("apartment(3), vacation(4). Compare the raw code values in section 2 against")
	fmt.Println("those: a DAR taxonomy that splits or crosses these four needs a lossy mapping,")
	fmt.Println("and anything it cannot express has to stay unfiltered rather than be guessed.")
}

// ---------------------------------------------------------------------------

func firstField(raw json.RawMessage, entity, field string) string {
	var m map[string]struct {
		Nodes []map[string]any `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	e := m[entity]
	if len(e.Nodes) == 0 {
		return ""
	}
	if v, ok := e.Nodes[0][field].(string); ok {
		return v
	}
	return ""
}

func pretty(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}
