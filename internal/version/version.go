// SPDX-FileCopyrightText: 2026 Christian Semmelmann
// SPDX-License-Identifier: Apache-2.0

// Package version carries the build's version and commit date. The linker
// stamps both; an unstamped build falls back to the module build information.
package version

import (
	"runtime/debug"
	"time"
)

// Version is the release version, or "dev" for an unstamped build. Use Get
// for the resolved value.
var Version = "dev"

// Date is the build's commit date (YYYY-MM-DD), or empty for an unstamped
// build. Use Get for the resolved value.
var Date = ""

// Get returns the version and the commit date (YYYY-MM-DD, possibly empty).
// A build the linker did not stamp, such as one from go install, reports the
// module version and the VCS commit time recorded in its build information.
func Get() (version, date string) {
	bi, _ := debug.ReadBuildInfo()
	return resolve(Version, Date, bi)
}

// resolve applies the build information to an unstamped version and an empty date.
func resolve(version, date string, bi *debug.BuildInfo) (string, string) {
	if version != "dev" || bi == nil {
		return version, date
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		version = v
	}
	if date == "" {
		for _, s := range bi.Settings {
			if s.Key != "vcs.time" {
				continue
			}
			if t, err := time.Parse(time.RFC3339, s.Value); err == nil {
				date = t.UTC().Format(time.DateOnly)
			}
			break
		}
	}
	return version, date
}
