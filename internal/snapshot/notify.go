package snapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/httputil"
	"github.com/obentoo/bentoolkit/internal/common/secrets"
	"github.com/obentoo/bentoolkit/internal/common/version"
)

// Notifier reports a completed run (R7.3, AD9). Story 004 shipped only the no-op
// default; story 005 added the ntfy/healthchecks/webhook drivers and story 008 the
// email driver, all composed behind multiNotifier by newNotifier.
type Notifier interface {
	Notify(ctx context.Context, res RunResult) error
}

// noopNotifier is the default Notifier: it accepts the result and does nothing.
type noopNotifier struct{}

func (noopNotifier) Notify(_ context.Context, _ RunResult) error { return nil }

// Compile-time assertion that noopNotifier satisfies Notifier.
var _ Notifier = noopNotifier{}

// newNotifier composes the notifier for cfg: it builds one driver per populated
// NotifyConfig sub-table (ntfy, healthchecks, webhook, email, in that order) and fans
// them out behind a single Notifier (R4.2). An empty config configures nothing and
// yields the no-op default. The (Notifier, error) signature is final — NewManager
// depends on it; it never returns a non-nil error today, but the error return is kept
// for signature stability.
//
// Neither the ntfy token nor the SMTP password is a config field: they resolve from
// BENTOO_NTFY_TOKEN and BENTOO_SMTP_PASSWORD via the secrets chain (env → user file
// → system file). Both resolve HERE, once per notifier rather than once per send, so
// an unreadable secrets file warns a single time instead of on every notification.
// An unreadable secrets file degrades to an unauthenticated notification (a logged
// warning), never a hard failure — so the resolution keeps the "never returns a
// non-nil error" contract above.
func newNotifier(cfg NotifyConfig) (Notifier, error) {
	var notifiers []Notifier
	if cfg.Ntfy.URL != "" {
		ntfyToken, _, err := secrets.Lookup("BENTOO_NTFY_TOKEN")
		if err != nil {
			warnLogf("snapshot: resolving ntfy token: %v; sending unauthenticated", err)
			ntfyToken = ""
		}
		notifiers = append(notifiers, ntfyNotifier{url: cfg.Ntfy.URL, token: ntfyToken})
	}
	if cfg.Healthchecks.PingURL != "" {
		notifiers = append(notifiers, healthchecksNotifier{pingURL: cfg.Healthchecks.PingURL, start: cfg.Healthchecks.Start})
	}
	if cfg.Webhook.URL != "" {
		notifiers = append(notifiers, webhookNotifier{url: cfg.Webhook.URL, headers: cfg.Webhook.Headers})
	}
	if len(cfg.Email.To) > 0 {
		notifiers = append(notifiers, emailNotifier{
			cfg:          cfg.Email,
			smtpPassword: resolveSMTPPassword(cfg.Email.SMTP.Host),
			runner:       defaultRunner(),
		})
	}

	if len(notifiers) == 0 {
		return noopNotifier{}, nil
	}
	return multiNotifier{notifiers: notifiers, on: cfg.On}, nil
}

// --- T2.1 shared HTTP helper ---

// notifyHTTPTimeout bounds each notifier HTTP request (R6.1).
const notifyHTTPTimeout = 15 * time.Second

// notifyMaxBodyBytes caps how many bytes are drained from a notifier response
// (R6.2). It is a var (defaulting to httputil.MaxBodyBytes) so tests can shrink it.
var notifyMaxBodyBytes = httputil.MaxBodyBytes

// notifierUserAgent returns the User-Agent applied to every notifier request — a
// descriptive UA avoids Go's default string that some upstreams reject (R6.1).
func notifierUserAgent() string { return "bentoolkit/" + version.Short() }

// notifierClient builds the http.Client shared by the notifier drivers, on
// httputil.BuildTransport() with a bounded timeout (R6.1).
func notifierClient() *http.Client {
	return &http.Client{Transport: httputil.BuildTransport(), Timeout: notifyHTTPTimeout}
}

