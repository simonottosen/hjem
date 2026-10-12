package hjem

import (
	"net/http/httptest"
	"testing"
)

// The header is the client's to write, so it only means anything when the
// connection came from a proxy of ours — and then only its last entry, which
// is the address that proxy saw rather than one the client put there.
func TestClientKeyTrustsOnlyTheProxysOwnEntry(t *testing.T) {
	cases := []struct {
		name      string
		peer      string
		forwarded []string
		want      string
	}{{
		name: "no proxy",
		peer: "198.51.100.7:51000",
		want: "198.51.100.7",
	}, {
		name:      "header from a public peer is ignored",
		peer:      "198.51.100.7:51000",
		forwarded: []string{"203.0.113.9"},
		want:      "198.51.100.7",
	}, {
		name:      "tunnel on loopback",
		peer:      "127.0.0.1:51000",
		forwarded: []string{"203.0.113.9"},
		want:      "203.0.113.9",
	}, {
		// The budget would be one header away from being reset otherwise:
		// every entry but the last was written before the request reached us.
		name:      "entries the client prepended",
		peer:      "10.0.0.2:51000",
		forwarded: []string{"1.1.1.1, 2.2.2.2, 203.0.113.9"},
		want:      "203.0.113.9",
	}, {
		name:      "repeated headers are one chain",
		peer:      "10.0.0.2:51000",
		forwarded: []string{"1.1.1.1", "2.2.2.2, 203.0.113.9"},
		want:      "203.0.113.9",
	}, {
		// Not what a proxy writes, so the chain is not the shape assumed here
		// and the connection itself is all that is left to go on.
		name:      "unparseable last entry",
		peer:      "10.0.0.2:51000",
		forwarded: []string{"203.0.113.9, not-an-address"},
		want:      "10.0.0.2",
	}, {
		name:      "empty header",
		peer:      "10.0.0.2:51000",
		forwarded: []string{""},
		want:      "10.0.0.2",
	}, {
		name: "ipv6 peer",
		peer: "[2001:db8::1]:51000",
		want: "2001:db8::1",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/lookup", nil)
			req.RemoteAddr = tc.peer
			for _, v := range tc.forwarded {
				req.Header.Add("X-Forwarded-For", v)
			}

			if got := clientKey(req); got != tc.want {
				t.Errorf("clientKey = %q, want %q", got, tc.want)
			}
		})
	}
}

// Two clients behind the same tunnel are two clients. Keying on the peer
// alone would put every visitor in one budget and 429 the fourth of them.
func TestClientKeySeparatesClientsBehindOneProxy(t *testing.T) {
	key := func(forwarded string) string {
		req := httptest.NewRequest("POST", "/api/lookup", nil)
		req.RemoteAddr = "127.0.0.1:51000"
		req.Header.Set("X-Forwarded-For", forwarded)
		return clientKey(req)
	}

	if a, b := key("203.0.113.9"), key("198.51.100.7"); a == b {
		t.Fatalf("both clients keyed as %q", a)
	}
}
