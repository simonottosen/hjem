// Command dar-probe inspects a single address in DAR by walking the entity
// chain *by id* (id lookups don't need the geometry filter), and dumps the raw
// JSON at each step — especially the access point's `position`, so we can see
// the exact CRS and coordinate format DAR actually serves geometry in.
//
//	DATAFORDELER_API_KEY=xxxx go run ./cmd/dar-probe -addr "Rådhuspladsen 1, 1550 København V"
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	hjem "github.com/tpanum/hjem"
)

const (
	qAdresse    = `query($id:String!){ DAR_Adresse(where:{ id_lokalId:{ eq:$id } }){ nodes { id_lokalId husnummer status } } }`
	qHusnummer  = `query($id:String!){ DAR_Husnummer(where:{ id_lokalId:{ eq:$id } }){ nodes { id_lokalId adgangspunkt husnummertekst status } } }`
	qAdressepkt = `query($id:String!){ DAR_Adressepunkt(where:{ id_lokalId:{ eq:$id } }){ nodes { id_lokalId status position { wkt crs dimension } } } }`
)

func main() {
	addr := flag.String("addr", "Rådhuspladsen 1, 1550 København V", "address to inspect")
	flag.Parse()

	if os.Getenv("DATAFORDELER_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "DATAFORDELER_API_KEY is not set.")
		os.Exit(1)
	}

	centres, err := hjem.DawaFuzzySearch{Query: *addr}.Fetch()
	must(err)
	if len(centres) == 0 {
		fmt.Fprintln(os.Stderr, "no DAWA match")
		os.Exit(1)
	}
	c := centres[0]
	// Address.Latitude holds DAWA x (longitude); .Longtitude holds y (latitude).
	fmt.Printf("centre: %s\n  DAR adresse id_lokalId: %s\n  DAWA point (lon, lat): %.6f, %.6f\n",
		c.DawaID, c.DawaUUID, c.Latitude, c.Longtitude)

	dataA, err := hjem.DARRawQuery(qAdresse, map[string]any{"id": c.DawaUUID})
	must(err)
	fmt.Printf("\n--- DAR_Adresse ---\n%s\n", pretty(dataA))
	husID := firstField(dataA, "DAR_Adresse", "husnummer")
	fmt.Printf("husnummer id: %s\n", husID)

	dataB, err := hjem.DARRawQuery(qHusnummer, map[string]any{"id": husID})
	must(err)
	fmt.Printf("\n--- DAR_Husnummer ---\n%s\n", pretty(dataB))
	punktID := firstField(dataB, "DAR_Husnummer", "adgangspunkt")
	fmt.Printf("adgangspunkt id: %s\n", punktID)

	dataC, err := hjem.DARRawQuery(qAdressepkt, map[string]any{"id": punktID})
	must(err)
	fmt.Printf("\n--- DAR_Adressepunkt (THE POSITION) ---\n%s\n", pretty(dataC))
}

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

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
