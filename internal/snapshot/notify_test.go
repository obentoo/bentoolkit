package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// roundTripFunc adapts a function to http.RoundTripper so a test can serve a
// canned response without a real server.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// countingReadCloser counts bytes read from the underlying reader and records
// whether Close was called — used to prove a response body is bounded and drained.
type countingReadCloser struct {
	r      io.Reader
	read   int
	closed bool
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += n
	return n, err
}

func (c *countingReadCloser) Close() error { c.closed = true; return nil }

func TestNotifierClient_HasTimeoutAndTransport(t *testing.T) {
	c := notifierClient()
	if c.Timeout <= 0 {
		t.Errorf("notifierClient().Timeout = %v, want > 0 (R6.1)", c.Timeout)
	}
	if c.Transport == nil {
		t.Error("notifierClient().Transport is nil, want httputil.BuildTransport() (R6.1)")
	}
}

func TestSendNotify_SetsUserAgentAndAccepts2xx(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := sendNotify(notifierClient(), req); err != nil {
		t.Fatalf("sendNotify on 2xx: %v", err)
	}
	if !strings.HasPrefix(gotUA, "bentoolkit/") {
		t.Errorf("User-Agent = %q, want a bentoolkit/ prefix (R6.1)", gotUA)
	}
}

func TestSendNotify_Non2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := sendNotify(notifierClient(), req); err == nil {
		t.Fatal("sendNotify on 500: want error, got nil")
	}
}

func TestSendNotify_BoundsAndDrainsBody(t *testing.T) {
	orig := notifyMaxBodyBytes
	t.Cleanup(func() { notifyMaxBodyBytes = orig })
	notifyMaxBodyBytes = 8

	body := &countingReadCloser{r: strings.NewReader(strings.Repeat("x", 100))}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header)}, nil
	})}

	req, err := http.NewRequest(http.MethodPost, "http://example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := sendNotify(client, req); err != nil {
		t.Fatalf("sendNotify: %v", err)
	}
	if !body.closed {
		t.Error("response body was not closed")
	}
	if int64(body.read) > notifyMaxBodyBytes {
		t.Errorf("read %d bytes from body, want <= %d (bounded, R6.2)", body.read, notifyMaxBodyBytes)
	}
}

func TestSendNotify_ContextCancellationAborts(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // block until the test ends, proving cancellation is what aborts
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the request is issued

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := sendNotify(notifierClient(), req); err == nil {
		t.Fatal("sendNotify with a cancelled context: want error, got nil (R6.2)")
	}
}

// --- T2.2 ntfy driver ---

// okStage / failStage build representative RunResults for the driver tests.
func okRun() RunResult {
	return RunResult{Stages: []StageResult{{Subvolume: "/home", Stage: StageCreate, Status: StatusOK}}}
}

func failRun() RunResult {
	return RunResult{
		Stages: []StageResult{{Subvolume: "/home", Stage: StageCreate, Status: StatusFailed, Err: "subvolume not found"}},
		Err:    "create /home failed: subvolume not found",
	}
}

func TestNtfyNotifier_SuccessNormalPriority(t *testing.T) {
	var gotPriority, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPriority = r.Header.Get("X-Priority")
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := ntfyNotifier{url: srv.URL, token: "tk_secret"}
	if err := n.Notify(context.Background(), okRun()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if gotPriority != "3" {
		t.Errorf("X-Priority = %q, want 3 (normal on success, R1.2)", gotPriority)
	}
	if gotAuth != "Bearer tk_secret" {
		t.Errorf("Authorization = %q, want Bearer tk_secret (R1.3)", gotAuth)
	}
	if gotBody == "" {
		t.Error("ntfy message body is empty, want a run summary (R1.1)")
	}
}

func TestNtfyNotifier_FailureHighPriorityAndTag(t *testing.T) {
	var gotPriority, gotTags string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPriority = r.Header.Get("X-Priority")
		gotTags = r.Header.Get("X-Tags")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := ntfyNotifier{url: srv.URL}
	if err := n.Notify(context.Background(), failRun()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if gotPriority != "5" {
		t.Errorf("X-Priority = %q, want 5 (elevated on failure, R1.2)", gotPriority)
	}
	if !strings.Contains(gotTags, "rotating_light") {
		t.Errorf("X-Tags = %q, want a failure tag (rotating_light, R1.2)", gotTags)
	}
}

func TestNtfyNotifier_NoTokenOmitsAuthHeader(t *testing.T) {
	var hasAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hasAuth = r.Header["Authorization"]
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := ntfyNotifier{url: srv.URL} // no token
	if err := n.Notify(context.Background(), okRun()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if hasAuth {
		t.Error("Authorization header present with no token configured")
	}
}

func TestNtfyNotifier_TokenNeverInError(t *testing.T) {
	const secret = "tk_supersecret_value"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // force an error path
	}))
	defer srv.Close()

	n := ntfyNotifier{url: srv.URL, token: secret}
	err := n.Notify(context.Background(), failRun())
	if err == nil {
		t.Fatal("want an error on 500")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error string leaked the token: %v (R1.3, R6.3)", err)
	}
}

// --- T2.3 healthchecks driver ---

