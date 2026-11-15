// SPDX-FileCopyrightText: 2026 Christian Semmelmann
// SPDX-License-Identifier: Apache-2.0

package version

import (
	"runtime/debug"
	"testing"
	"time"
)

func TestResolve(t *testing.T) {
	info := func(v string, vcsTime string) *debug.BuildInfo {
		bi := &debug.BuildInfo{Main: debug.Module{Version: v}}
		if vcsTime != "" {
			bi.Settings = []debug.BuildSetting{{Key: "vcs.time", Value: vcsTime}}
		}
		return bi
	}
	for _, tc := range []struct {
		name                 string
		version, date        string
		bi                   *debug.BuildInfo
		wantVersion, wantDay string
	}{
		{"stamped", "1.0.0", "2026-01-02", info("v9.9.9", "2030-01-01T00:00:00Z"), "1.0.0", "2026-01-02"},
		{"module version", "dev", "", info("v1.2.3", "2026-03-04T05:06:07Z"), "v1.2.3", "2026-03-04"},
		{"module version keeps a stamped date", "dev", "2026-01-02", info("v1.2.3", "2026-03-04T05:06:07Z"), "v1.2.3", "2026-01-02"},
		{"devel", "dev", "", info("(devel)", "2026-03-04T05:06:07Z"), "dev", "2026-03-04"},
		{"empty version", "dev", "", info("", ""), "dev", ""},
		{"short vcs time", "dev", "", info("v1.0.0", "2026"), "v1.0.0", ""},
		{"no build info", "dev", "", nil, "dev", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, d := resolve(tc.version, tc.date, tc.bi)
			if v != tc.wantVersion || d != tc.wantDay {
				t.Errorf("resolve = %q, %q; want %q, %q", v, d, tc.wantVersion, tc.wantDay)
			}
		})
	}
}

func TestGetUnstamped(t *testing.T) {
	v, d := Get()
	if v == "" {
		t.Error("Get returned an empty version")
	}
	if _, err := time.Parse(time.DateOnly, d); d != "" && err != nil {
		t.Errorf("Get date = %q, want empty or YYYY-MM-DD", d)
	}
}
