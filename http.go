package hjem

import (
	"log"
	"math"
	"net/http"
	"time"
)

var DefaultClient http.Client

func init() {
	DefaultClient = http.Client{
		Transport: &RetryRoundTripper{
			next: &DefaultHeadersTripper{
				next: http.DefaultTransport,
				headers: map[string]string{
					"User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
				},
			},
			maxRetries: 5,
		},
	}
}

type DefaultHeadersTripper struct {
	next    http.RoundTripper
	headers map[string]string
}

func (t *DefaultHeadersTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range t.headers {
		req.Header.Add(k, v)
	}

	return t.next.RoundTrip(req)
}

type RetryRoundTripper struct {
	next       http.RoundTripper
	maxRetries int
}

func (r *RetryRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	var resp *http.Response
	var err error

	for i := 0; i <= r.maxRetries; i++ {
		resp, err = r.next.RoundTrip(req)

		if err != nil {
			// Transport/network error — retry with backoff
			if i < r.maxRetries {
				backoff := time.Duration(math.Pow(1.5, float64(i))) * time.Second
				log.Printf("HTTP retry %d/%d for %s: transport error: %v (backoff %s)",
					i+1, r.maxRetries, req.URL.Host, err, backoff)
				time.Sleep(backoff)
				continue
			}
			return nil, err
		}

		// Success — no retry needed
		if resp.StatusCode < 429 {
			return resp, nil
		}

		// Rate limited (429) or server error (5xx) — retry with backoff
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			if i < r.maxRetries {
				backoff := time.Duration(math.Pow(1.5, float64(i))) * time.Second
				log.Printf("HTTP retry %d/%d for %s: status %d (backoff %s)",
					i+1, r.maxRetries, req.URL.Host, resp.StatusCode, backoff)
				resp.Body.Close()
				time.Sleep(backoff)
				continue
			}
		}

		return resp, nil
	}

	return resp, err
}
