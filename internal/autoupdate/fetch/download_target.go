package fetch

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// downloadTargetGuard decides whether the URL a vendor's endpoint answered
// with (fetch_response = "url") may be fetched. The vendor's reply is
// untrusted: pointed at loopback, a private network or a link-local address
// (a cloud metadata service), the download would read a local service and
// write its answer into the distdir as if it were the release.
type downloadTargetGuard interface {
	checkURL(u *url.URL) error
	checkIP(ip net.IP) error
}

// publicHTTPSGuard is the production guard: https only, to a public address.
type publicHTTPSGuard struct{}

func (publicHTTPSGuard) checkURL(u *url.URL) error {
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("%w: the download URL the endpoint returned is %s://, not https", ErrAuthFetchFailed, u.Scheme)
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return fmt.Errorf("%w: the download URL the endpoint returned names %s, this machine", ErrAuthFetchFailed, host)
	}
	if ip := net.ParseIP(host); ip != nil {
		return publicHTTPSGuard{}.checkIP(ip)
	}
	return nil
}

func (publicHTTPSGuard) checkIP(ip net.IP) error {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("%w: the download URL the endpoint returned leads to %s, a loopback, private or link-local address",
			ErrAuthFetchFailed, ip)
	}
	return nil
}

// downloadGuard is the guard followDownloadURL applies. Only tests replace
// it, to download from a loopback fixture server.
var downloadGuard downloadTargetGuard = publicHTTPSGuard{}

// downloadClient returns the client for the download leg. Without a proxy its
// dialer re-applies the guard to the address the name resolved to, so a
// public-looking name that resolves (or rebinds) to a private address is
// refused too. Through a proxy the dialed address is the proxy's, so only the
// URL check applies.
func downloadClient(target *url.URL, timeout time.Duration, checkRedirect func(*http.Request, []*http.Request) error) *http.Client {
	base, _ := http.DefaultTransport.(*http.Transport)
	transport := base.Clone()
	proxied, err := transport.Proxy(&http.Request{URL: target})
	if err == nil && proxied == nil {
		dialer := &net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			Control: func(_, address string, _ syscall.RawConn) error {
				host, _, splitErr := net.SplitHostPort(address)
				if splitErr != nil {
					return fmt.Errorf("%w: unexpected dial address %q", ErrAuthFetchFailed, address)
				}
				ip := net.ParseIP(host)
				if ip == nil {
					return fmt.Errorf("%w: dial address %q is not an IP", ErrAuthFetchFailed, address)
				}
				return downloadGuard.checkIP(ip)
			},
		}
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		}
	}
	return &http.Client{Timeout: timeout, Transport: transport, CheckRedirect: checkRedirect}
}
