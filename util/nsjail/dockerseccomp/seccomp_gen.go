//go:build ignore

// seccomp_gen translates Docker's default seccomp profile into a Kafel policy for nsjail.
//
// The policy is specialized for a process that runs on x86_64 without any capabilities, which is how nsjail runs the jailed process.
// Concretely, a rule is kept if:
//   - it does not require a capability (includes.caps), since we have none.
//   - it is not excluded for lacking a capability (excludes.caps), since we lack them all.
//   - it applies to amd64 (includes.arches and excludes.arches).
//
// Rules gated by includes.minKernel are kept, assuming the host kernel is at least that version.
// Syscalls are matched by number, so names that differ between the kernel headers and Kafel, such as fstat and newfstat, are handled.
// Syscalls that are not on x86_64, such as fstat64, are dropped.
// Syscalls that x86_64 has but Kafel does not know are written as SYSCALL[nr].
// Profile fields or operators that this program does not understand are errors, so that new Docker features are not silently ignored.
//
// Usage:
//
//	go run seccomp_gen.go -profile docker_seccomp.json -source moby/profiles@<commit> \
//	  -kafel ~/nsjail/kafel/src/syscalls/amd64_syscalls.c -unistd /usr/include/x86_64-linux-gnu/asm/unistd_64.h \
//	  -o seccomp.kafel
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

var (
	profilePath = flag.String("profile", "docker_seccomp.json", "Docker seccomp profile")
	source      = flag.String("source", "", "where the profile came from, recorded in the output")
	kafelPath   = flag.String("kafel", "", "Kafel's amd64_syscalls.c, from the source tree of the nsjail binary in use")
	unistdPath  = flag.String("unistd", "/usr/include/x86_64-linux-gnu/asm/unistd_64.h", "kernel header listing x86_64 syscall numbers")
	outPath     = flag.String("o", "seccomp.kafel", "output Kafel policy")
)

const (
	dockerArch = "amd64"
	policyName = "docker_default"
)

type profile struct {
	DefaultAction   string    `json:"defaultAction"`
	DefaultErrnoRet *int      `json:"defaultErrnoRet"`
	ArchMap         []archMap `json:"archMap"`
	Syscalls        []rule    `json:"syscalls"`
}

type archMap struct {
	Architecture     string   `json:"architecture"`
	SubArchitectures []string `json:"subArchitectures"`
}

type rule struct {
	Names    []string `json:"names"`
	Action   string   `json:"action"`
	ErrnoRet *int     `json:"errnoRet"`
	Args     []arg    `json:"args"`
	Comment  string   `json:"comment"`
	Includes filter   `json:"includes"`
	Excludes filter   `json:"excludes"`
}

type filter struct {
	Caps      []string `json:"caps"`
	Arches    []string `json:"arches"`
	MinKernel string   `json:"minKernel"`
}

type arg struct {
	Index    int    `json:"index"`
	Value    uint64 `json:"value"`
	ValueTwo uint64 `json:"valueTwo"`
	Op       string `json:"op"`
}

func mainWithErr() error {
	if *kafelPath == "" {
		return errors.Errorf("-kafel is required")
	}
	p, err := readProfile(*profilePath)
	if err != nil {
		return errors.Wrap(err, "")
	}
	kernelNr, err := parseUnistd(*unistdPath)
	if err != nil {
		return errors.Wrap(err, "")
	}
	kafelName, err := parseKafel(*kafelPath)
	if err != nil {
		return errors.Wrap(err, "")
	}

	pol, err := translate(p, kernelNr, kafelName)
	if err != nil {
		return errors.Wrap(err, "")
	}
	if err := os.WriteFile(*outPath, pol, 0644); err != nil {
		return errors.Wrap(err, "")
	}
	return nil
}

func main() {
	flag.Parse()
	log.SetFlags(log.Lmicroseconds | log.Llongfile | log.LstdFlags)

	if err := mainWithErr(); err != nil {
		log.Fatalf("%+v", err)
	}
}

func readProfile(path string) (profile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return profile{}, errors.Wrap(err, "")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var p profile
	if err := dec.Decode(&p); err != nil {
		return profile{}, errors.Wrap(err, path)
	}
	return p, nil
}

// parseUnistd returns the x86_64 syscall numbers by name.
func parseUnistd(path string) (map[string]int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.Wrap(err, "")
	}
	nrs := make(map[string]int)
	for _, m := range regexp.MustCompile(`(?m)^#define __NR_(\w+)\s+(\d+)\s*$`).FindAllSubmatch(b, -1) {
		nr, err := strconv.Atoi(string(m[2]))
		if err != nil {
			return nil, errors.Wrap(err, string(m[0]))
		}
		nrs[string(m[1])] = nr
	}
	if len(nrs) == 0 {
		return nil, errors.Errorf("no syscalls found in %s", path)
	}
	return nrs, nil
}

