package main

// Authored for story 046, sub-task 13.3 — R4.3.
//
// 13.3's objective: `overlay validate --help` renders `--json` as the boolean it
// is.
//
// # The trap, stated once
//
// pflag reads the FIRST back-quoted substring of a flag's usage string as that
// flag's VALUE PLACEHOLDER — the word printed after the flag name in `--help`
// to tell the operator what argument to supply. pflag.UnquoteUsage scans for a
// back-quote, takes everything up to the closing one as the placeholder, and
// strips both quotes out of the sentence; only when no back-quote is present
// does it fall back to the flag's type, where "bool" means "no placeholder at
// all". pflag.FlagUsagesWrapped then appends that placeholder to the line:
// `      --name` + " " + placeholder.
//
// Since pflag v1.1.0 FlagUsagesWrapped prints `[=true|false]` instead of the
// placeholder for a flag whose type is exactly "bool", so on a plain Bool the
// rendered trap is closed upstream. The sweep below still matters: a
// "boolfunc" flag keeps the old rendering, and the back-quotes are still
// stripped from the sentence on every flag type.
//
// So a back-quote inside a BOOL flag's usage makes `--help` advertise a value
// the flag does not take. Nothing in the source says so — the registration is
// an ordinary Bool call with an ordinary sentence — and the damage appears only
// in rendered output, which is why this file is a standing guard rather than a
// one-line fix. The fix without the guard is invisible the day it is undone.
//
// # RED ON ARRIVAL
//
// cmd/bentoo/overlay_validate.go:115 registers --json as a Bool whose usage
// ends "... and `jq '.kind'` says which command wrote it". Against a HEAD
// binary:
//
//	$ bentoo overlay validate --help
//	      --json jq '.kind'   Write the whole report to stdout as a single JSON document. …
//
// At the story's branch point (c8e347e, same line) the sentence carried no
// back-quote and the flag rendered as a bare `--json`. Story 046 introduced it.
// It is the only back-quoted usage string among the flag registrations in this
// package.
//
// S046-R8.3 asks for recorded evidence that a guard fails when its rule is
// broken. This guard was red before the code it guards was written — the
// strongest form of that evidence, since no mutation had to be staged to
// produce it. TestFlagUsageSweepReportsABoolAndSparesAString keeps the
// capability provable after the fix lands and the two tests above go green.
//
// # Why this is not a source sweep for back-quotes
//
// The obvious cheap guard — grep the package's registrations and ban a
// back-quote in any usage string — would be WRONG, and would be deleted by the
// first person it obstructed. A back-quoted placeholder is pflag's documented
// and intended mechanism: on a flag that genuinely takes a value, writing
//
//	cmd.Flags().String("distdir", "", "Read distfiles from `dir`")
//
// is how you make `--help` say `--distdir dir` instead of `--distdir string`.
// The defect is not the back-quote; it is a placeholder on a flag that accepts
// no value. So the rule below is stated over exactly that: a flag whose
// Value.Type() reports "bool" (or "boolfunc", which pflag treats identically)
// must resolve to an EMPTY placeholder. String, Int and Duration flags may
// carry all the back-quotes they like and this guard stays silent on them —
// which the self-test proves in both directions.
//
// Reading the registered flags rather than the source text also means the guard
// sweeps what the operator actually gets: every flag on every command reachable
// from newRootCmd(), however it was registered, including any added tomorrow in
// a file this test has never heard of.