// sendNotify performs req with the notifier client: it sets the User-Agent, sends
// the request (respecting req's context — R6.2), drains the response body bounded
// by notifyMaxBodyBytes, closes it, and returns an error on a non-2xx status. The
// response body is irrelevant to a notifier, so an oversized body is silently
// truncated rather than treated as a failure. The error never includes request
// headers or body, which may carry secrets (R6.3).
func sendNotify(client *http.Client, req *http.Request) error {
	req.Header.Set("User-Agent", notifierUserAgent())

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Drain the body bounded by notifyMaxBodyBytes so a connection can be reused
	// and an oversized response cannot exhaust memory; io.LimitReader truncates
	// silently because a notifier does not care about the response body (R6.2).
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, notifyMaxBodyBytes))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("notify: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// --- T2.2 ntfy driver ---

// ntfyNotifier posts a run summary to an ntfy topic URL (R1). Token, when set, is
// sent as a Bearer Authorization header and never logged (R1.3).
type ntfyNotifier struct {
	url   string
	token string
}

// Notify POSTs a human-readable summary of res to the ntfy topic. Priority and
// tags vary by outcome (R1.2): a successful run is normal priority, a failed run
// is elevated and tagged for alerting. The token, when set, authenticates via a
// Bearer header and is never interpolated into an error or log line (R1.3, R6.3).
func (n ntfyNotifier) Notify(ctx context.Context, res RunResult) error {
	summary := summarizeRun(res)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, strings.NewReader(summary))
	if err != nil {
		return err
	}

	if res.Failed() {
		req.Header.Set("X-Priority", "5")
		req.Header.Set("X-Tags", "rotating_light")
		req.Header.Set("X-Title", "Snapshot run failed")
	} else {
		req.Header.Set("X-Priority", "3")
		req.Header.Set("X-Tags", "white_check_mark")
		req.Header.Set("X-Title", "Snapshot run succeeded")
	}

	if n.token != "" {
		req.Header.Set("Authorization", "Bearer "+n.token)
	}

	if err := sendNotify(notifierClient(), req); err != nil {
		// The token lives only in the request header; never let it reach an
		// error string (R1.3, R6.3).
		return fmt.Errorf("ntfy notify: %w", err)
	}
	return nil
}

var _ Notifier = ntfyNotifier{}