// parseKafel returns the names Kafel uses for x86_64 syscalls by number.
func parseKafel(path string) (map[int]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.Wrap(err, "")
	}
	names := make(map[int]string)
	for _, m := range regexp.MustCompile(`(?m)^\s*\{"(\w+)",\s*(\d+),`).FindAllSubmatch(b, -1) {
		nr, err := strconv.Atoi(string(m[2]))
		if err != nil {
			return nil, errors.Wrap(err, string(m[0]))
		}
		names[nr] = string(m[1])
	}
	if len(names) == 0 {
		return nil, errors.Errorf("no syscalls found in %s", path)
	}
	return names, nil
}

// syscallRules are the conditions under which a syscall gets an action, which are ORed.
// An empty condition means always.
type syscallRules struct {
	action string
	conds  []string
}

func (r syscallRules) always() bool { return slices.Contains(r.conds, "") }

func translate(p profile, kernelNr map[string]int, kafelName map[int]string) ([]byte, error) {
	defaultAction, err := kafelAction(p.DefaultAction, p.DefaultErrnoRet, p.DefaultErrnoRet)
	if err != nil {
		return nil, errors.Wrap(err, "defaultAction")
	}
	if !slices.ContainsFunc(p.ArchMap, func(a archMap) bool { return a.Architecture == "SCMP_ARCH_X86_64" }) {
		return nil, errors.Errorf("profile does not support SCMP_ARCH_X86_64")
	}

	rules := make(map[int]*syscallRules)
	notAmd64 := make(map[string]bool)
	minKernels := make(map[string]bool)
	for i, r := range p.Syscalls {
		ok, err := applies(r)
		if err != nil {
			return nil, errors.Wrapf(err, "rule %d %v", i, r.Names)
		}
		if !ok {
			continue
		}
		if r.Includes.MinKernel != "" {
			minKernels[r.Includes.MinKernel] = true
		}
		action, err := kafelAction(r.Action, r.ErrnoRet, p.DefaultErrnoRet)
		if err != nil {
			return nil, errors.Wrapf(err, "rule %d %v", i, r.Names)
		}
		cond, err := kafelCond(r.Args)
		if err != nil {
			return nil, errors.Wrapf(err, "rule %d %v", i, r.Names)
		}

		for _, name := range r.Names {
			nr, ok := kernelNr[name]
			if !ok {
				notAmd64[name] = true
				continue
			}
			sr, ok := rules[nr]
			if !ok {
				sr = &syscallRules{action: action}
				rules[nr] = sr
			}
			if sr.action != action {
				return nil, errors.Errorf("rule %d: %s has conflicting actions %s and %s", i, name, sr.action, action)
			}
			sr.conds = append(sr.conds, cond)
		}
	}

	// Group syscalls by action, and sort them for a stable output.
	type entry struct {
		name  string
		nr    int
		rules *syscallRules
	}
	byAction := make(map[string][]entry)
	for nr, sr := range rules {
		name, ok := kafelName[nr]
		if !ok {
			name = fmt.Sprintf("SYSCALL[%d]", nr)
		}
		byAction[sr.action] = append(byAction[sr.action], entry{name: name, nr: nr, rules: sr})
	}
	actions := make([]string, 0, len(byAction))
	for a, es := range byAction {
		actions = append(actions, a)
		slices.SortFunc(es, func(x, y entry) int { return strings.Compare(x.name, y.name) })
	}
	// ALLOW first, then the rest alphabetically.
	slices.SortFunc(actions, func(x, y string) int {
		if (x == "ALLOW") != (y == "ALLOW") {
			if x == "ALLOW" {
				return -1
			}
			return 1
		}
		return strings.Compare(x, y)
	})

	var b strings.Builder
	fmt.Fprintf(&b, "// Code generated by seccomp_gen.go from %s; DO NOT EDIT.\n", *profilePath)
	if *source != "" {
		fmt.Fprintf(&b, "// Source: %s\n", *source)
	}
	b.WriteString("//\n")
	b.WriteString("// Docker's default seccomp profile, for an x86_64 process without capabilities.\n")
	for _, k := range slices.Sorted(maps.Keys(minKernels)) {
		fmt.Fprintf(&b, "// Assumes Linux >= %s.\n", k)
	}
	b.WriteString("// Syscalls in the profile that do not exist on x86_64, and are therefore omitted:\n")
	for _, line := range wrap(slices.Sorted(maps.Keys(notAmd64)), 100) {
		fmt.Fprintf(&b, "//   %s\n", line)
	}
	b.WriteString("\n")

	fmt.Fprintf(&b, "POLICY %s {\n", policyName)
	for i, a := range actions {
		fmt.Fprintf(&b, "  %s {\n", a)
		es := byAction[a]
		for j, e := range es {
			b.WriteString("    ")
			b.WriteString(e.name)
			if !e.rules.always() {
				maxIdx := 0
				for _, c := range e.rules.conds {
					for _, m := range regexp.MustCompile(`\ba(\d)\b`).FindAllStringSubmatch(c, -1) {
						maxIdx = max(maxIdx, int(m[1][0]-'0'))
					}
				}
				params := make([]string, 0, maxIdx+1)
				for k := 0; k <= maxIdx; k++ {
					params = append(params, fmt.Sprintf("a%d", k))
				}
				fmt.Fprintf(&b, "(%s) { %s }", strings.Join(params, ", "), strings.Join(e.rules.conds, ", "))
			}
			if j < len(es)-1 {
				b.WriteString(",")
			}
			if strings.HasPrefix(e.name, "SYSCALL[") {
				fmt.Fprintf(&b, " // %s", kernelName(kernelNr, e.nr))
			}
			b.WriteString("\n")
		}
		b.WriteString("  }")
		if i < len(actions)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}\n\n")
	fmt.Fprintf(&b, "USE %s DEFAULT %s\n", policyName, defaultAction)
	return []byte(b.String()), nil
}

