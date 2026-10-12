package autoupdate

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fetch"
)

// s090CancelOnHead interrupts the run when the vendor URI is probed, the way
// Ctrl+C lands while a HEAD is in flight.
type s090CancelOnHead struct {
	next   http.RoundTripper
	uri    string
	cancel context.CancelFunc
}

func (c s090CancelOnHead) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodHead && req.URL.String() == c.uri {
		c.cancel()
		<-req.Context().Done()
		return nil, req.Context().Err()
	}
	return c.next.RoundTrip(req)
}

// A probe cut short because the run was interrupted is not a failed probe:
// no WARN blames the upstream and the outcome stays zero.
func TestVendorProbeCancelledIsNotAFailure(t *testing.T) {
	f := s090ProbeCase{name: "cancelled", head: s090Resp{status: http.StatusNotFound}}.fixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	client := fetch.NewRetryableHTTPClient().WithCircuitBreaker(false)
	client.SetHTTPClient(&http.Client{Transport: s090CancelOnHead{next: f.tr, uri: f.vendorURI, cancel: cancel}})
	client.SetDelayFunc(func(time.Duration) {})
	sw := newSweeper(f.overlay,
		withSweeperExec(f.exec.fn),
		withSweeperHTTPClient(client),
		withSweeperLogger(f.sink.logger()),
		withSweeperReporter(f.rec),
	)

	out, err := sw.prepareVendorDistfile(ctx, f.stagedRoot, s090Pkg, f.version, f.distdir)
	if err != nil {
		t.Fatalf("prepareVendorDistfile on a cancelled run = %v, want nil", err)
	}
	if out != (VendorOutcome{}) {
		t.Fatalf("outcome on a cancelled run = %+v, want zero", out)
	}
	if s090ProbeLogged(f.sink, slog.LevelWarn, f.vendorURI) {
		t.Fatal("a cancelled probe was logged at WARN as a probe failure")
	}
}
