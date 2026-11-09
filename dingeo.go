package hjem

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// ErrDingeoUnconfigured marks a failure that is explained by this deployment
// rather than by Dingeo. Without FLARESOLVERR_URL the direct request goes
// straight into Cloudflare's bot check, so it fails for a reason an operator
// already knows about and has chosen; filing those alongside genuine outages
// would make an unconfigured instance look permanently broken and bury the
// case worth paging on. It is a separate class from "no valuation exists"
// because a valuation may well exist — we are simply not equipped to read it.
var ErrDingeoUnconfigured = errors.New("dingeo: FlareSolverr not configured")

type DingeoEstimate struct {
	Name  string `json:"name"`
	Link  string `json:"link"`
	Value int    `json:"value"`
}

type DingeoValuation struct {
	AdresseID     string           `json:"adresseId"`
	IncludedEvals []DingeoEstimate `json:"includedEvals"`
	OutlierEvals  []DingeoEstimate `json:"outlierEvals"`
	MinVal        int              `json:"minVal"`
	MaxVal        int              `json:"maxVal"`
	Mean          float64          `json:"mean"`
	StdDev        float64          `json:"standdev"`
	CountEvals    int              `json:"countEvals"`
}

// FlareSolverr request/response types
type flareSolverrRequest struct {
	Cmd        string `json:"cmd"`
	URL        string `json:"url"`
	MaxTimeout int    `json:"maxTimeout"`
}

type flareSolverrResponse struct {
	Status   string `json:"status"`
	Message  string `json:"message"`
	Solution struct {
		URL      string `json:"url"`
		Status   int    `json:"status"`
		Response string `json:"response"`
	} `json:"solution"`
}

// A var so tests can point it at an httptest server, like boligaSoldSearchURL.
var dingeoSmartVurderingURL = "https://www.dingeo.dk/_ah/api/bvsvurderingendpoint/v1/getSmartVurdering"

func getFlareSolverrURL() string {
	if url := os.Getenv("FLARESOLVERR_URL"); url != "" {
		return url
	}
	return "" // disabled by default
}

// FetchDingeoValuation reports three outcomes, and the caller has to be able to
// tell them apart: a valuation, (nil, nil) for a property Dingeo knows about but
// has no estimates for, and an error for everything that stopped us reading an
// answer at all. They used to collapse into (nil, nil), which meant a Cloudflare
// challenge, a missing FlareSolverr and an unlisted flat were one state —
// Dingeo could be down for every lookup for weeks and the only visible symptom
// was two estimate cards where three belong.
func FetchDingeoValuation(dawaUUID string) (*DingeoValuation, error) {
	// Not an error: an address with no DAR id is not a Dingeo problem, and
	// there is no key we could have asked under.
	if dawaUUID == "" {
		return nil, nil
	}

	endpoint := fmt.Sprintf(
		dingeoSmartVurderingURL+"?adresseid=%s",
		strings.ToUpper(dawaUUID),
	)

	flareSolverrURL := getFlareSolverrURL()

	// If FlareSolverr is configured, use it to bypass Cloudflare challenges
	if flareSolverrURL != "" {
		return fetchDingeoViaFlareSolverr(flareSolverrURL, endpoint, dawaUUID)
	}

	// Direct request (works when not behind Cloudflare bot detection)
	val, err := fetchDingeoDirect(endpoint, dawaUUID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDingeoUnconfigured, err)
	}
	return val, nil
}

