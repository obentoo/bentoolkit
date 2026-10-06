package snapshot

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Regression guards for story 053: green before the fix, green after it.

// R9.1: btrbk + ssh stays Delegated/OK even when the snapshot is unidentified.
func TestManagerRun_BtrbkSSHDelegatedWhenUnidentified(t *testing.T) {
	dir := t.TempDir()
	orig := StateDir
	StateDir = func() string { return dir }
	t.Cleanup(func() { StateDir = orig })
	mr := &mockRunner{}
	ship := ShipConfig{Name: "offsite", Type: "ssh", Target: "u@h:/b"}
	m, err := NewManager(Config{Engine: EngineConfig{Driver: "btrbk", Subvolumes: []string{"/home"}}, Ship: []ShipConfig{ship}},
		filepath.Join(t.TempDir(), "snapshot.toml"), mr)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	res, err := m.Run(t.Context())
	if err != nil || res.Failed() {
		t.Fatalf("Run = (%+v, %v), want success", res, err)
	}
	sh, err := newShipper(ship, mr, Retention{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := sh.Send(t.Context(), Snapshot{Subvolume: "/home"})
	if err != nil || !rep.Delegated {
		t.Errorf("ssh Send(unidentified) = (%+v, %v), want Delegated and nil", rep, err)
	}
}

// R9.2 through the unchanged Apply signature: ordinary values render the goldens.
func TestSystemdApply_OrdinaryUnitsMatchGoldens(t *testing.T) {
	dir := t.TempDir()
	s := newSystemdScheduler("/etc/bentoo/snapshot.toml", &mockRunner{})
	s.unitDir = dir
	if err := s.Apply(t.Context(), ScheduleConfig{OnCalendar: "daily", Persistent: boolPtr(true), RandomizedDelay: "5m"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for unit, golden := range map[string]string{serviceUnitName: "service.golden", timerUnitName: "timer_persistent.golden"} {
		got, err := os.ReadFile(filepath.Join(dir, unit))
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join("testdata", golden))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from %s:\n%s", unit, golden, got)
		}
	}
}

// R5.3: the pipe and the sequential Run chain produce byte-identical output.
func TestExecRunner_PipeByteIdenticalToSequential(t *testing.T) {
	for _, b := range []string{"sh", "head", "gzip"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("%s not on PATH", b)
		}
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "in")
	if err := exec.Command("sh", "-c", `head -c 4194304 /dev/urandom > "$1"`, "sh", in).Run(); err != nil {
		t.Fatal(err)
	}
	stages := func(out string) []PipeStage {
		return []PipeStage{
			{Name: "sh", Args: []string{"-c", `cat "$1"`, "sh", in}},
			{Name: "gzip", Args: []string{"-n", "-c"}},
			{Name: "sh", Args: []string{"-c", `cat > "$1"`, "sh", out}},
		}
	}
	pipeOut, seqOut := filepath.Join(dir, "pipe.gz"), filepath.Join(dir, "seq.gz")
	if _, err := runPipe(t.Context(), execRunner{}, stages(pipeOut)); err != nil {
		t.Fatalf("runPipe: %v", err)
	}
	var prev []byte
	for _, st := range stages(seqOut) {
		out, err := execRunner{}.Run(t.Context(), st.Name, st.Args, prev)
		if err != nil {
			t.Fatalf("sequential %s: %v", st.Name, err)
		}
		prev = out
	}
	a, errA := os.ReadFile(pipeOut)
	b, errB := os.ReadFile(seqOut)
	if errA != nil || errB != nil || len(a) == 0 || !bytes.Equal(a, b) {
		t.Errorf("pipe output (%d bytes, %v) != sequential output (%d bytes, %v)", len(a), errA, len(b), errB)
	}
}

// s053ScriptedSMTP serves one SMTP session and reports every client line.
func s053ScriptedSMTP(t *testing.T, exts ...string) (addr string, lines <-chan []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	got := make(chan []string, 1)
	go func() {
		var seen []string
		defer func() { got <- seen }()
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		w := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
		w("220 localhost ESMTP")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			seen = append(seen, line)
			switch cmd := strings.ToUpper(strings.SplitN(line, " ", 2)[0]); cmd {
			case "EHLO", "HELO":
				all := append([]string{"localhost"}, exts...)
				for i, e := range all {
					if i == len(all)-1 {
						w("250 " + e)
					} else {
						w("250-" + e)
					}
				}
			case "STARTTLS":
				w("454 4.7.0 TLS not available")
			case "AUTH":
				w("235 2.7.0 Authentication successful")
			case "DATA":
				w("354 go ahead")
				for {
					d, err := r.ReadString('\n')
					if err != nil {
						return
					}
					d = strings.TrimRight(d, "\r\n")
					if d == "." {
						break
					}
					seen = append(seen, "DATA:"+d)
				}
				w("250 OK queued")
			case "QUIT":
				w("221 bye")
				return
			default:
				w("250 OK")
			}
		}
	}()
	return ln.Addr().String(), got
}

