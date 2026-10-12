package hjem

import (
	"net/http"
	"net/netip"
	"strings"
)

// clientKey identifies the caller a lookup is charged to.
//
// X-Forwarded-For is a list each hop appends to, so everything to the left of
// the last entry is whatever the client chose to send: a budget keyed on the
// leftmost entry is a budget the client resets by changing a header. The
// rightmost entry is the address our own immediate proxy observed, and is the
// one place in the chain the client cannot write.
//
// That only holds when the request really did arrive through a proxy of ours,
// hence the peer test. The deployment terminates behind a tunnel whose client
// runs alongside the server, so the connection comes from loopback or a
// private address. A request that reaches us straight from a public address
// has no proxy we trust in front of it, and its header is ignored entirely.
func clientKey(r *http.Request) string {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		// Not an address we can reason about, but still one caller; keying on
		// the raw string keeps it accounted for rather than exempt.
		return r.RemoteAddr
	}

	addr := peer.Addr().Unmap()
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() {
		if fwd, ok := forwardedClient(r.Header.Values("X-Forwarded-For")); ok {
			return fwd.String()
		}
	}

	return addr.String()
}

// forwardedClient reads the last entry of the last X-Forwarded-For header.
// Repeated headers are one list split across lines, so the final entry of the
// final line is the most recently appended one either way.
//
// An entry that does not parse is not searched past: the rightmost one is
// written by a proxy and is always an address, so anything else means the
// chain is not the shape assumed here — and stepping further left to find
// something parseable would be reading exactly the part the client controls.
func forwardedClient(values []string) (netip.Addr, bool) {
	if len(values) == 0 {
		return netip.Addr{}, false
	}

	chain := values[len(values)-1]
	last := strings.TrimSpace(chain[strings.LastIndex(chain, ",")+1:])

	addr, err := netip.ParseAddr(last)
	if err != nil {
		return netip.Addr{}, false
	}

	return addr.Unmap(), true
}
