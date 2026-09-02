// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bufio"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The usage text is prose, and prose about flags rots silently: -sleep was
// documented as 1.0 long after the default became 0.25, and thirteen flags
// were never mentioned at all. Nothing could notice, because the only copy of
// the truth was inside each cmdX function's FlagSet.
//
// These tests read that truth. Every FlagSet is ExitOnError, so the only way
// to make one print its defaults is to let it: the test binary re-executes
// itself with USAGE_TEST_CMD set, main() dispatches, and flag prints and exits.
//
// What is NOT reached, deliberately:
//   - the prose itself. "safe to interrupt" could be a lie and this passes.
//   - string and bool defaults. Only numbers are compared, because only a
//     number can be checked without restating the help text here.
//   - the other direction — a flag NAMED in usageText that no command
//     registers. Every candidate token in the prose would have to be told
//     apart from ordinary hyphenated words first, and the failure mode that
//     actually happened is the missing one, not the invented one.

const usageTestEnv = "USAGE_TEST_CMD"

func TestMain(m *testing.M) {
	if cmd := os.Getenv(usageTestEnv); cmd != "" {
		os.Args = []string{"youtubehistii", cmd, "-h"}
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// flagSpec is one registered flag as its own FlagSet describes it.
type flagSpec struct {
	kind     string // "int", "float", "string", "" for bool
	defValue string // as PrintDefaults renders it, "" when it printed none
}

var (
	flagLineRe = regexp.MustCompile(`^\s+-([A-Za-z][\w-]*)(?:\s+(\S+))?\s*$`)
	defaultRe  = regexp.MustCompile(`\(default (.*)\)`)
	numberRe   = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)
	cmdLineRe  = regexp.MustCompile(`^  youtubehistii (\w+)`)
)

// registeredFlags re-executes this test binary as `youtubehistii <cmd> -h`
// and parses what the command's own FlagSet printed.
func registeredFlags(t *testing.T, cmd string) map[string]flagSpec {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}
	c := exec.Command(exe)
	c.Env = append(os.Environ(), usageTestEnv+"="+cmd)
	out, _ := c.CombinedOutput() // -h exits 2 by design; the output is the point

	flags := map[string]flagSpec{}
	var last string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := sc.Text()
		if m := flagLineRe.FindStringSubmatch(line); m != nil {
			last = m[1]
			flags[last] = flagSpec{kind: m[2]}
			continue
		}
		if last == "" {
			continue
		}
		if m := defaultRe.FindStringSubmatch(line); m != nil {
			spec := flags[last]
			spec.defValue = strings.Trim(m[1], `"`)
			flags[last] = spec
		}
	}
	if len(flags) == 0 {
		t.Fatalf("%s -h printed no flags at all:\n%s", cmd, out)
	}
	return flags
}

// usageSegments cuts usageText into one block per command. The tail after
// "Global flag" belongs to no command and is returned separately — -data is
// documented there once instead of in all nine blocks.
func usageSegments(t *testing.T) (map[string]string, string) {
	t.Helper()
	body, tail, found := strings.Cut(usageText, "\nGlobal flag")
	if !found {
		t.Fatal(`usageText no longer has a "Global flag" line — the segmenting below assumes it`)
	}
	segs := map[string]string{}
	cur := ""
	for _, line := range strings.Split(body, "\n") {
		if m := cmdLineRe.FindStringSubmatch(line); m != nil {
			cur = m[1]
		}
		if cur != "" {
			segs[cur] += line + "\n"
		}
	}
	return segs, tail
}

// mentions reports whether seg names exactly this flag. A plain word boundary
// is not enough: it would find -batch inside -llm-batch and -retry inside
// -retry-gone, and then a missing flag would read as documented.
func mentions(seg, name string) bool {
	re := regexp.MustCompile(`(^|[^\w-])-` + regexp.QuoteMeta(name) + `($|[^\w-])`)
	return re.MatchString(seg)
}

func TestUsageNamesEveryFlagOfEveryCommand(t *testing.T) {
	segs, _ := usageSegments(t)
	for cmd := range commands {
		seg, ok := segs[cmd]
		if !ok {
			t.Errorf("usageText has no block for command %q", cmd)
			continue
		}
		for name := range registeredFlags(t, cmd) {
			if name == "data" {
				continue // documented once, globally
			}
			if !mentions(seg, name) {
				t.Errorf("%s: -%s is registered but usageText never names it", cmd, name)
			}
		}
	}
}

func TestUsageNumbersAreTheRealDefaults(t *testing.T) {
	segs, _ := usageSegments(t)
	for cmd := range commands {
		seg := segs[cmd]
		for name, spec := range registeredFlags(t, cmd) {
			if name == "data" || spec.kind == "string" || spec.kind == "" {
				continue
			}
			// A number written next to the flag is a claim about its default.
			re := regexp.MustCompile(`(^|[^\w-])-` + regexp.QuoteMeta(name) + `[= ]+(-?[0-9][0-9.]*)`)
			m := re.FindStringSubmatch(seg)
			if m == nil {
				continue // named without a number: no claim, nothing to check
			}
			want := spec.defValue
			if want == "" {
				want = "0" // PrintDefaults omits the zero value
			}
			if !numberRe.MatchString(want) {
				continue
			}
			got, err1 := strconv.ParseFloat(m[2], 64)
			exp, err2 := strconv.ParseFloat(want, 64)
			if err1 != nil || err2 != nil {
				continue
			}
			if got != exp {
				t.Errorf("%s: usageText says -%s %s, the flag's default is %s", cmd, name, m[2], want)
			}
		}
	}
}

// run takes both other commands' flags and watchpath's, and the usage block
// says so. If it ever registers less than that union, a documented flag is
// silently ignored — which is exactly what -retry did.
func TestRunTakesTheUnionItAdvertises(t *testing.T) {
	union := map[string]bool{}
	for _, cmd := range []string{"enrich", "classify", "watchpath"} {
		for name := range registeredFlags(t, cmd) {
			union[name] = true
		}
	}
	got := registeredFlags(t, "run")
	for name := range union {
		if _, ok := got[name]; !ok {
			t.Errorf("run does not register -%s, which enrich/classify/watchpath do", name)
		}
	}
	for name := range got {
		if !union[name] {
			t.Errorf("run registers -%s, which no other command has — the union claim is wrong", name)
		}
	}
}

func TestDataIsDocumentedOnceAndGlobally(t *testing.T) {
	segs, tail := usageSegments(t)
	if !mentions(tail, "data") {
		t.Error("the global section does not name -data")
	}
	if n := strings.Count(tail, "-data"); n != 1 {
		t.Errorf("the global section names -data %d times, want 1", n)
	}
	for cmd, seg := range segs {
		if mentions(seg, "data") {
			t.Errorf("%s repeats the global -data flag in its own block", cmd)
		}
	}
}

// A command in the dispatch table with no usage block is a command nobody can
// find. The reverse — a block for a command that no longer exists — is the
// same bug from the other side.
func TestUsageBlocksAndCommandsAgree(t *testing.T) {
	segs, _ := usageSegments(t)
	for cmd := range segs {
		if cmd == "version" {
			continue // handled in main, not in the table
		}
		if _, ok := commands[cmd]; !ok {
			t.Errorf("usageText documents %q, which is not a command", cmd)
		}
	}
	if _, ok := segs["version"]; !ok {
		t.Errorf("usageText no longer documents %q", "version")
	}
}