func fetchDingeoDirect(endpoint, dawaUUID string) (*DingeoValuation, error) {
	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("dingeo: build request: %w", err)
	}

	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9,da;q=0.8")
	req.Header.Set("Origin", "https://www.boliga.dk")
	req.Header.Set("Referer", "https://www.boliga.dk/")

	log.Printf("Fetching Dingeo valuation (direct) for %s", dawaUUID)
	resp, err := DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dingeo: request failed: %w", err)
	}
	defer resp.Body.Close()

	// Every non-200 is a failure, including 404. Dingeo answers an address it
	// has nothing for with a 200 and countEvals 0, so a 404 here is the API
	// having moved or Cloudflare standing in front of it — not a quiet way of
	// saying the flat is unlisted.
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dingeo: status %d", resp.StatusCode)
	}

	var val DingeoValuation
	if err := json.NewDecoder(resp.Body).Decode(&val); err != nil {
		// A bot check answers 200 with HTML, so this is the usual shape of
		// being blocked rather than a rare malformed payload.
		return nil, fmt.Errorf("dingeo: decode: %w", err)
	}

	// The one genuine "no valuation exists": we reached Dingeo, it answered for
	// this address, and the answer was that it holds no estimates.
	if val.CountEvals == 0 {
		log.Printf("Dingeo: no valuations available for %s", dawaUUID)
		return nil, nil
	}

	log.Printf("Dingeo valuation: %d estimates, mean %.0f", val.CountEvals, val.Mean)
	return &val, nil
}

// extractJSON finds the first JSON object in a string that may contain HTML wrapping.
// Browsers render JSON API responses inside <pre> tags or <body> directly.
func extractJSON(s string) string {
	start := strings.Index(s, "{")
	if start == -1 {
		return ""
	}
	end := strings.LastIndex(s, "}")
	if end == -1 || end <= start {
		return ""
	}
	return s[start : end+1]
}

func fetchDingeoViaFlareSolverr(flareSolverrURL, endpoint, dawaUUID string) (*DingeoValuation, error) {
	log.Printf("Fetching Dingeo valuation (FlareSolverr) for %s", dawaUUID)

	body, err := json.Marshal(flareSolverrRequest{
		Cmd:        "request.get",
		URL:        endpoint,
		MaxTimeout: 30000,
	})
	if err != nil {
		return nil, fmt.Errorf("dingeo: marshal FlareSolverr request: %w", err)
	}

	client := http.Client{Timeout: 60 * time.Second}
	resp, err := client.Post(flareSolverrURL+"/v1", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("dingeo: FlareSolverr request failed: %w", err)
	}
	defer resp.Body.Close()

	var fsResp flareSolverrResponse
	if err := json.NewDecoder(resp.Body).Decode(&fsResp); err != nil {
		return nil, fmt.Errorf("dingeo: decode FlareSolverr reply: %w", err)
	}

	// FlareSolverr itself failing — it could not solve the challenge, or the
	// browser behind it died. Nothing was learned about the address.
	if fsResp.Status != "ok" {
		return nil, fmt.Errorf("dingeo: FlareSolverr: %s", fsResp.Message)
	}

	if fsResp.Solution.Status != 200 {
		return nil, fmt.Errorf("dingeo: status %d via FlareSolverr", fsResp.Solution.Status)
	}

	// FlareSolverr returns the page HTML. The JSON may be wrapped in HTML tags.
	// Extract the JSON content — look for the first '{' to last '}'
	rawResponse := fsResp.Solution.Response
	jsonStr := extractJSON(rawResponse)
	if jsonStr == "" {
		// Reaching here with a 200 is the signature of a challenge page served
		// in place of the API, so the preview is worth the log line.
		if len(rawResponse) > 200 {
			log.Printf("Dingeo response preview: %s", rawResponse[:200])
		}
		return nil, fmt.Errorf("dingeo: no JSON in FlareSolverr response (length %d)", len(rawResponse))
	}

	var val DingeoValuation
	if err := json.NewDecoder(strings.NewReader(jsonStr)).Decode(&val); err != nil {
		return nil, fmt.Errorf("dingeo: decode via FlareSolverr: %w", err)
	}

	if val.CountEvals == 0 {
		log.Printf("Dingeo: no valuations available for %s (via FlareSolverr)", dawaUUID)
		return nil, nil
	}

	log.Printf("Dingeo valuation (via FlareSolverr): %d estimates, mean %.0f", val.CountEvals, val.Mean)
	return &val, nil
}
