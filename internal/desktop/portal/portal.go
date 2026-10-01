// Package portal opens a notice's URL in the user's browser: through the
// desktop portal's OpenURI when it answers, through xdg-open when it
// definitely did not act (S072-R7).
package portal

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"
)

// The desktop portal's well-known name, object path, OpenURI method and the
// Request interface its asynchronous answer arrives on.
const (
	portalName     = "org.freedesktop.portal.Desktop"
	portalPath     = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	openURIMethod  = "org.freedesktop.portal.OpenURI.OpenURI"
	requestIface   = "org.freedesktop.portal.Request"
	responseMember = "Response"
	responseSignal = requestIface + "." + responseMember
)

// Request.Response codes. A code the specification does not define is
// treated like responseFailed.
const (
	responseSuccess   = 0
	responseCancelled = 1
	responseFailed    = 2
)

// fallbackCommand is the program run when the portal did not act (R7.2).
const fallbackCommand = "xdg-open"

// portalTimeout bounds the wait for the OpenURI reply. A D-Bus call cannot be
// withdrawn: a portal that is slow (being activated, say) may still act on
// it later. So a call that times out is reported as an error and is never
// followed by xdg-open, which could open the URL a second time.
const portalTimeout = 10 * time.Second

// responseWindow is how long Open listens, after the portal accepted the
// call, for a Request.Response reporting the outcome. The portal replies with
// the request handle first and does the work afterwards; its failures (an
// invalid URI, no handler, a launch error) come back within milliseconds,
// while success may wait on an application chooser the user keeps open. So a
// failure inside the window falls back, and silence past it counts as
// accepted, never as a failure: falling back then could open the URL twice.
const responseWindow = 500 * time.Millisecond

// signalBuffer is the capacity of the channel registered with conn.Signal for
// one Open. godbus fans every signal on the connection out to it and parks a
// goroutine per signal when it is full, so it is buffered and drained.
const signalBuffer = 16

// cleanupTimeout bounds the RemoveMatch sent when Open is done listening.
const cleanupTimeout = 2 * time.Second

// ErrURLRefused is returned by Open for a URL that is not https on exactly
// the allowed host, or whose query or fragment is not well-formed (R7.3).
// Nothing is opened.
var ErrURLRefused = errors.New("URL refused")

// ErrCancelled is returned by Open when the user dismissed the portal's
// application chooser. Nothing was opened, so the notice is not to be marked
// read (R7.4), and xdg-open is not run: the user chose not to open it.
var ErrCancelled = errors.New("opening cancelled by the user")

// RunFunc runs the program name with args, without a shell.
type RunFunc func(ctx context.Context, name string, args ...string) error

// activationTokenKey carries the activation token from Open to the RunFunc.
type activationTokenKey struct{}

// ExecRun is the production RunFunc: it runs name with args as separate argv
// elements, never through a shell, and waits for it to exit. The child never
// inherits this process's own XDG_ACTIVATION_TOKEN or DESKTOP_STARTUP_ID,
// which are stale (spent at its own launch); when Open passes an activation
// token, the child gets that one as XDG_ACTIVATION_TOKEN, so the browser it
// starts may take focus.
func ExecRun(ctx context.Context, name string, args ...string) error {
	// G204: Open passes only the fixed fallbackCommand, and the URL travels as
	// one argv element with no shell in between, so nothing in it is parsed.
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: see above
	token, _ := ctx.Value(activationTokenKey{}).(string)
	cmd.Env = childEnv(os.Environ(), token)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running %s: %w", name, err)
	}
	return nil
}

// childEnv returns environ without any activation variable, plus
// XDG_ACTIVATION_TOKEN=token when token is not empty.
func childEnv(environ []string, token string) []string {
	env := make([]string, 0, len(environ)+1)
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if name == "XDG_ACTIVATION_TOKEN" || name == "DESKTOP_STARTUP_ID" {
			continue
		}
		env = append(env, kv)
	}
	if token != "" {
		env = append(env, "XDG_ACTIVATION_TOKEN="+token)
	}
	return env
}

// Opener opens notice URLs on one session bus connection. It is safe for
// concurrent use.
type Opener struct {
	conn        *dbus.Conn
	allowedHost string
	log         *slog.Logger
	run         RunFunc
}

