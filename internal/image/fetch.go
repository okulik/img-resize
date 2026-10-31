package image

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"

	"github.com/okulik/img-resize/internal/settings"
)

// Non-public ranges that netip.Addr has no predicate for.
var internalPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),     // "this" network
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),  // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),   // reserved
	netip.MustParsePrefix("64:ff9b::/96"),  // NAT64, embeds an IPv4 address
	netip.MustParsePrefix("2002::/16"),     // 6to4, embeds an IPv4 address
}

// Creates the HTTP client used for fetching images. Image URLs come from the
// caller, so unless internal URLs are explicitly allowed, the client refuses to
// connect to addresses that aren't publicly routable. The check is performed on
// the resolved address of each connection, which also covers redirects and
// hostnames resolving to an internal address.
func newHTTPClient(settings *settings.Settings) *http.Client {
	client := &http.Client{Timeout: settings.Http.ClientReadTimeout}
	if settings.Service.AllowInternalURLs {
		return client
	}

	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   rejectInternalAddress,
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = dialer.DialContext
	// With a proxy, the dialer would see the address of the proxy, not of the target.
	transport.Proxy = nil
	client.Transport = transport

	return client
}

func rejectInternalAddress(_ string, address string, _ syscall.RawConn) error {
	if isInternalAddress(address) {
		return fmt.Errorf("connecting to internal address %s is not allowed", address)
	}

	return nil
}

// Reports whether an address isn't publicly routable. An address that can't be
// parsed is considered internal.
func isInternalAddress(address string) bool {
	addrPort, err := netip.ParseAddrPort(address)
	if err != nil {
		return true
	}

	ip := addrPort.Addr().Unmap().WithZone("")
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return true
	}

	for _, prefix := range internalPrefixes {
		if prefix.Contains(ip) {
			return true
		}
	}

	return false
}
