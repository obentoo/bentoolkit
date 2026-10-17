package tray

// Executor-authored unit tests for the emerge, units, read-only and SMART
// message builders: each names the fields the requirements list, carries the
// notification key notificationKey gives its condition, and cuts every
// host-supplied field to maxField bytes on a rune boundary.

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestS098Builders_ClipKeepsValidUTF8(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"short", "sshd.service", len("sshd.service")},
		{"exact", strings.Repeat("a", maxField), maxField},
		{"long ASCII", strings.Repeat("a", 300), maxField},
		// 127 ASCII bytes then a 3-byte rune straddling the limit: the rune goes.
		{"rune on the limit", strings.Repeat("a", maxField-1) + "€" + "tail", maxField - 1},
	}
	for _, tc := range cases {
		got := clip(tc.in)
		if len(got) != tc.want || !utf8.ValidString(got) || !strings.HasPrefix(tc.in, got) {
			t.Errorf("%s: clip = %d bytes (valid UTF-8 %v), want a %d-byte prefix", tc.name, len(got), utf8.ValidString(got), tc.want)
		}
	}
}

// A host name that is not UTF-8 (a Latin-1 directory) or carries a control
// character (an unescaped newline in a mount point) must leave clip as valid,
// single-line text, or the bus refuses the whole notification.
func TestS098Builders_ClipSanitisesHostText(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"latin-1 byte", "/mnt/caf\xe9", "/mnt/caf\uFFFD"},
		{"newline and tab", "/mnt/a\nb\tc", "/mnt/a b c"},
		{"C1 control", "Model\u0085X", "Model X"},
		{"only continuation bytes", strings.Repeat("\x80", 200), ""},
	}
	for _, tc := range cases {
		got := clip(tc.in)
		if !utf8.ValidString(got) || strings.ContainsAny(got, "\n\t\r") {
			t.Errorf("%s: clip(%q) = %q: want valid UTF-8 with no line break", tc.name, tc.in, got)
		}
		if tc.want != "" && got != tc.want {
			t.Errorf("%s: clip(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
		if len(got) > maxField {
			t.Errorf("%s: clip(%q) is %d bytes, over %d", tc.name, tc.in, len(got), maxField)
		}
	}
	m, _ := readonlySysMessage("/mnt/caf\xe9", "/dev/sdb1", "ext4", 0, urgencyCritical)
	if !utf8.ValidString(m.Summary) || !utf8.ValidString(m.Body) {
		t.Errorf("read-only message for a Latin-1 mount point = %q / %q, want valid UTF-8", m.Summary, m.Body)
	}
}

func TestS098Builders_Emerge(t *testing.T) {
	m, line := emergeSysMessage("app-misc/jq-1.7.1", 0, "", 0, urgencyNormal)
	if m.Key != notificationKey(condKey{Monitor: monitorEmerge}) || !m.System || m.Summary == "" {
		t.Errorf("emerge message = %+v, want a system message keyed %q with a summary", m, keyEmerge)
	}
	if !strings.Contains(m.Body, "app-misc/jq-1.7.1") || strings.Contains(m.Body, "more") || strings.Contains(m.Body, "build.log") {
		t.Errorf("one failed atom, no log: body %q, want the atom and neither a count nor a log", m.Body)
	}
	if !strings.Contains(line, "app-misc/jq-1.7.1") {
		t.Errorf("emerge tooltip line %q does not name the atom", line)
	}

	log := "/var/tmp/portage/app-misc/aaa-1.0/temp/build.log"
	m, line = emergeSysMessage("app-misc/aaa-1.0", 2, log, 7, urgencyNormal)
	for _, want := range []string{"app-misc/aaa-1.0", "2 more", log} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("three failed atoms with a log: body %q does not state %q", m.Body, want)
		}
	}
	if m.ReplacesID != 7 || !strings.Contains(line, "2 more") {
		t.Errorf("emerge message ReplacesID %d, line %q; want 7 and the count", m.ReplacesID, line)
	}
}

func TestS098Builders_Units(t *testing.T) {
	m, line := unitsSysMessage("user", "pipewire.service", 2, 3, urgencyNormal)
	if m.Key != notificationKey(condKey{Monitor: monitorUnits, Target: "user:pipewire.service"}) || m.ReplacesID != 3 {
		t.Errorf("units message key %q, ReplacesID %d; want %q and 3", m.Key, m.ReplacesID, keyUnits)
	}
	for _, want := range []string{"pipewire.service", "user", "2"} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("units body %q does not state %q", m.Body, want)
		}
	}
	if !strings.Contains(line, "pipewire.service") || !strings.Contains(line, "user") {
		t.Errorf("units tooltip line %q, want the unit and its manager", line)
	}

	m, _ = unitsSysMessage("system", strings.Repeat("x", 300)+".service", 1, 0, urgencyNormal)
	if strings.Contains(m.Body, strings.Repeat("x", maxField+1)) || !strings.Contains(m.Body, strings.Repeat("x", maxField)) {
		t.Errorf("units body carries %d bytes; want the unit name cut to %d", len(m.Body), maxField)
	}
}

func TestS098Builders_Readonly(t *testing.T) {
	m, line := readonlySysMessage("/mnt/data2", "/dev/nvme0n1p3", "btrfs", 0, urgencyCritical)
	if m.Key != notificationKey(condKey{Monitor: monitorReadonly, Target: "/mnt/data2"}) || m.Urgency != urgencyCritical {
		t.Errorf("read-only message key %q, urgency %d; want %q and critical", m.Key, m.Urgency, keyReadonlyPrefix+"/mnt/data2")
	}
	for _, want := range []string{"/mnt/data2", "/dev/nvme0n1p3", "btrfs"} {
		if !strings.Contains(m.Summary+" "+m.Body, want) || !strings.Contains(line, want) {
			t.Errorf("read-only message %q / %q, line %q: %q missing", m.Summary, m.Body, line, want)
		}
	}
}

func TestS098Builders_Smart(t *testing.T) {
	const path = "/org/freedesktop/UDisks2/drives/A"
	m, line := smartSysMessage(path, "ST4000DM004", "ZFN0XYZ", nil, 0, urgencyCritical)
	if m.Key != notificationKey(condKey{Monitor: monitorSmart, Target: path}) {
		t.Errorf("SMART message key %q, want %q", m.Key, keySmartPrefix+path)
	}
	for _, want := range []string{"ST4000DM004", "ZFN0XYZ", "SMART reports failing"} {
		if !strings.Contains(m.Summary+" "+m.Body, want) {
			t.Errorf("SMART message %q / %q does not state %q", m.Summary, m.Body, want)
		}
	}
	if !strings.Contains(line, "ST4000DM004") {
		t.Errorf("SMART tooltip line %q does not name the model", line)
	}

	m, line = smartSysMessage(path, strings.Repeat("M", 300), "SER1", []string{"spare", "temperature"}, 0, urgencyCritical)
	if !strings.Contains(m.Body, "spare, temperature") || strings.Contains(m.Body, "SMART reports failing") {
		t.Errorf("NVMe SMART body %q, want the warning names joined by \", \" instead of the verdict", m.Body)
	}
	if strings.Contains(m.Body+line, strings.Repeat("M", maxField+1)) || !strings.Contains(m.Body, "SER1") {
		t.Errorf("SMART body %q carries more than %d bytes of the model, or lost the serial", m.Body, maxField)
	}
}
