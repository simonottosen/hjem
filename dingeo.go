package hjem

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
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

func FetchDingeoValuation(dawaUUID string) (*DingeoValuation, error) {
	if dawaUUID == "" {
		return nil, nil
	}

	endpoint := fmt.Sprintf(
		"https://www.dingeo.dk/_ah/api/bvsvurderingendpoint/v1/getSmartVurdering?adresseid=%s",
		strings.ToUpper(dawaUUID),
	)

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", "https://www.boliga.dk")
	req.Header.Set("Referer", "https://www.boliga.dk/")

	log.Printf("Fetching Dingeo valuation for %s", dawaUUID)
	resp, err := DefaultClient.Do(req)
	if err != nil {
		log.Printf("Dingeo request failed: %v", err)
		return nil, nil // non-fatal: don't fail the whole lookup
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Dingeo returned status %d", resp.StatusCode)
		return nil, nil // non-fatal
	}

	var val DingeoValuation
	if err := json.NewDecoder(resp.Body).Decode(&val); err != nil {
		log.Printf("Dingeo decode failed: %v", err)
		return nil, nil // non-fatal
	}

	log.Printf("Dingeo valuation: %d estimates, range %d–%d, mean %.0f",
		val.CountEvals, val.MinVal, val.MaxVal, val.Mean)

	return &val, nil
}