// applies reports whether the rule applies to an x86_64 process without capabilities.
func applies(r rule) (bool, error) {
	if r.Excludes.MinKernel != "" {
		return false, errors.Errorf("excludes.minKernel is not supported")
	}
	if len(r.Includes.Caps) > 0 {
		return false, nil
	}
	if len(r.Includes.Arches) > 0 && !slices.Contains(r.Includes.Arches, dockerArch) {
		return false, nil
	}
	if slices.Contains(r.Excludes.Arches, dockerArch) {
		return false, nil
	}
	// Excludes.Caps means the rule applies when we lack those capabilities, which we always do.
	return true, nil
}

func kafelAction(action string, errnoRet, defaultErrnoRet *int) (string, error) {
	switch action {
	case "SCMP_ACT_ALLOW":
		return "ALLOW", nil
	case "SCMP_ACT_ERRNO":
		errno := 1 // EPERM, which is libseccomp's default.
		if defaultErrnoRet != nil {
			errno = *defaultErrnoRet
		}
		if errnoRet != nil {
			errno = *errnoRet
		}
		return fmt.Sprintf("ERRNO(%d)", errno), nil
	case "SCMP_ACT_KILL", "SCMP_ACT_KILL_THREAD":
		return "KILL", nil
	case "SCMP_ACT_KILL_PROCESS":
		return "KILL_PROCESS", nil
	case "SCMP_ACT_LOG":
		return "LOG", nil
	default:
		return "", errors.Errorf("unsupported action %q", action)
	}
}

// kafelCond returns the Kafel expression of args, which libseccomp ANDs.
// Arguments are referred to as a0 to a5, and are declared as 64-bit, matching libseccomp's comparisons.
func kafelCond(args []arg) (string, error) {
	conds := make([]string, 0, len(args))
	for _, a := range args {
		if a.Index < 0 || a.Index > 5 {
			return "", errors.Errorf("invalid arg index %d", a.Index)
		}
		v := fmt.Sprintf("a%d", a.Index)
		var c string
		switch a.Op {
		case "SCMP_CMP_EQ":
			c = fmt.Sprintf("%s == %#x", v, a.Value)
		case "SCMP_CMP_NE":
			c = fmt.Sprintf("%s != %#x", v, a.Value)
		case "SCMP_CMP_LT":
			c = fmt.Sprintf("%s < %#x", v, a.Value)
		case "SCMP_CMP_LE":
			c = fmt.Sprintf("%s <= %#x", v, a.Value)
		case "SCMP_CMP_GT":
			c = fmt.Sprintf("%s > %#x", v, a.Value)
		case "SCMP_CMP_GE":
			c = fmt.Sprintf("%s >= %#x", v, a.Value)
		case "SCMP_CMP_MASKED_EQ":
			// In Docker's profile, value is the mask and valueTwo is the expected result.
			c = fmt.Sprintf("(%s & %#x) == %#x", v, a.Value, a.ValueTwo)
		default:
			return "", errors.Errorf("unsupported op %q", a.Op)
		}
		conds = append(conds, c)
	}
	if len(conds) > 1 {
		return "(" + strings.Join(conds, " && ") + ")", nil
	}
	return strings.Join(conds, ""), nil
}

func kernelName(kernelNr map[string]int, nr int) string {
	for name, n := range kernelNr {
		if n == nr {
			return name
		}
	}
	return "?"
}

func wrap(words []string, width int) []string {
	lines := make([]string, 0)
	var cur strings.Builder
	for _, w := range words {
		if cur.Len() > 0 && cur.Len()+2+len(w) > width {
			lines = append(lines, cur.String())
			cur.Reset()
		}
		if cur.Len() > 0 {
			cur.WriteString(", ")
		}
		cur.WriteString(w)
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}
