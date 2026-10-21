// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Command renovatecheck fails when the Go module lists in .github/renovate.json5 drift from go.mod. Renovate types an
// update `build(deps)`, which lists it in the changelog and opens a release, only for a module that ships in the
// binary, so the configuration names two sets by hand, each between `// renovatecheck:begin <name>` and
// `// renovatecheck:end`: not-shipped, the direct requires that `go list -deps ./cmd/muster` does not reach, and
// indirect-shipped, the indirect requires it does reach. The binary is listed for linux and darwin with cgo off, as
// goreleaser builds it. It prints the names missing from or extra in each list and exits with 1.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
)

const configFile = ".github/renovate.json5"

var (
	blockRe  = regexp.MustCompile(`(?s)// renovatecheck:begin (\S+)\n(.*?)// renovatecheck:end`)
	stringRe = regexp.MustCompile(`"([^"]+)"`)
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "renovatecheck:", err)
		os.Exit(1)
	}
}

func run() error {
	content, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}
	lists := map[string][]string{}
	for _, m := range blockRe.FindAllStringSubmatch(string(content), -1) {
		for _, s := range stringRe.FindAllStringSubmatch(m[2], -1) {
			lists[m[1]] = append(lists[m[1]], s[1])
		}
	}
	direct, indirect, err := requires()
	if err != nil {
		return err
	}
	shipped := map[string]bool{}
	for _, goos := range []string{"linux", "darwin"} {
		out, err := goCmd([]string{"GOOS=" + goos, "CGO_ENABLED=0"},
			"list", "-deps", "-f", "{{with .Module}}{{.Path}}{{end}}", "./cmd/muster")
		if err != nil {
			return err
		}
		for _, m := range strings.Fields(out) {
			shipped[m] = true
		}
	}
	want := map[string][]string{
		"not-shipped":      slices.DeleteFunc(direct, func(m string) bool { return shipped[m] }),
		"indirect-shipped": slices.DeleteFunc(indirect, func(m string) bool { return !shipped[m] }),
	}
	failed := false
	for _, name := range []string{"not-shipped", "indirect-shipped"} {
		got, ok := lists[name]
		if !ok {
			fmt.Printf("%s: no block %q\n", configFile, name)
			failed = true
			continue
		}
		for _, m := range want[name] {
			if !slices.Contains(got, m) {
				fmt.Printf("%s: %s lacks %s\n", configFile, name, m)
				failed = true
			}
		}
		for _, m := range got {
			if !slices.Contains(want[name], m) {
				fmt.Printf("%s: %s has %s, which go.mod and the binary do not place there\n", configFile, name, m)
				failed = true
			}
		}
	}
	if failed {
		fmt.Println("Update the lists in " + configFile + " to match go.mod and `go list -deps ./cmd/muster`.")
		os.Exit(1)
	}
	fmt.Printf("%s: the Go module lists match go.mod and the binary\n", configFile)
	return nil
}

// requires returns the direct and the indirect requires of go.mod.
func requires() (direct, indirect []string, err error) {
	out, err := goCmd(nil, "mod", "edit", "-json")
	if err != nil {
		return nil, nil, err
	}
	var mod struct {
		Require []struct {
			Path     string
			Indirect bool
		}
	}
	if err := json.Unmarshal([]byte(out), &mod); err != nil {
		return nil, nil, err
	}
	for _, r := range mod.Require {
		if r.Indirect {
			indirect = append(indirect, r.Path)
		} else {
			direct = append(direct, r.Path)
		}
	}
	return direct, indirect, nil
}

func goCmd(env []string, args ...string) (string, error) {
	cmd := exec.Command("go", args...) //nolint:noctx // one-shot build tool with nothing to cancel; rule 6 keeps context.Background() out of internal/tools
	cmd.Env = append(os.Environ(), env...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}
