package hjem

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDirtyStringToInt(t *testing.T) {
	tt := []struct {
		name string
		in   string
		out  int
		err  string
	}{
		{name: "with dot", in: "10.000", out: 10000},
		{name: "multiple dots", in: "88.299.199", out: 88299199},
		{name: "additional spaces", in: "  999  ", out: 999},
		{name: "with unit", in: "64kr", out: 64},
		{name: "zero", in: "0", out: 0},
		{name: "letters only", in: "absc", err: "invalid syntax"},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			o, err := DirtyStringToInt(tc.in)
			if err != nil {
				if tc.err != "" {
					if strings.Contains(err.Error(), tc.err) {
						return
					}

					t.Fatalf("unexpected error: %s (expected: %s)", err, tc.err)
				}

				t.Fatalf("received unexpected error: %s", err)
			}

			if o != tc.out {
				t.Fatalf("unexpected output: %d (expected: %d)", o, tc.out)
			}
		})
	}
}

func TestDanishDateToTime(t *testing.T) {
	tt := []struct {
		name   string
		in     string
		format string
		out    time.Time
		err    string
	}{
		{name: "basic", in: "10. jan. 2018", format: "2. jan. 2006", out: time.Date(2018, 1, 10, 0, 0, 0, 0, time.UTC)},
		{name: "basic (maj)", in: "12. maj. 2021", format: "2. jan. 2006", out: time.Date(2021, 5, 12, 0, 0, 0, 0, time.UTC)},
		{name: "basic (okt)", in: "28. okt. 2021", format: "2. jan. 2006", out: time.Date(2021, 10, 28, 0, 0, 0, 0, time.UTC)},
		{name: "syntax issue", in: "28-okt-2021", format: "2. jan. 2006", out: time.Date(2021, 10, 28, 0, 0, 0, 0, time.UTC), err: "cannot parse"},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			o, err := DanishDateToTime(tc.format, tc.in)
			if err != nil {
				if tc.err != "" {
					if strings.Contains(err.Error(), tc.err) {
						return
					}

					t.Fatalf("unexpected error: %s (expected: %s)", err, tc.err)
				}

				t.Fatalf("received unexpected error: %s", err)
			}

			if o != tc.out {
				t.Fatalf("unexpected output: %v (expected: %v)", o, tc.out)
			}
		})
	}
}

// TestBoligaForbiddenNotRetried pins the behaviour issue #30 was about: a 403
// must be attempted once and then reported as a block, not retried and not
// filed as a generic network error.
func TestBoligaForbiddenNotRetried(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	orig := boligaSoldSearchURL
	boligaSoldSearchURL = srv.URL
	defer func() { boligaSoldSearchURL = orig }()

	addrs := []*Address{{
		MunicipalityCode: "0101",
		StreetName:       "Vestergade",
		PostalCode:       "1456",
		StreetNumber:     "1",
	}}
	stats := NewHealthStats()

	_, warnings, err := BoligaSalesFromAddrs(addrs, NewProgress(), stats)
	if err == nil {
		t.Fatal("expected an error when the only street request is refused")
	}

	// The load-bearing assertion. Retrying would make this 6 (1 + maxRetries)
	// and cost ~62s of backoff per blocked street for a refusal that will not
	// change on a retry.
	if requests != 1 {
		t.Errorf("made %d requests, want 1 — a 403 must not be retried", requests)
	}

	recent := stats.Snapshot().RecentErrors
	if len(recent) != 1 || recent[0].Type != "forbidden" {
		t.Errorf("recorded %+v, want a single error of type \"forbidden\"", recent)
	}

	if len(warnings) != 1 || !strings.Contains(warnings[0], "blokeret af Boliga") {
		t.Errorf("warnings = %v, want one mentioning \"blokeret af Boliga\"", warnings)
	}
}
