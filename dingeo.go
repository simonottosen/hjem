package hjem

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

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

func getFlareSolverrURL() string {
	if url := os.Getenv("FLARESOLVERR_URL"); url != "" {
		return url
	}
	return "" // disabled by default
}

func FetchDingeoValuation(dawaUUID string) (*DingeoValuation, error) {
	if dawaUUID == "" {
		return nil, nil
	}

	endpoint := fmt.Sprintf(
		"https://www.dingeo.dk/_ah/api/bvsvurderingendpoint/v1/getSmartVurdering?adresseid=%s",
		strings.ToUpper(dawaUUID),
	)

	flareSolverrURL := getFlareSolverrURL()

	// If FlareSolverr is configured, use it to bypass Cloudflare challenges
	if flareSolverrURL != "" {
		return fetchDingeoViaFlareSolverr(flareSolverrURL, endpoint, dawaUUID)
	}

	// Direct request (works when not behind Cloudflare bot detection)
	return fetchDingeoDirect(endpoint, dawaUUID)
}

func fetchDingeoDirect(endpoint, dawaUUID string) (*DingeoValuation, error) {
	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, nil
	}

	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9,da;q=0.8")
	req.Header.Set("Origin", "https://www.boliga.dk")
	req.Header.Set("Referer", "https://www.boliga.dk/")

	log.Printf("Fetching Dingeo valuation (direct) for %s", dawaUUID)
	resp, err := DefaultClient.Do(req)
	if err != nil {
		log.Printf("Dingeo request failed: %v", err)
		return nil, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Dingeo returned status %d (direct)", resp.StatusCode)
		return nil, nil
	}

	var val DingeoValuation
	if err := json.NewDecoder(resp.Body).Decode(&val); err != nil {
		log.Printf("Dingeo decode failed: %v", err)
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
		log.Printf("FlareSolverr marshal failed: %v", err)
		return nil, nil
	}

	client := http.Client{Timeout: 60 * time.Second}
	resp, err := client.Post(flareSolverrURL+"/v1", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("FlareSolverr request failed: %v", err)
		return nil, nil
	}
	defer resp.Body.Close()

	var fsResp flareSolverrResponse
	if err := json.NewDecoder(resp.Body).Decode(&fsResp); err != nil {
		log.Printf("FlareSolverr decode failed: %v", err)
		return nil, nil
	}

	if fsResp.Status != "ok" {
		log.Printf("FlareSolverr error: %s", fsResp.Message)
		return nil, nil
	}

	if fsResp.Solution.Status != 200 {
		log.Printf("Dingeo returned status %d via FlareSolverr", fsResp.Solution.Status)
		return nil, nil
	}

	// FlareSolverr returns the page HTML. The JSON may be wrapped in HTML tags.
	// Extract the JSON content — look for the first '{' to last '}'
	rawResponse := fsResp.Solution.Response
	jsonStr := extractJSON(rawResponse)
	if jsonStr == "" {
		log.Printf("Dingeo via FlareSolverr: no JSON found in response (length %d)", len(rawResponse))
		if len(rawResponse) > 200 {
			log.Printf("Dingeo response preview: %s", rawResponse[:200])
		}
		return nil, nil
	}

	var val DingeoValuation
	if err := json.NewDecoder(strings.NewReader(jsonStr)).Decode(&val); err != nil {
		log.Printf("Dingeo decode (via FlareSolverr) failed: %v", err)
		return nil, nil
	}

	log.Printf("Dingeo valuation (via FlareSolverr): %d estimates, mean %.0f", val.CountEvals, val.Mean)
	return &val, nil
}
