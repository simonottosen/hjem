// Command bbr-usage-probe answers the question dar-usage-probe left open: DAR
// turned out to carry nothing describing what a property *is* (no anvendelse
// on DAR_Adresse or DAR_Husnummer, and no building or BBR types on its
// endpoint), so the comparables filter's property type has to come from BBR,
// the building register, if it is to cover properties that have never sold.
//
// Three things are unknown, and the probe reports each separately:
//
//  1. Which Datafordeler GraphQL endpoint serves BBR to this API key. The
//     documentation names a per-register path and cross-register "Flexible"
//     paths under different versions in different places, so every candidate
//     is tried rather than one guessed.
//
//  2. Whether BBR_Enhed carries the unit's usage code (enh020EnhedensAnvendelse)
//     and the DAR address id it hangs off (adresseIdentificerer), which is the
//     join the app would need. Each endpoint's schema is downloaded from its
//     /schema path, because DAR's endpoint refuses introspection and BBR's may
//     too; a GraphQL validation error is used as evidence the same way.
//
//  3. The actual code values for real addresses. Boliga encodes exactly four
//     kinds (house, shared house, apartment, vacation), and whether BBR's
//     codes can be mapped onto those four is only decidable from the values.
//
//     set -a; . ./.env; set +a
//     go run ./cmd/bbr-usage-probe -addr "Flensborggade 40, 4. tv, 1669 København V" -addr "<a house>"
//
// The API key is sent as the apiKey query parameter, as the app does for DAR,
// and is never printed.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	hjem "github.com/tpanum/hjem"
)

// candidates are the endpoint paths the Datafordeler documentation mentions
// for BBR, in the order worth trying: the register's own endpoint first, then
// the cross-register ones that also serve DAR (and so could do the join in one
// query). Capitalisation differs between documents, so the request is made as
// written and the server decides.
var candidates = []string{
	"https://graphql.datafordeler.dk/BBR/v1",
	"https://graphql.datafordeler.dk/BBR/v2",
	"https://graphql.datafordeler.dk/BBR/v3",
	"https://graphql.datafordeler.dk/Flexible/v1",
	"https://graphql.datafordeler.dk/Flexible/v3",
	"https://graphql.datafordeler.dk/FlexibleCurrent/v1",
}

var client = &http.Client{Timeout: 60 * time.Second}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, "; ") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	var addrs multiFlag
	flag.Var(&addrs, "addr", "address to inspect (repeatable); pick a few of known kinds — an apartment, a detached house, a terraced house, a summer house")
	only := flag.String("endpoint", "", "try only this endpoint base URL instead of the candidate list")
	flag.Parse()
	if len(addrs) == 0 {
		addrs = multiFlag{"Flensborggade 40, 4. tv, 1669 København V", "Rådhuspladsen 1, 1550 København V"}
	}

	key := os.Getenv("DATAFORDELER_API_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "DATAFORDELER_API_KEY is not set — this probe talks to the live Datafordeler API and cannot run without a key.")
		fmt.Fprintln(os.Stderr, `Usage: set -a; . ./.env; set +a; go run ./cmd/bbr-usage-probe -addr "<address>" [-addr ...]`)
		os.Exit(1)
	}

	eps := candidates
	if *only != "" {
		eps = []string{*only}
	}

	working, schemaPath := probeEndpoints(eps, key)
	walk(working, key, addrs)
	interpret(working, schemaPath)
}

// ---------------------------------------------------------------------------
// 1 + 2. Endpoints and schema
// ---------------------------------------------------------------------------

// endpoint is a base URL that answered a BBR_Enhed query, and whether it wants
// registreringstid as well as virkningstid. The Flexible endpoints require
// both; FlexibleCurrent is documented as taking virkningstid only.
type endpoint struct {
	base    string
	regTime bool
}

func probeEndpoints(eps []string, key string) (*endpoint, string) {
	fmt.Print("=== 1. ENDPOINTS ===\n\n")

	var found *endpoint
	var schemaPath string
	for _, base := range eps {
		fmt.Printf("--- %s\n", base)

		sdl, status, err := get(base+"/schema", key)
		switch {
		case err != nil:
			fmt.Printf("  schema: request failed: %v\n", err)
		case status != http.StatusOK:
			fmt.Printf("  schema: HTTP %d\n", status)
		default:
			fmt.Printf("  schema: HTTP 200, %d bytes; mentions BBR_Enhed: %v; enh020EnhedensAnvendelse: %v; adresseIdentificerer: %v\n",
				len(sdl), strings.Contains(sdl, "BBR_Enhed"), strings.Contains(sdl, "enh020EnhedensAnvendelse"), strings.Contains(sdl, "adresseIdentificerer"))
			if schemaPath == "" && strings.Contains(sdl, "BBR_Enhed") {
				schemaPath = saveSchema(base, sdl)
				excerpt(sdl)
			}
		}

		ep := tryEnhed(base, key)
		if ep != nil && found == nil {
			found = ep
		}
		fmt.Println()
	}
	return found, schemaPath
}

