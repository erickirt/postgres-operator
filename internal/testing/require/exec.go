// Copyright 2021 - 2025 Crunchy Data Solutions, Inc.
//
// SPDX-License-Identifier: Apache-2.0

package require

import (
	"os/exec"
	"sync"
	"testing"

	"gotest.tools/v3/assert"
)

// Flake8 returns the path to the "flake8" executable or calls t.Skip.
func Flake8(t testing.TB) string { t.Helper(); return flake8(t) }

var flake8 = executable("flake8", "--version")

// OpenSSL returns the path to the "openssl" executable or calls t.Skip.
func OpenSSL(t testing.TB) string { t.Helper(); return openssl(t) }

var openssl = executable("openssl", "version", "-a")

// ShellCheck returns the path to the "shellcheck" executable or calls t.Skip.
func ShellCheck(t testing.TB) string { t.Helper(); return shellcheck(t) }

var shellcheck = executable("shellcheck", "--version")

// executable builds a function that returns the full path to name.
// The function (1) locates name or calls t.Skip, (2) runs that with args,
// (3) calls t.Log with the output, and (4) calls t.Fatal if it exits non-zero.
func executable(name string, args ...string) func(testing.TB) string {
	var result func(testing.TB) string
	var once sync.Once

	return func(t testing.TB) string {
		t.Helper()
		once.Do(func() {
			path, err := exec.LookPath(name)
			cmd := exec.CommandContext(t.Context(), path, args...) // #nosec G204 -- args from init()

			if err != nil {
				result = func(t testing.TB) string {
					t.Helper()
					t.Skipf("requires %q executable", name)
					return ""
				}
			} else if info, err := cmd.CombinedOutput(); err != nil {
				result = func(t testing.TB) string {
					t.Helper()
					// Let the "assert" package inspect and format the error.
					// Show what was executed and anything it printed as well.
					// This always calls t.Fatal because err is not nil here.
					assert.NilError(t, err, "%q\n%s", cmd.Args, info)
					return ""
				}
			} else {
				result = func(t testing.TB) string {
					t.Helper()
					t.Logf("using %q\n%s", path, info)
					return path
				}
			}
		})
		return result(t)
	}
}
