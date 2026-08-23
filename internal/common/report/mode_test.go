package report

import (
	"strings"
	"testing"
)

// TestResolveModePrecedence walks R3.2: flag, then env, then config, then auto.
func TestResolveModePrecedence(t *testing.T) {
	cases := []struct {
		name string
		in   ModeInputs
		want Mode
	}{
		{
			name: "the flag outranks every other source",
			in:   ModeInputs{Flag: "plain", Env: "fullscreen", Config: "inline", Interactive: true},
			want: ModePlain,
		},
		{
			name: "the env var outranks config",
			in:   ModeInputs{Env: "fullscreen", Config: "inline", Interactive: true},
			want: ModeFullscreen,
		},
		{
			name: "config decides when neither flag nor env speaks",
			in:   ModeInputs{Config: "inline", Interactive: true},
			want: ModeInline,
		},
		{
			name: "nothing configured, on a terminal",
			in:   ModeInputs{Interactive: true},
			want: ModeInline,
		},
		{
			name: "nothing configured, off a terminal",
			in:   ModeInputs{Interactive: false},
			want: ModePlain,
		},
		{
			name: "auto stated explicitly is still auto, not an error",
			in:   ModeInputs{Flag: "auto", Interactive: true},
			want: ModeInline,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, warning, err := ResolveMode(tc.in)
			if err != nil {
				t.Fatalf("ResolveMode(%+v) returned an error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("ResolveMode(%+v) = %q, want %q", tc.in, got, tc.want)
			}
			if warning != "" {
				t.Fatalf("ResolveMode(%+v) warned %q, want no warning: nothing was downgraded", tc.in, warning)
			}
		})
	}
}

// TestResolveModeAutoResolvesByInteractivity pins R3.7 at its source: with
// nothing configured, the answer is exactly what tui.Enabled gives today —
// inline on a TTY, plain off one. That is what makes "no change for a user who
// configured nothing" hold by construction rather than by a compatibility
// branch.
func TestResolveModeAutoResolvesByInteractivity(t *testing.T) {
	for _, interactive := range []bool{true, false} {
		want := ModePlain
		if interactive {
			want = ModeInline
		}

		got, _, err := ResolveMode(ModeInputs{Interactive: interactive})
		if err != nil {
			t.Fatalf("ResolveMode(Interactive=%v) returned an error: %v", interactive, err)
		}
		if got != want {
			t.Errorf("ResolveMode(Interactive=%v) = %q, want %q", interactive, got, want)
		}
	}
}

// TestAutoNeverFullscreen pins R3.3. Full screen takes over the terminal, so it
// is opt-in: no combination that leaves the mode unstated may produce it.
func TestAutoNeverFullscreen(t *testing.T) {
	unstated := []string{"", "auto"}

	for _, flag := range unstated {
		for _, env := range unstated {
			for _, config := range unstated {
				for _, interactive := range []bool{true, false} {
					in := ModeInputs{Flag: flag, Env: env, Config: config, Interactive: interactive}

					got, _, err := ResolveMode(in)
					if err != nil {
						t.Fatalf("ResolveMode(%+v) returned an error: %v", in, err)
					}
					if got == ModeFullscreen {
						t.Errorf("ResolveMode(%+v) = %q: fullscreen is opt-in and nothing here opted in", in, got)
					}
				}
			}
		}
	}
}

// TestResolveModeExplicitFullscreenOffATTYDowngradesAndWarns asserts BOTH
// halves of R3.6. A downgrade with no sentence leaves the operator wondering
// why the screen they asked for never appeared.
func TestResolveModeExplicitFullscreenOffATTYDowngradesAndWarns(t *testing.T) {
	in := ModeInputs{Flag: "fullscreen", Interactive: false}

	got, warning, err := ResolveMode(in)
	if err != nil {
		t.Fatalf("ResolveMode(%+v) returned an error: %v — an unsupported terminal is a downgrade, not a failure", in, err)
	}
	if got != ModePlain {
		t.Errorf("ResolveMode(%+v) = %q, want %q", in, got, ModePlain)
	}
	if strings.TrimSpace(warning) == "" {
		t.Error("ResolveMode downgraded fullscreen to plain and said nothing; R3.6 requires the sentence")
	}
}

// TestResolveModeFullscreenOnATTYIsHonoured is the other side of the same
// branch: a downgrade that fires unconditionally would satisfy the test above
// and make --ui=fullscreen dead code.
func TestResolveModeFullscreenOnATTYIsHonoured(t *testing.T) {
	in := ModeInputs{Flag: "fullscreen", Interactive: true}

	got, warning, err := ResolveMode(in)
	if err != nil {
		t.Fatalf("ResolveMode(%+v) returned an error: %v", in, err)
	}
	if got != ModeFullscreen {
		t.Fatalf("ResolveMode(%+v) = %q, want %q", in, got, ModeFullscreen)
	}
	if warning != "" {
		t.Errorf("ResolveMode(%+v) warned %q, want no warning: nothing was downgraded", in, warning)
	}
}

// TestResolveModeNoTUIBeatsConfigAndEnv pins R3.4. --no-tui is an alias for
// --ui=plain applied at the flag layer, not a second mechanism competing with
// --ui.
func TestResolveModeNoTUIBeatsConfigAndEnv(t *testing.T) {
	in := ModeInputs{Env: "fullscreen", Config: "fullscreen", NoTUI: true, Interactive: true}

	got, _, err := ResolveMode(in)
	if err != nil {
		t.Fatalf("ResolveMode(%+v) returned an error: %v", in, err)
	}
	if got != ModePlain {
		t.Fatalf("ResolveMode(%+v) = %q, want %q", in, got, ModePlain)
	}
}

// TestResolveModeUnknownValueErrors pins R3.9. An error that does not name the
// legal set leaves the operator guessing which four words are accepted.
func TestResolveModeUnknownValueErrors(t *testing.T) {
	in := ModeInputs{Flag: "bogus", Interactive: true}

	_, _, err := ResolveMode(in)
	if err == nil {
		t.Fatal("ResolveMode(Flag=\"bogus\") returned no error")
	}

	for _, legal := range []string{"auto", "plain", "inline", "fullscreen"} {
		if !strings.Contains(err.Error(), legal) {
			t.Errorf("the error %q does not name the legal value %q", err.Error(), legal)
		}
	}
}

// TestResolveModeNeverReturnsAuto is the postcondition every caller depends on.
// ModeAuto is a question ("decide for me"); a renderer cannot render a question.
func TestResolveModeNeverReturnsAuto(t *testing.T) {
	values := []string{"", "auto", "plain", "inline", "fullscreen"}

	for _, flag := range values {
		for _, config := range values {
			for _, noTUI := range []bool{true, false} {
				for _, interactive := range []bool{true, false} {
					in := ModeInputs{Flag: flag, Config: config, NoTUI: noTUI, Interactive: interactive}

					got, _, err := ResolveMode(in)
					if err != nil {
						continue
					}
					if got == ModeAuto {
						t.Errorf("ResolveMode(%+v) = %q: auto must be resolved, never returned", in, got)
					}
				}
			}
		}
	}
}
