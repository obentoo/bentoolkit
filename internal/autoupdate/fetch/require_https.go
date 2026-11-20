package fetch

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ErrInsecureURL marks a version source reached over plain http. Anyone on
// the network path of such a request can answer it, and so choose the
// version a bump goes to.
var ErrInsecureURL = errors.New("version source is not https")

// RequireHTTPS refuses an http:// URL unless its host is loopback, where no
// network path exists to tamper with (and where tests serve fixtures). Other
// schemes and unparseable URLs pass through: the code that uses the URL
// reports them in its own words.
func RequireHTTPS(rawURL string) error {
	u, parseErr := url.Parse(rawURL)
	if parseErr != nil {
		u = nil
	}
	if u == nil || !strings.EqualFold(u.Scheme, "http") || isLoopbackHost(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("%w: http://%s can be answered by anyone on the network path, "+
		"who would then choose the version; use https://", ErrInsecureURL, u.Host)
}

// isLoopbackHost reports whether host names this machine.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