// tryEnhed runs the smallest query that would prove the join works: one unit,
// its usage code and its address id. A validation error naming a field is kept
// verbatim — it is the evidence of absence, exactly as in dar-usage-probe.
func tryEnhed(base, key string) *endpoint {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000000") + "Z"
	sel := "id_lokalId adresseIdentificerer enh020EnhedensAnvendelse"

	for _, regTime := range []bool{true, false} {
		q := fmt.Sprintf(`query{ BBR_Enhed(first: 1, %s){ nodes { %s } } }`, timeArgs(regTime, now), sel)
		data, err := post(base, key, q, nil)
		label := "registreringstid+virkningstid"
		if !regTime {
			label = "virkningstid only"
		}
		if err != nil {
			fmt.Printf("  BBR_Enhed (%s): %v\n", label, short(err.Error()))
			continue
		}
		fmt.Printf("  BBR_Enhed (%s): OK\n%s\n", label, indent(pretty(data)))
		return &endpoint{base: base, regTime: regTime}
	}
	return nil
}

func timeArgs(regTime bool, now string) string {
	if regTime {
		return fmt.Sprintf(`registreringstid: "%s", virkningstid: "%s"`, now, now)
	}
	return fmt.Sprintf(`virkningstid: "%s"`, now)
}

// saveSchema keeps the whole schema on disk: it is the reference for whatever
// the next step turns out to be, and far too long to print.
func saveSchema(base, sdl string) string {
	name := "bbr-schema-" + regexp.MustCompile(`[^A-Za-z0-9]+`).ReplaceAllString(strings.TrimPrefix(base, "https://"), "-") + ".graphql"
	p := filepath.Join(os.TempDir(), name)
	if err := os.WriteFile(p, []byte(sdl), 0o644); err != nil {
		fmt.Printf("  (could not save schema: %v)\n", err)
		return ""
	}
	fmt.Printf("  full schema saved to %s\n", p)
	return p
}

// excerpt prints the parts of the schema the decision turns on: every field of
// BBR_Enhed and BBR_Bygning that names a usage, an address or a building, and
// any field on DAR_Adresse that leads to a unit (the documented join is
// DAR_Adresse.AdresseHarEnhed).
func excerpt(sdl string) {
	show := map[string]*regexp.Regexp{
		"BBR_Enhed":   regexp.MustCompile(`(?i)anvendelse|adresse|bygning|status|kommune`),
		"BBR_Bygning": regexp.MustCompile(`(?i)anvendelse|husnummer|status`),
		"DAR_Adresse": regexp.MustCompile(`(?i)enhed`),
	}
	for _, typ := range []string{"BBR_Enhed", "BBR_Bygning", "DAR_Adresse"} {
		block := typeBlock(sdl, typ)
		if block == "" {
			fmt.Printf("  type %s: not in this schema\n", typ)
			continue
		}
		fmt.Printf("  type %s (matching fields):\n", typ)
		n := 0
		for _, line := range strings.Split(block, "\n") {
			if show[typ].MatchString(line) {
				fmt.Printf("    %s\n", strings.TrimSpace(line))
				n++
			}
		}
		if n == 0 {
			fmt.Println("    (none)")
		}
	}
}

func typeBlock(sdl, name string) string {
	m := regexp.MustCompile(`(?s)\btype ` + name + `\b[^{]*\{(.*?)\n\}`).FindStringSubmatch(sdl)
	if m == nil {
		return ""
	}
	return m[1]
}

// ---------------------------------------------------------------------------
// 3. Walk real addresses
// ---------------------------------------------------------------------------