func s053HasPrefix(lines []string, p string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	return false
}

func s053Email(t *testing.T, addr string) emailNotifier {
	t.Helper()
	host, port, _ := net.SplitHostPort(addr)
	cfg := EmailConfig{To: []string{"ops@example.org"}, From: "bentoo@example.org"}
	cfg.SMTP.Host = host
	for _, ch := range port {
		cfg.SMTP.Port = cfg.SMTP.Port*10 + int(ch-'0')
	}
	return emailNotifier{cfg: cfg, runner: &mockRunner{}}
}

// R8.4: plain delivery to a local server behaves as smtp.SendMail does.
func TestEmailNotifier_SMTPDeliversToLocalServer(t *testing.T) {
	addr, lines := s053ScriptedSMTP(t, "8BITMIME")
	if err := s053Email(t, addr).Notify(t.Context(), failRun()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	seen := <-lines
	for _, p := range []string{"EHLO", "MAIL FROM:<bentoo@example.org>", "RCPT TO:<ops@example.org>", "DATA:Subject: Snapshot run FAILED", "QUIT"} {
		if !s053HasPrefix(seen, p) {
			t.Errorf("server never saw %q; session %q", p, seen)
		}
	}
	if s053HasPrefix(seen, "STARTTLS") || s053HasPrefix(seen, "AUTH") {
		t.Errorf("unexpected STARTTLS/AUTH without advertisement/credentials: %q", seen)
	}
}

// R8.4: STARTTLS is attempted when advertised, before any envelope command.
func TestEmailNotifier_SMTPStartTLSAttempted(t *testing.T) {
	addr, lines := s053ScriptedSMTP(t, "STARTTLS")
	if err := s053Email(t, addr).Notify(t.Context(), failRun()); err == nil {
		t.Errorf("Notify = nil although STARTTLS was refused")
	}
	seen := <-lines
	if !s053HasPrefix(seen, "STARTTLS") || s053HasPrefix(seen, "MAIL") {
		t.Errorf("session %q: want STARTTLS attempted and no MAIL after its refusal", seen)
	}
}

// R8.4: AUTH PLAIN is sent when credentials are set.
func TestEmailNotifier_SMTPAuthPlainSent(t *testing.T) {
	addr, lines := s053ScriptedSMTP(t, "AUTH PLAIN")
	n := s053Email(t, addr)
	n.cfg.SMTP.User, n.smtpPassword = "bentoo", "s3cret"
	if err := n.Notify(t.Context(), failRun()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	var cred string
	for _, l := range <-lines {
		if strings.HasPrefix(l, "AUTH PLAIN ") {
			b, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(l, "AUTH PLAIN "))
			cred = string(b)
		}
	}
	if cred != "\x00bentoo\x00s3cret" {
		t.Errorf("AUTH PLAIN credential = %q, want \\x00bentoo\\x00s3cret", cred)
	}
}
