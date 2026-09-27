package snapshot

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/smtp"
	"strings"
	"sync"
	"testing"
	"time"
)

// s053ReviewSMTP serves one session: it greets, answers every line with the
// reply reply(line) returns, and records what the client sent. A "" reply
// means stay silent.
func s053ReviewSMTP(t *testing.T, reply func(line string) string) (addr string, seen func() []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var mu sync.Mutex
	var lines []string
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		w := bufio.NewWriter(c)
		w.WriteString("220 test ESMTP\r\n")
		w.Flush()
		r := bufio.NewReader(c)
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				return
			}
			l = strings.TrimRight(l, "\r\n")
			mu.Lock()
			lines = append(lines, l)
			mu.Unlock()
			if resp := reply(l); resp != "" {
				w.WriteString(resp + "\r\n")
				w.Flush()
			}
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String(), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), lines...)
	}
}

// TestSendMailBounded_HelloErrorIsReported pins the review fix: a greeting that
// stalls at EHLO is reported as the hello step's own error, not as a missing
// AUTH extension.
func TestSendMailBounded_HelloErrorIsReported(t *testing.T) {
	orig := smtpTimeout
	smtpTimeout = 300 * time.Millisecond
	t.Cleanup(func() { smtpTimeout = orig })
	addr, _ := s053ReviewSMTP(t, func(string) string { return "" })
	auth := smtp.PlainAuth("", "u", "SECRETPW", "127.0.0.1")
	err := sendMailBounded(t.Context(), addr, auth, "a@example.org", []string{"b@example.org"}, []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "smtp hello") {
		t.Fatalf("err = %v, want the hello step's error", err)
	}
	if strings.Contains(err.Error(), "AUTH") || strings.Contains(err.Error(), "SECRETPW") {
		t.Errorf("err %q blames AUTH or carries the password", err)
	}
}

// TestSendMailBounded_RefusesCRLFBeforeDialing pins the review fix: as in
// smtp.SendMail, an address with CR or LF is refused before any byte, let
// alone a credential, reaches the server.
func TestSendMailBounded_RefusesCRLFBeforeDialing(t *testing.T) {
	addr, seen := s053ReviewSMTP(t, func(string) string { return "250 ok" })
	auth := smtp.PlainAuth("", "u", "SECRETPW", "127.0.0.1")
	for _, tc := range []struct{ from, to string }{{"a@x\r\nRCPT TO:<evil>", "b@x"}, {"a@x", "b@x\nDATA"}} {
		if err := sendMailBounded(t.Context(), addr, auth, tc.from, []string{tc.to}, []byte("x")); err == nil {
			t.Errorf("from %q to %q: err = nil, want a refusal", tc.from, tc.to)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if l := seen(); len(l) != 0 {
		t.Errorf("server saw %q, want nothing", l)
	}
}

// TestSendMailBounded_CancelIsContextCanceled pins R8.2 at the error: a
// cancelled session returns an error for which errors.Is(context.Canceled)
// holds.
func TestSendMailBounded_CancelIsContextCanceled(t *testing.T) {
	addr, _ := s053ReviewSMTP(t, func(string) string { return "" })
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	err := sendMailBounded(ctx, addr, nil, "a@example.org", []string{"b@example.org"}, []byte("x"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want errors.Is(err, context.Canceled)", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("cancel took %v, want < 1s", d)
	}
}

// TestSendMailBounded_FailedAuthCarriesNoCloseNoise pins the review fix: a
// rejected AUTH returns the server's reply, not a second "use of closed
// network connection" from closing what Auth already closed.
func TestSendMailBounded_FailedAuthCarriesNoCloseNoise(t *testing.T) {
	addr, _ := s053ReviewSMTP(t, func(l string) string {
		switch {
		case strings.HasPrefix(l, "EHLO"):
			return "250-test\r\n250 AUTH PLAIN"
		case strings.HasPrefix(l, "AUTH"):
			return `535 bad`
		case strings.HasPrefix(l, "QUIT"):
			return "221 bye"
		}
		return "250 ok"
	})
	auth := smtp.PlainAuth("", "u", "SECRETPW", "127.0.0.1")
	err := sendMailBounded(t.Context(), addr, auth, "a@example.org", []string{"b@example.org"}, []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "535") {
		t.Fatalf("err = %v, want the 535 reply", err)
	}
	if strings.Contains(err.Error(), "closed network connection") || strings.Contains(err.Error(), "SECRETPW") {
		t.Errorf("err %q carries close noise or the password", err)
	}
}