func TestHealthchecksNotifier_SuccessPingsBase(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := healthchecksNotifier{pingURL: srv.URL + "/check-uuid"}
	if err := n.Notify(context.Background(), okRun()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if gotPath != "/check-uuid" {
		t.Errorf("success pinged %q, want base /check-uuid (R2.1)", gotPath)
	}
}

func TestHealthchecksNotifier_FailurePingsFail(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := healthchecksNotifier{pingURL: srv.URL + "/check-uuid"}
	if err := n.Notify(context.Background(), failRun()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if gotPath != "/check-uuid/fail" {
		t.Errorf("failure pinged %q, want /check-uuid/fail (R2.2)", gotPath)
	}
}

func TestHealthchecksNotifier_StartPingsStartWhenEnabled(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := healthchecksNotifier{pingURL: srv.URL + "/check-uuid", start: true}
	if err := n.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if gotPath != "/check-uuid/start" {
		t.Errorf("Start pinged %q, want /check-uuid/start (R2.3)", gotPath)
	}
}

func TestHealthchecksNotifier_StartNoopWhenDisabled(t *testing.T) {
	var pinged bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pinged = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := healthchecksNotifier{pingURL: srv.URL + "/check-uuid", start: false}
	if err := n.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if pinged {
		t.Error("Start pinged the server with start=false (R2.3)")
	}
}

// --- T2.4 webhook driver ---

func TestWebhookNotifier_PostsRunResultJSON(t *testing.T) {
	var gotBody []byte
	var gotCT, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res := failRun()
	n := webhookNotifier{url: srv.URL}
	if err := n.Notify(context.Background(), res); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST (R3.1)", gotMethod)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q, want application/json (R3.1)", gotCT)
	}
	var decoded RunResult
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("body is not the serialized RunResult JSON: %v (body=%q)", err, gotBody)
	}
	if decoded.Err != res.Err || len(decoded.Stages) != len(res.Stages) {
		t.Errorf("decoded RunResult = %+v, want Err=%q with %d stages", decoded, res.Err, len(res.Stages))
	}
}

func TestWebhookNotifier_AppliesCustomHeaders(t *testing.T) {
	var gotAuth, gotCustom string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCustom = r.Header.Get("X-Custom")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := webhookNotifier{
		url:     srv.URL,
		headers: map[string]string{"Authorization": "Bearer z", "X-Custom": "v"},
	}
	if err := n.Notify(context.Background(), okRun()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if gotAuth != "Bearer z" {
		t.Errorf("Authorization = %q, want Bearer z (R3.2)", gotAuth)
	}
	if gotCustom != "v" {
		t.Errorf("X-Custom = %q, want v (R3.2)", gotCustom)
	}
}

// --- T3.1 composite notifier + factory ---

// spyNotifier records how many times Notify was called and returns a canned error.
type spyNotifier struct {
	calls int
	err   error
}

func (s *spyNotifier) Notify(_ context.Context, _ RunResult) error {
	s.calls++
	return s.err
}

// spyStarter is a spyNotifier that also implements the starter interface.
type spyStarter struct {
	spyNotifier
	startCalls int
}

func (s *spyStarter) Start(_ context.Context) error {
	s.startCalls++
	return nil
}

func TestMultiNotifier_FansOutToAll(t *testing.T) {
	a, b := &spyNotifier{}, &spyNotifier{}
	m := multiNotifier{notifiers: []Notifier{a, b}, on: []string{"failure"}}

	if err := m.Notify(context.Background(), failRun()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if a.calls != 1 || b.calls != 1 {
		t.Errorf("fan-out calls a=%d b=%d, want 1,1 (R4.2)", a.calls, b.calls)
	}
}

func TestMultiNotifier_OneErrorsOthersStillCalledAndWarn(t *testing.T) {
	var warnings int
	origWarn := warnLogf
	t.Cleanup(func() { warnLogf = origWarn })
	warnLogf = func(string, ...interface{}) { warnings++ }

	bad := &spyNotifier{err: errors.New("boom")}
	good := &spyNotifier{}
	m := multiNotifier{notifiers: []Notifier{bad, good}, on: []string{"failure"}}

	if err := m.Notify(context.Background(), failRun()); err != nil {
		t.Errorf("Notify returned %v, want nil (best-effort, R5.2)", err)
	}
	if good.calls != 1 {
		t.Error("the second notifier was not called after the first errored (R5.2)")
	}
	if warnings == 0 {
		t.Error("a failing notifier should emit a warning (R5.2)")
	}
}

func TestMultiNotifier_OnFilterSuppressesAll(t *testing.T) {
	a, b := &spyNotifier{}, &spyNotifier{}
	m := multiNotifier{notifiers: []Notifier{a, b}, on: []string{"failure"}}

	// Success outcome with on=failure-only must skip every notifier (R4.3).
	if err := m.Notify(context.Background(), okRun()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if a.calls != 0 || b.calls != 0 {
		t.Errorf("on-filter did not suppress: a=%d b=%d (R4.3)", a.calls, b.calls)
	}
}

func TestMultiNotifier_StartFansOutToStartersOnly(t *testing.T) {
	plain := &spyNotifier{}
	hc := &spyStarter{}
	m := multiNotifier{notifiers: []Notifier{plain, hc}}

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if hc.startCalls != 1 {
		t.Errorf("starter.Start calls = %d, want 1 (R2.3 wiring)", hc.startCalls)
	}
}

func TestNewNotifier_BuildsConfiguredDrivers(t *testing.T) {
	cfg := NotifyConfig{
		On:      []string{"failure"},
		Ntfy:    NtfyConfig{URL: "https://ntfy.sh/topic"},
		Webhook: WebhookConfig{URL: "https://example.com/hook"},
	}
	n, err := newNotifier(cfg)
	if err != nil {
		t.Fatalf("newNotifier: %v", err)
	}
	m, ok := n.(multiNotifier)
	if !ok {
		t.Fatalf("newNotifier returned %T, want multiNotifier", n)
	}
	if len(m.notifiers) != 2 {
		t.Errorf("built %d notifiers, want 2 (ntfy + webhook configured)", len(m.notifiers))
	}
}
