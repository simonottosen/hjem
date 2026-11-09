package hjem

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A Cloudflare interstitial: 200 with an HTML body, which is what a scraper
// without FlareSolverr actually receives rather than a clean 403.
const cloudflareChallenge = `<!DOCTYPE html><html><head><title>Just a moment...</title></head>
<body><div id="challenge-running">Checking your browser...</div></body></html>`

// The three outcomes used to be one: every one of them returned (nil, nil), so
// a blocked scraper and an unlisted flat were indistinguishable at the call
// site. This asserts they are now told apart.
func TestDingeoDirectDistinguishesFailureFromNoValuation(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		wantErr bool
		wantVal bool
	}{
		{
			name: "cloudflare challenge",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				fmt.Fprint(w, cloudflareChallenge)
			},
			wantErr: true,
		},
		{
			name: "malformed body",
			handler: func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `{"countEvals": `)
			},
			wantErr: true,
		},
		{
			name: "refused outright",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
			},
			wantErr: true,
		},
		{
			name: "no valuation for this property",
			handler: func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(DingeoValuation{CountEvals: 0})
			},
		},
		{
			name: "valuation present",
			handler: func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(DingeoValuation{CountEvals: 2, Mean: 4_200_000})
			},
			wantVal: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(tt.handler)
			defer ts.Close()

			val, err := fetchDingeoDirect(ts.URL, "some-uuid")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if (val != nil) != tt.wantVal {
				t.Fatalf("val = %+v, wantVal %v", val, tt.wantVal)
			}
		})
	}
}

// Without FLARESOLVERR_URL a block is a consequence of how this instance is
// deployed, not of Dingeo being down, and the two must not be filed together.
func TestDingeoWithoutFlareSolverrMarksFailureUnconfigured(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, cloudflareChallenge)
	}))
	defer ts.Close()

	t.Setenv("FLARESOLVERR_URL", "")
	old := dingeoSmartVurderingURL
	dingeoSmartVurderingURL = ts.URL
	defer func() { dingeoSmartVurderingURL = old }()

	_, err := FetchDingeoValuation("some-uuid")
	if !errors.Is(err, ErrDingeoUnconfigured) {
		t.Fatalf("err = %v, want it to wrap ErrDingeoUnconfigured", err)
	}
}

// A property with no estimates is not a configuration problem, so it must not
// pick up the marker even on an unconfigured instance.
func TestDingeoNoValuationIsNotUnconfigured(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(DingeoValuation{CountEvals: 0})
	}))
	defer ts.Close()

	t.Setenv("FLARESOLVERR_URL", "")
	old := dingeoSmartVurderingURL
	dingeoSmartVurderingURL = ts.URL
	defer func() { dingeoSmartVurderingURL = old }()

	val, err := FetchDingeoValuation("some-uuid")
	if err != nil || val != nil {
		t.Fatalf("got (%+v, %v), want (nil, nil)", val, err)
	}
}

// FlareSolverr can fail in ways the JSON body alone does not show: it answers
// 200 with status "error", or hands back the challenge page it failed to solve.
func TestDingeoViaFlareSolverrDistinguishesFailureFromNoValuation(t *testing.T) {
	tests := []struct {
		name     string
		solution flareSolverrResponse
		wantErr  bool
		wantVal  bool
	}{
		{
			name:     "flaresolverr could not solve the challenge",
			solution: flareSolverrResponse{Status: "error", Message: "Challenge not solved!"},
			wantErr:  true,
		},
		{
			name: "challenge page relayed as the answer",
			solution: func() flareSolverrResponse {
				var r flareSolverrResponse
				r.Status = "ok"
				r.Solution.Status = 200
				r.Solution.Response = "<html><body>Checking your browser...</body></html>"
				return r
			}(),
			wantErr: true,
		},
		{
			name: "dingeo refused",
			solution: func() flareSolverrResponse {
				var r flareSolverrResponse
				r.Status = "ok"
				r.Solution.Status = 503
				return r
			}(),
			wantErr: true,
		},
		{
			name: "no valuation for this property",
			solution: func() flareSolverrResponse {
				var r flareSolverrResponse
				r.Status = "ok"
				r.Solution.Status = 200
				r.Solution.Response = `<pre>{"countEvals":0}</pre>`
				return r
			}(),
		},
		{
			name: "valuation present",
			solution: func() flareSolverrResponse {
				var r flareSolverrResponse
				r.Status = "ok"
				r.Solution.Status = 200
				r.Solution.Response = `<pre>{"countEvals":3,"mean":5100000}</pre>`
				return r
			}(),
			wantVal: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(tt.solution)
			}))
			defer ts.Close()

			val, err := fetchDingeoViaFlareSolverr(ts.URL, "https://example.invalid/x", "some-uuid")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if (val != nil) != tt.wantVal {
				t.Fatalf("val = %+v, wantVal %v", val, tt.wantVal)
			}
			// A configured instance that fails has failed for real, and must
			// not be excused as a configuration gap.
			if err != nil && errors.Is(err, ErrDingeoUnconfigured) {
				t.Fatalf("err = %v, want it not to wrap ErrDingeoUnconfigured", err)
			}
		})
	}
}