// walk resolves each address to its DAR id and asks BBR for the units hanging
// off it, then for each unit's building. Both codes are shown because they can
// disagree — a flat in a building used mainly for shops, a granny flat on a
// farm — and which one the filter should trust depends on what they look like.
func walk(ep *endpoint, key string, addrs []string) {
	fmt.Print("\n=== 3. REAL ADDRESSES ===\n\n")
	if ep == nil {
		fmt.Println("skipped: no endpoint answered a BBR_Enhed query (see section 1).")
		return
	}
	fmt.Printf("using %s\n", ep.base)
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000000") + "Z"

	for _, a := range addrs {
		fmt.Printf("\n--- %s\n", a)
		found, err := hjem.AVFuzzySearch{Query: a}.Fetch()
		if err != nil {
			fmt.Printf("  address lookup failed: %v\n", err)
			continue
		}
		if len(found) == 0 {
			fmt.Println("  no address match")
			continue
		}
		fmt.Printf("  DAR: %s (id %s)\n", found[0].DawaID, found[0].DawaUUID)

		vars := map[string]any{"id": found[0].DawaUUID}
		q := fmt.Sprintf(`query($id: String!){ BBR_Enhed(first: 20, %s, where: { adresseIdentificerer: { eq: $id } }){ nodes { id_lokalId status enh020EnhedensAnvendelse } } }`,
			timeArgs(ep.regTime, now))
		data, err := post(ep.base, key, q, vars)
		if err != nil {
			fmt.Printf("  BBR_Enhed by adresseIdentificerer: %v\n", short(err.Error()))
			continue
		}
		fmt.Printf("  units:\n%s\n", indent(pretty(data)))

		// The building link is asked for separately: if the field is named
		// differently, that error should not also hide the usage codes above.
		q = fmt.Sprintf(`query($id: String!){ BBR_Enhed(first: 20, %s, where: { adresseIdentificerer: { eq: $id } }){ nodes { bygning } } }`,
			timeArgs(ep.regTime, now))
		links, err := post(ep.base, key, q, vars)
		if err != nil {
			fmt.Printf("  BBR_Enhed.bygning: %v\n", short(err.Error()))
			continue
		}
		for _, b := range uniqueField(links, "BBR_Enhed", "bygning") {
			q := fmt.Sprintf(`query($id: String!){ BBR_Bygning(first: 1, %s, where: { id_lokalId: { eq: $id } }){ nodes { id_lokalId status byg021BygningensAnvendelse } } }`,
				timeArgs(ep.regTime, now))
			bd, err := post(ep.base, key, q, map[string]any{"id": b})
			if err != nil {
				fmt.Printf("  BBR_Bygning %s: %v\n", b, short(err.Error()))
				continue
			}
			fmt.Printf("  building %s:\n%s\n", b, indent(pretty(bd)))
		}
	}
}

// ---------------------------------------------------------------------------
// Interpretation
// ---------------------------------------------------------------------------

func interpret(ep *endpoint, schemaPath string) {
	fmt.Print("\n=== 4. INTERPRETATION ===\n\n")
	if ep == nil {
		fmt.Println("No candidate endpoint answered a BBR_Enhed query. Read section 1:")
		fmt.Println("  - HTTP 401/403 everywhere: the key was rejected. The gateway checks it before")
		fmt.Println("    looking at the path, so this says nothing about which paths exist. If the")
		fmt.Println("    same key works for DAR, it is not entitled to BBR: BBR is free data, but has")
		fmt.Println("    to be added to the key's services in the Datafordeler portal.")
		fmt.Println("  - HTTP 404 everywhere: the endpoint paths have moved; rerun with -endpoint")
		fmt.Println("    set to the path the Datafordeler documentation currently gives for BBR.")
		fmt.Println("  - A validation error naming a field: that field does not exist under that")
		fmt.Println("    name, and the schema excerpt shows what does.")
		return
	}
	fmt.Printf("BBR is reachable at %s (%s).\n",
		ep.base, map[bool]string{true: "registreringstid + virkningstid", false: "virkningstid only"}[ep.regTime])
	fmt.Println("BBR_Enhed carries enh020EnhedensAnvendelse and adresseIdentificerer, so a unit's")
	fmt.Println("usage can be looked up from the DAR address id the app already has for every")
	fmt.Println("address — one extra batched query per lookup, keyed on ids it already holds.")
	if schemaPath != "" {
		fmt.Printf("The full schema is in %s.\n", schemaPath)
	}
	fmt.Println()
	fmt.Println("Mapping check: compare the enh020 codes in section 3 against Boliga's four")
	fmt.Println("kinds — house(1), sharedhouse(2), apartment(3), vacation(4). An address with no")
	fmt.Println("unit, or a code outside the residential ones, should stay unfiltered rather than")
	fmt.Println("be guessed. Code meanings: Danmarks Statistik, ENH ANVENDELSEKODE.")
}

// ---------------------------------------------------------------------------
// HTTP
// ---------------------------------------------------------------------------

func withKey(base, key string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("apiKey", key)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func get(base, key string) (string, int, error) {
	u, err := withKey(base, key)
	if err != nil {
		return "", 0, err
	}
	resp, err := client.Get(u)
	if err != nil {
		return "", 0, redact(err, key)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	return string(b), resp.StatusCode, err
}

func post(base, key, query string, variables map[string]any) (json.RawMessage, error) {
	u, err := withKey(base, key)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"query": query}
	if variables != nil {
		payload["variables"] = variables
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, redact(err, key)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, redact(fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw))), key)
	}

	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("not JSON: %s", strings.TrimSpace(string(raw)))
	}
	if len(env.Errors) > 0 {
		msgs := make([]string, len(env.Errors))
		for i, e := range env.Errors {
			msgs[i] = e.Message
		}
		return nil, fmt.Errorf("GraphQL errors: %s", strings.Join(msgs, "; "))
	}
	return env.Data, nil
}

// redact keeps the key out of transport errors, which quote the request URL.
func redact(err error, key string) error {
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), key, "<key>"))
}

// ---------------------------------------------------------------------------

func uniqueField(raw json.RawMessage, entity, field string) []string {
	var m map[string]struct {
		Nodes []map[string]any `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, n := range m[entity].Nodes {
		if v, ok := n[field].(string); ok && v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func pretty(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}

func short(s string) string {
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}