// New returns an Opener that opens only https URLs whose host (with its port,
// if any) equals allowedHost, compared case-insensitively. An empty
// allowedHost refuses every URL (the feed URL itself was refused, R2.9). A nil
// log discards; a nil run is ExecRun.
func New(conn *dbus.Conn, allowedHost string, log *slog.Logger, run func(ctx context.Context, name string, args ...string) error) *Opener {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if run == nil {
		run = ExecRun
	}
	return &Opener{conn: conn, allowedHost: allowedHost, log: log, run: run}
}

// Open opens rawURL.
//
// A URL that is not https on the allowed host, or is malformed, returns
// ErrURLRefused and nothing runs (R7.3; the caller, which knows the notice
// ID, logs the WARN). Otherwise the portal's OpenURI is asked to open it,
// with token as the activation token when it is not empty (R7.1).
//
// xdg-open runs, with the URL as its only argument, only when the portal
// definitely did not act: the call got a D-Bus error reply (no portal, no
// such method, lockdown...) or the portal answered the request with a
// failure (R7.2). It then fails only when xdg-open fails too, with both
// causes joined. When the portal's fate is unknown (no reply in time), Open
// returns an error and does not fall back. A dismissed chooser returns
// ErrCancelled.
func (o *Opener) Open(ctx context.Context, rawURL, token string) error {
	target, err := o.check(rawURL)
	if err != nil {
		return err
	}

	fallback, portalErr := o.openURI(ctx, target, token)
	switch {
	case portalErr == nil:
		return nil
	case errors.Is(portalErr, ErrCancelled):
		return portalErr
	case !fallback:
		o.log.Warn("desktop portal did not answer; not running xdg-open, which could open the URL twice",
			"url", target, "err", portalErr)
		return portalErr
	}
	if runErr := o.run(context.WithValue(ctx, activationTokenKey{}, token), fallbackCommand, target); runErr != nil {
		return errors.Join(portalErr, fmt.Errorf("opening %s with %s: %w", target, fallbackCommand, runErr))
	}
	o.log.Warn("desktop portal could not open the URL; opened it with xdg-open",
		"url", target, "err", portalErr)
	return nil
}

// check parses rawURL and returns its canonical form when it is https on
// exactly the allowed host with a well-formed query and fragment,
// ErrURLRefused otherwise. The canonical form, not rawURL, is what gets
// opened, so the consumer sees what was checked.
func (o *Opener) check(rawURL string) (string, error) {
	if !utf8.ValidString(rawURL) {
		return "", fmt.Errorf("%w: %q is not valid UTF-8", ErrURLRefused, rawURL)
	}
	u, err := url.Parse(rawURL)
	switch {
	case err != nil:
		return "", fmt.Errorf("%w: %q does not parse: %w", ErrURLRefused, rawURL, err)
	case u.Scheme != "https" || u.Opaque != "":
		return "", fmt.Errorf("%w: %q is not an https URL", ErrURLRefused, rawURL)
	case u.User != nil:
		return "", fmt.Errorf("%w: %q carries user information", ErrURLRefused, rawURL)
	case o.allowedHost == "" || !strings.EqualFold(u.Host, o.allowedHost):
		return "", fmt.Errorf("%w: %q is not on the feed host %q", ErrURLRefused, rawURL, o.allowedHost)
	}
	// url.Parse validates the path's escapes but passes the query through
	// raw, and u.String() would hand it on unchanged; the fragment is checked
	// as written too. Both are cut exactly where url.Parse cuts them.
	beforeFragment, fragment, _ := strings.Cut(rawURL, "#")
	_, query, _ := strings.Cut(beforeFragment, "?")
	if !wellFormedComponent(query) || !wellFormedComponent(fragment) {
		return "", fmt.Errorf("%w: %q has a query or fragment that is not RFC 3986", ErrURLRefused, rawURL)
	}
	return u.String(), nil
}

// wellFormedComponent reports whether s is a valid RFC 3986 query or
// fragment: unreserved characters, sub-delims, ":", "@", "/", "?" and
// complete %-escapes only.
func wellFormedComponent(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '%':
			if i+2 >= len(s) || !isHex(s[i+1]) || !isHex(s[i+2]) {
				return false
			}
			i += 2
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.IndexByte("-._~!$&'()*+,;=:@/?", c) >= 0:
		default:
			return false
		}
	}
	return true
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