import (
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// placeholderOffender is one flag that advertises a value it does not take.
type placeholderOffender struct {
	command     string // the command path where the flag was first seen
	flag        string
	placeholder string // what pflag will print after the flag name
	usage       string
}

// boolFlagsAdvertisingAValue is the rule, in one place so the guard and its
// self-test run the identical predicate.
//
// visit is any VisitAll-shaped function; it is called once per flag.
func boolFlagsAdvertisingAValue(command string, visit func(func(*pflag.Flag))) []placeholderOffender {
	var found []placeholderOffender
	visit(func(f *pflag.Flag) {
		switch f.Value.Type() {
		case "bool", "boolfunc":
		default:
			// A flag that takes a value is entitled to name it.
			return
		}
		placeholder, _ := pflag.UnquoteUsage(f)
		if placeholder == "" {
			return
		}
		found = append(found, placeholderOffender{
			command:     command,
			flag:        f.Name,
			placeholder: placeholder,
			usage:       f.Usage,
		})
	})
	return found
}

// TestFlagUsageBoolFlagsAdvertiseNoValuePlaceholder sweeps the whole command
// tree, because R4.3's subject is what the operator reads and every command's
// help is part of that.
//
// Flags are deduplicated by pointer: cobra hands a child the very same *Flag
// objects for its inherited persistent flags, so a single bad root flag would
// otherwise be reported once per command in the tree and bury the one line that
// names it.
func TestFlagUsageBoolFlagsAdvertiseNoValuePlaceholder(t *testing.T) {
	seen := map[*pflag.Flag]bool{}
	var offenders []placeholderOffender

	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		for _, set := range []*pflag.FlagSet{cmd.Flags(), cmd.PersistentFlags()} {
			offenders = append(offenders, boolFlagsAdvertisingAValue(cmd.CommandPath(), func(fn func(*pflag.Flag)) {
				set.VisitAll(func(f *pflag.Flag) {
					if seen[f] {
						return
					}
					seen[f] = true
					fn(f)
				})
			})...)
		}
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(newRootCmd())

	if len(seen) == 0 {
		t.Fatal("the sweep visited no flags at all — newRootCmd() returned a tree with nothing registered, so this guard is reporting the harness and not the flags")
	}

	for _, o := range offenders {
		t.Errorf("`%s --help` renders --%s as if it took a value: it prints `--%s %s`.\n"+
			"    --%s is a BOOL. pflag's UnquoteUsage reads the first back-quoted substring of a\n"+
			"    usage string as the flag's value placeholder (pflag.UnquoteUsage) and prints it after\n"+
			"    the flag name, so the back-quotes below are not quoting — they are telling the\n"+
			"    operator to supply an argument the flag will refuse. They are also stripped out of\n"+
			"    the sentence itself.\n"+
			"    Remedy: remove the back-quotes from the usage string, keeping the sentence.\n"+
			"    usage: %q",
			o.command, o.flag, o.flag, o.placeholder, o.flag, o.usage)
	}
}

// jsonFlagLine matches the --json entry of a rendered flag block: the flag name,
// the `[=true|false]` suffix pflag v1.1.0+ prints after every plain Bool, and
// whatever pflag chose to print after that, up to the end of the line.
var jsonFlagLine = regexp.MustCompile(`(?m)^\s*--json(?:\[=true\|false\])?(.*)$`)

// TestFlagUsageOverlayValidateJSONRendersAsABoolean is the user-visible half:
// it reads the help text an operator would actually see, through the same
// process a real run takes.
//
// The sweep above pins the mechanism; this pins the outcome for the one command
// 13.3 names. Both are kept because they fail for different reasons — the sweep
// would still catch a back-quote on a command whose help nobody reads, and this
// one would still catch a rendering change in a future pflag that made the
// placeholder appear by some other route.
//
// What it asserts: after `--json`, and after the `[=true|false]` suffix when
// pflag prints one, the line must continue with the column gap, not with a
// word. pflag writes " " + placeholder when there is one, and pads with at
// least three spaces before the description when there is not (one space, at
// least one padding space, one more space, in FlagUsagesWrapped). So "two or
// more spaces next" separates the two renderings with room to spare, and does
// not depend on how wide the block happens to be.
func TestFlagUsageOverlayValidateJSONRendersAsABoolean(t *testing.T) {
	stdout, stderr, code := newTestCLI(t).Run("overlay", "validate", "--help")
	if code != 0 {
		t.Fatalf("`overlay validate --help` exited %d (stderr: %q)", code, stderr)
	}

	match := jsonFlagLine.FindStringSubmatch(stdout)
	if match == nil {
		t.Fatalf("`overlay validate --help` prints no --json flag at all — this guard is looking at the wrong output:\n%s", stdout)
	}

	rest := match[1]
	if !strings.HasPrefix(rest, "  ") {
		t.Errorf("`overlay validate --help` renders --json as `--json%s`.\n"+
			"    --json is a boolean and takes no argument, but the help advertises one: the token\n"+
			"    after the flag name is pflag's VALUE PLACEHOLDER, taken from the first back-quoted\n"+
			"    substring of the flag's usage string (pflag.UnquoteUsage, printed by FlagUsagesWrapped).\n"+
			"    An operator reading this line will type a value the flag rejects.\n"+
			"    Remedy: drop the back-quotes from the --json usage string in overlay_validate.go.\n"+
			"    Rendered block:\n%s",
			rest, stdout)
	}
}

// TestFlagUsageSweepReportsABoolAndSparesAString proves the sweep is capable of
// both answers, on fixtures it builds itself.
//
// S046-R8.3: a guard that has only ever passed has proved nothing about what it
// catches. This one arrived red (see the header), and after 13.3's fix lands
// both tests above go green — at which point THIS test is the only remaining
// evidence that the rule still fires. It also pins the other direction, which
// no red-on-arrival can: a legitimate placeholder on a flag that takes a value
// must NOT be reported, or the first person to give --distdir a back-quoted
// placeholder deletes this file instead of the back-quote.
func TestFlagUsageSweepReportsABoolAndSparesAString(t *testing.T) {
	set := pflag.NewFlagSet("probe", pflag.ContinueOnError)
	set.Bool("probe-bool", false, "a sentence that mentions `jq '.kind'` in passing")
	set.String("probe-string", "", "read distfiles from `dir`")
	set.String("probe-plain-string", "", "read distfiles from a directory")
	set.Bool("probe-plain-bool", false, "a sentence with no back-quote at all")

	offenders := boolFlagsAdvertisingAValue("probe", set.VisitAll)

	if len(offenders) != 1 {
		names := make([]string, 0, len(offenders))
		for _, o := range offenders {
			names = append(names, o.flag+"="+o.placeholder)
		}
		t.Fatalf("the sweep reported %d offender(s) %v on a fixture with exactly one: probe-bool.\n"+
			"    Expected: probe-bool reported (a bool advertising `jq '.kind'`), probe-string spared\n"+
			"    (a string flag naming its own placeholder is pflag's intended use).",
			len(offenders), names)
	}
	if offenders[0].flag != "probe-bool" {
		t.Errorf("the sweep reported --%s; the offending fixture is --probe-bool.\n"+
			"    Reporting a flag that takes a value would make this guard forbid pflag's documented\n"+
			"    placeholder syntax, which is legitimate and in use.", offenders[0].flag)
	}
	if offenders[0].placeholder != "jq '.kind'" {
		t.Errorf("the sweep read the placeholder as %q, want %q — it is not extracting what pflag would print",
			offenders[0].placeholder, "jq '.kind'")
	}
}
