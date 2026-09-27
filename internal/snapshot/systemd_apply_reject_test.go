package snapshot

import (
	"os"
	"strings"
	"testing"
	"text/template"
)

// TestSystemdApply_WritesNothingOnRenderError pins R7.4 at the Apply boundary.
func TestSystemdApply_WritesNothingOnRenderError(t *testing.T) {
	for name, set := range map[string]func(*systemdScheduler){
		"newline in config path": func(s *systemdScheduler) { s.configPath = "/etc/bentoo/a\nExecStartPre=/bin/evil" },
		"DEL in config path":     func(s *systemdScheduler) { s.configPath = "/etc/bentoo/a\x7f.toml" },
		"newline in exec path":   func(s *systemdScheduler) { s.execPath = "bentoo\nExecStartPre=/bin/evil" },
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			mock := &MockRunner{}
			s := newSystemdScheduler("/etc/bentoo/snapshot.toml", mock)
			s.unitDir = dir
			set(s)
			if err := s.Apply(t.Context(), ScheduleConfig{OnCalendar: "daily"}); err == nil {
				t.Errorf("Apply = nil, want a refusal")
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Errorf("Apply wrote %d file(s) despite the refusal", len(entries))
			}
			if len(mock.Calls) != 0 {
				t.Errorf("Apply ran systemctl %+v despite the refusal", mock.Calls)
			}
		})
	}
}

// TestSystemdApply_WrapsTemplateErrorWithUnitName pins R7.5.
func TestSystemdApply_WrapsTemplateErrorWithUnitName(t *testing.T) {
	broken := template.Must(template.New("broken").Parse(`{{template "missing"}}`))
	for _, tc := range []struct {
		unit string
		tmpl **template.Template
	}{{serviceUnitName, &serviceTemplate}, {timerUnitName, &timerTemplate}} {
		t.Run(tc.unit, func(t *testing.T) {
			orig := *tc.tmpl
			*tc.tmpl = broken
			t.Cleanup(func() { *tc.tmpl = orig })
			mock := &MockRunner{}
			s := newSystemdScheduler("/etc/bentoo/snapshot.toml", mock)
			s.unitDir = t.TempDir()
			err := s.Apply(t.Context(), ScheduleConfig{OnCalendar: "daily"})
			if err == nil || !strings.Contains(err.Error(), tc.unit) {
				t.Errorf("Apply = %v, want the template error wrapped with %s", err, tc.unit)
			}
			if len(mock.Calls) != 0 {
				t.Errorf("Apply ran systemctl %+v after a render failure", mock.Calls)
			}
		})
	}
}