// openURI asks the desktop portal to open target and reports the outcome:
// nil when the portal accepted it and did not report a failure within
// responseWindow; otherwise an error, with fallback true only when the
// portal definitely did not act.
func (o *Opener) openURI(ctx context.Context, target, token string) (fallback bool, err error) {
	// Listen before calling: the portal may answer the request before its
	// reply to the call has been read.
	signals := make(chan *dbus.Signal, signalBuffer)
	o.conn.Signal(signals)
	defer o.conn.RemoveSignal(signals)
	match := []dbus.MatchOption{
		dbus.WithMatchSender(portalName),
		dbus.WithMatchInterface(requestIface),
		dbus.WithMatchMember(responseMember),
	}
	callCtx, cancel := context.WithTimeout(ctx, portalTimeout)
	defer cancel()
	if err := o.conn.AddMatchSignalContext(callCtx, match...); err != nil {
		// The portal has not been called yet.
		return true, fmt.Errorf("listening for %s answers: %w", portalName, err)
	}
	defer o.removeMatch(ctx, match)

	options := map[string]dbus.Variant{"handle_token": dbus.MakeVariant(handleToken())}
	if token != "" {
		options["activation_token"] = dbus.MakeVariant(token)
	}
	var handle dbus.ObjectPath
	call := o.conn.Object(portalName, portalPath).CallWithContext(callCtx, openURIMethod, 0, "", target, options)
	if call.Err != nil {
		err := fmt.Errorf("opening %s through %s: %w", target, portalName, call.Err)
		return isErrorReply(call.Err), err
	}
	if err := call.Store(&handle); err != nil {
		// The portal accepted the call; only its reply is odd. Listening
		// is impossible without the handle, so the call counts as accepted.
		o.log.Debug("desktop portal reply carries no request handle", "url", target, "err", err)
		return false, nil
	}

	// Signals are matched by the portal's unique name: the bus routes a
	// signal sent straight to this connection whatever its sender.
	var owner string
	if err := o.conn.BusObject().CallWithContext(callCtx, "org.freedesktop.DBus.GetNameOwner", 0, portalName).Store(&owner); err != nil {
		o.log.Debug("desktop portal owner unknown; not waiting for its answer", "url", target, "err", err)
		return false, nil
	}
	return o.awaitResponse(ctx, signals, handle, owner, target)
}

// awaitResponse waits up to responseWindow for the Request.Response on
// handle sent by owner. The handle the portal returned is used, not the one
// handle_token predicts: older portals ignore handle_token.
func (o *Opener) awaitResponse(ctx context.Context, signals <-chan *dbus.Signal, handle dbus.ObjectPath, owner, target string) (fallback bool, err error) {
	window := time.NewTimer(responseWindow)
	defer window.Stop()
	for {
		select {
		case sig, ok := <-signals:
			if !ok {
				// The connection closed after the portal accepted the call.
				return false, nil
			}
			if sig.Name != responseSignal || sig.Path != handle || sig.Sender != owner || len(sig.Body) == 0 {
				continue
			}
			code, ok := sig.Body[0].(uint32)
			if !ok {
				continue
			}
			switch code {
			case responseSuccess:
				return false, nil
			case responseCancelled:
				return false, fmt.Errorf("opening %s through %s: %w", target, portalName, ErrCancelled)
			case responseFailed:
				return true, fmt.Errorf("opening %s through %s: the portal reported a failure", target, portalName)
			default:
				return true, fmt.Errorf("opening %s through %s: the portal answered with unknown code %d", target, portalName, code)
			}
		case <-window.C:
			return false, nil
		case <-ctx.Done():
			// The portal accepted the call; its outcome is just not known.
			return false, nil
		}
	}
}

// removeMatch drops the match rule openURI added. It runs after ctx may have
// ended, so it gets a bound of its own.
func (o *Opener) removeMatch(ctx context.Context, match []dbus.MatchOption) {
	rmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if err := o.conn.RemoveMatchSignalContext(rmCtx, match...); err != nil {
		o.log.Debug("removing the desktop portal match rule", "err", err)
	}
}

// handleToken returns a fresh handle_token: a valid object path element.
func handleToken() string {
	return "bentoo_" + rand.Text()
}

// isErrorReply reports whether err is an error reply from the bus or the
// portal, which means the portal did not act on the call. A cancelled or
// timed-out call is not: the message may still be delivered and acted on.
func isErrorReply(err error) bool {
	var value dbus.Error
	var pointer *dbus.Error
	return errors.As(err, &value) || errors.As(err, &pointer)
}