// summarizeRun builds a human-readable one-paragraph summary of a completed run:
// overall status, the number of stages, how many failed, and the top-level error
// when present. Shared by the notifier drivers (ntfy here, healthchecks in T2.3)
// so the message body stays consistent (R1.1).
func summarizeRun(res RunResult) string {
	status := "succeeded"
	if res.Failed() {
		status = "FAILED"
	}

	failed := 0
	for _, s := range res.Stages {
		if s.Status == StatusFailed {
			failed++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Snapshot run %s: %d stage(s), %d failed.", status, len(res.Stages), failed)
	for _, s := range res.Stages {
		if s.Status == StatusFailed {
			fmt.Fprintf(&b, "\n- %s %s failed", s.Stage, s.Subvolume)
			if s.Err != "" {
				fmt.Fprintf(&b, ": %s", s.Err)
			}
		}
	}
	if res.Err != "" {
		fmt.Fprintf(&b, "\n%s", res.Err)
	}
	return b.String()
}

// --- T2.3 healthchecks driver ---

// healthchecksNotifier pings a healthchecks.io check (R2): the base PingURL on
// success, PingURL+"/fail" on failure, and optionally PingURL+"/start" before the
// run when Start is enabled.
type healthchecksNotifier struct {
	pingURL string
	start   bool
}

// Notify pings the healthchecks.io check for res: the base ping URL on success and
// the same URL with "/fail" appended on failure (R2.1, R2.2). It is a bare GET — a
// healthchecks ping carries no body. A single trailing "/" on pingURL is trimmed so
// the sub-path is appended cleanly.
func (n healthchecksNotifier) Notify(ctx context.Context, res RunResult) error {
	base := strings.TrimSuffix(n.pingURL, "/")
	target := base
	if res.Failed() {
		target = base + "/fail"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	if err := sendNotify(notifierClient(), req); err != nil {
		return fmt.Errorf("healthchecks notify: %w", err)
	}
	return nil
}

// Start pings the /start sub-path before the run when enabled (R2.3). It is not part
// of the one-shot Notifier interface; the Manager invokes it best-effort pre-run.
// When start is disabled it returns nil without issuing any request.
func (n healthchecksNotifier) Start(ctx context.Context) error {
	if !n.start {
		return nil
	}

	target := strings.TrimSuffix(n.pingURL, "/") + "/start"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	if err := sendNotify(notifierClient(), req); err != nil {
		return fmt.Errorf("healthchecks start: %w", err)
	}
	return nil
}

var _ Notifier = healthchecksNotifier{}

// --- T2.4 webhook driver ---

// webhookNotifier POSTs the serialized RunResult to a generic webhook URL (R3),
// applying any configured custom headers (R3.2).
type webhookNotifier struct {
	url     string
	headers map[string]string
}

// Notify POSTs res as JSON to the webhook URL (R3.1). Content-Type is set to
// application/json first, then any configured custom headers are applied (R3.2) —
// applying them last lets a caller deliberately override Content-Type. Custom
// header values may carry secrets, so they are never interpolated into an error
// (R6.3).
func (n webhookNotifier) Notify(ctx context.Context, res RunResult) error {
	body, err := json.Marshal(res)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, bytes.NewReader(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	for k, v := range n.headers {
		req.Header.Set(k, v)
	}

	if err := sendNotify(notifierClient(), req); err != nil {
		// Custom header values may carry secrets; never let them reach an error
		// string (R6.3).
		return fmt.Errorf("webhook notify: %w", err)
	}
	return nil
}

var _ Notifier = webhookNotifier{}

// --- T3.1 composite notifier + factory ---

// starter is implemented by notifiers that want a best-effort signal before the run
// begins (healthchecks /start, R2.3). It is separate from the one-shot Notifier
// interface; the Manager invokes it pre-run.
type starter interface {
	Start(ctx context.Context) error
}

// multiNotifier fans a single Notify out to every configured driver (R4.2). The
// outcome filter (on) is applied once up front (R4.3); per-notifier errors are
// logged as warnings and never abort the others or change the run (R5.2).
type multiNotifier struct {
	notifiers []Notifier
	on        []string
}

// Notify applies the outcome filter once, then fans out to every configured driver
// in order (R4.2, R4.3). When the run's outcome is not selected by on, no notifier is
// called. A non-nil error from any notifier is downgraded to a warning and the loop
// continues to the rest — notification is best-effort and never aborts the others or
// changes the run's exit (R5.2). The driver errors are already secret-free, so the
// error is safe to log verbatim. It always returns nil.
func (m multiNotifier) Notify(ctx context.Context, res RunResult) error {
	if !shouldNotify(m.on, res.Failed()) {
		return nil
	}
	for _, n := range m.notifiers {
		if err := n.Notify(ctx, res); err != nil {
			warnLogf("snapshot: notifier failed: %v", err)
		}
	}
	return nil
}

// Start fans the pre-run signal out to every configured notifier that implements
// starter (only healthchecks /start today, R2.3); the rest are skipped via the type
// assertion. A start error is downgraded to a warning and the loop continues. The
// outcome filter is deliberately not applied — /start is a pre-run signal, independent
// of the eventual outcome (R2.3). It always returns nil.
func (m multiNotifier) Start(ctx context.Context) error {
	for _, n := range m.notifiers {
		s, ok := n.(starter)
		if !ok {
			continue
		}
		if err := s.Start(ctx); err != nil {
			warnLogf("snapshot: notifier start failed: %v", err)
		}
	}
	return nil
}

var _ Notifier = multiNotifier{}

// --- 008 T1.1 email driver ---

// smtpSendMail is the injectable SMTP transport seam. It defaults to stdlib
// smtp.SendMail and is overridable in tests so the email driver runs without a
// real SMTP server (008 R1.1, A1) — the net/smtp analogue of the execCommand
// seam in runner.go.
var smtpSendMail = smtp.SendMail

// emailNotifier sends the run summary by email (008 R1). With SMTP.Host unset the
// message is piped to the local sendmail binary through the Runner seam; a
// populated SMTP.Host switches to direct SMTP via smtpSendMail (008 R1.1, A1).
// SMTP credentials never appear in argv, error strings, or logs (008 R1.3).
//
// smtpPassword carries the credential resolved once by newNotifier from
// BENTOO_SMTP_PASSWORD (017 R1.1). It lives on the notifier rather than on
// EmailConfig/SMTPConfig precisely because it is no longer configuration: nothing
// reads it from snapshot.toml (017 R2.1). Its zero value is the safe one — empty
// means send unauthenticated, whether the secret was absent (017 R1.2), its file
// was unreadable (017 R1.3), or this notifier takes the sendmail path at all.
type emailNotifier struct {
	cfg          EmailConfig
	smtpPassword string
	runner       Runner
}

// smtpPasswordEnv names the environment variable (and secrets-file key) that
// supplies the SMTP password (017 R1.1). It is a const shared with the migration
// diagnostic in config.go: that warning's entire job is to tell a user which
// variable to set, so it must never be able to name one this resolver does not
// actually read.
const smtpPasswordEnv = "BENTOO_SMTP_PASSWORD"

// resolveSMTPPassword resolves the SMTP credential from BENTOO_SMTP_PASSWORD
// through the secrets chain (017 R1.1), mirroring the ntfy token lookup above.
//
// It resolves only when host is non-empty — i.e. only when the SMTP transport is
// actually selected — so a user on the local sendmail path is never warned about a
// secrets file that path does not need.
//
// Lookup's three-way (value, found, err) contract collapses to one string here. A
// miss yields "" with found=false — the normal "no secret" case, which the PLAIN
// guard already reads as "send unauthenticated" (017 R1.2) — so found is discarded:
// "" and "not found" are the same instruction. A non-nil err means a secrets file
// exists but could not be read (secrets.ErrUnreadable); it warns and returns ""
// as well, degrading the notification to unauthenticated rather than aborting it
// (017 R1.3). The warning names the offending path, never a secret value.
func resolveSMTPPassword(host string) string {
	if host == "" {
		return ""
	}
	password, _, err := secrets.Lookup(smtpPasswordEnv)
	if err != nil {
		warnLogf("snapshot: resolving SMTP password: %v; sending unauthenticated", err)
		return ""
	}
	return password
}

// Notify composes the RFC-822-style message for res and hands it to the selected
// transport: SMTP when SMTP.Host is set, local sendmail otherwise (008 R1.1).
func (n emailNotifier) Notify(ctx context.Context, res RunResult) error {
	msg := n.message(res)
	if n.cfg.SMTP.Host != "" {
		return n.sendSMTP(msg)
	}
	return n.sendSendmail(ctx, msg)
}

// message builds the full RFC-822-style message: To/From/Subject headers, a blank
// line, then the shared summarizeRun body (008 R1.1). The Subject reflects the
// outcome (succeeded / FAILED) consistent with the other drivers. Multiple
// recipients are joined with ", " in the To header; the same list doubles as the
// SMTP envelope. Lines end in bare \n — the Unix convention sendmail expects, and
// net/smtp's data writer normalizes \n to \r\n on the wire.
func (n emailNotifier) message(res RunResult) []byte {
	subject := "Snapshot run succeeded"
	if res.Failed() {
		subject = "Snapshot run FAILED"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "To: %s\n", strings.Join(n.cfg.To, ", "))
	fmt.Fprintf(&b, "From: %s\n", n.cfg.From)
	fmt.Fprintf(&b, "Subject: %s\n", subject)
	b.WriteString("\n")
	b.WriteString(summarizeRun(res))
	b.WriteString("\n")
	return []byte(b.String())
}

// sendSendmail pipes msg to the local sendmail binary via the Runner seam
// (008 R1.1, A1). The single -t flag tells sendmail to read the recipients from
// the message headers; the message travels on stdin, never in argv.
func (n emailNotifier) sendSendmail(ctx context.Context, msg []byte) error {
	if _, err := n.runner.Run(ctx, "sendmail", []string{"-t"}, msg); err != nil {
		return fmt.Errorf("email notify: %w", err)
	}
	return nil
}

// sendSMTP sends msg via stdlib net/smtp through the smtpSendMail seam (008 R1.1).
// PLAIN auth is enabled only when SMTP.User is configured AND the password resolved
// from BENTOO_SMTP_PASSWORD is non-empty (nil auth otherwise) — the guard shape is
// unchanged, only the password's source moved from snapshot.toml to the secrets
// chain (017 R1.1, R2.1). An unset user therefore still means no auth, and an
// unresolvable password sends unauthenticated rather than failing (017 R1.2).
//
// The password is resolved once by newNotifier, not here: a per-send lookup would
// re-warn on every notification when the secrets file is unreadable. The
// credentials live only in the smtp.Auth value and are never interpolated into an
// error or log line (008 R1.3).
func (n emailNotifier) sendSMTP(msg []byte) error {
	var auth smtp.Auth
	if n.cfg.SMTP.User != "" && n.smtpPassword != "" {
		auth = smtp.PlainAuth("", n.cfg.SMTP.User, n.smtpPassword, n.cfg.SMTP.Host)
	}

	addr := net.JoinHostPort(n.cfg.SMTP.Host, strconv.Itoa(n.cfg.SMTP.Port))
	if err := smtpSendMail(addr, auth, n.cfg.From, n.cfg.To, msg); err != nil {
		// The password lives only in the auth value; never let it reach an
		// error string (008 R1.3).
		return fmt.Errorf("email notify: %w", err)
	}
	return nil
}

var _ Notifier = emailNotifier{}
