/*
 * Copyright (c) 2022, MegaEase
 * All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package probe

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v3"
)

func mustWindow(t *testing.T, s MaintenanceSchedule) []MergedWindow {
	merged, err := s.Normalize(time.UTC)
	assert.Nil(t, err)
	return merged
}

func TestMaintenanceParseAndValidate(t *testing.T) {
	// RFC3339 and offset forms - three disjoint windows
	s := MaintenanceSchedule{
		{Start: "2026-11-02T01:00:00+08:00", End: "2026-11-02T03:00:00+08:00", Reason: "rfc3339"},
		{Start: "2026-11-03 01:00:00 +0800", End: "2026-11-03 03:00:00 +0800", Reason: "offset"},
		{Start: "2026-11-04 01:00:00 +08:00", End: "2026-11-04 03:00:00 +08:00", Reason: "offset-colon"},
	}
	merged := mustWindow(t, s)
	assert.Equal(t, time.Date(2026, 11, 1, 17, 0, 0, 0, time.UTC), merged[0].Start)
	assert.Equal(t, time.Date(2026, 11, 1, 19, 0, 0, 0, time.UTC), merged[0].End)
	assert.Equal(t, 3, len(merged))

	// naive local time with an explicit IANA timezone
	s = MaintenanceSchedule{
		{Timezone: "Asia/Shanghai", Start: "2026-11-02 09:00:00", End: "2026-11-02 10:30:00"},
	}
	merged = mustWindow(t, s)
	assert.Equal(t, time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC), merged[0].Start)
	assert.Equal(t, time.Duration(90)*time.Minute, merged[0].End.Sub(merged[0].Start))

	// naive time falls back to the default location (Berlin is UTC+1 in winter)
	s = MaintenanceSchedule{
		{Start: "2026-11-02 09:00:00", End: "2026-11-02 10:00:00"},
	}
	loc, _ := time.LoadLocation("Europe/Berlin")
	merged, err := s.Normalize(loc)
	assert.Nil(t, err)
	assert.Equal(t, time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC), merged[0].Start)

	// invalid time
	s = MaintenanceSchedule{{Start: "not-a-time", End: "2026-11-02T03:00:00Z"}}
	_, err = s.Normalize(time.UTC)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "#1")

	// invalid timezone
	s = MaintenanceSchedule{{Timezone: "Mars/Olympus", Start: "2026-11-02 01:00:00", End: "2026-11-02 03:00:00"}}
	_, err = s.Normalize(time.UTC)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid timezone")

	// end equals start
	s = MaintenanceSchedule{{Start: "2026-11-02T01:00:00Z", End: "2026-11-02T01:00:00Z"}}
	_, err = s.Normalize(time.UTC)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "must be later than start")

	// end before start
	s = MaintenanceSchedule{{Start: "2026-11-02T03:00:00Z", End: "2026-11-02T01:00:00Z"}}
	_, err = s.Normalize(time.UTC)
	assert.Error(t, err)

	// empty schedule is valid and stays disabled
	merged, err = MaintenanceSchedule{}.Normalize(time.UTC)
	assert.Nil(t, err)
	assert.Nil(t, merged)
}

func TestMaintenanceYAML(t *testing.T) {
	var cfg struct {
		Maintenance MaintenanceSchedule `yaml:"maintenance"`
	}
	yamlCfg := `
maintenance:
  - timezone: Asia/Shanghai
    start: "2026-11-02 09:00:00"
    end: "2026-11-02 10:00:00"
    reason: upgrade
  - start: "2026-11-03T00:00:00Z"
    end: "2026-11-03T02:00:00Z"
`
	err := yaml.Unmarshal([]byte(yamlCfg), &cfg)
	assert.Nil(t, err)
	assert.Equal(t, 2, len(cfg.Maintenance))
	merged := mustWindow(t, cfg.Maintenance)
	assert.Equal(t, "upgrade", merged[0].Reason)
	assert.Equal(t, time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC), merged[0].Start)
}

func TestMaintenanceMerge(t *testing.T) {
	// out of order on purpose: sorted and the overlapping/touching
	// windows are merged into one continuous window.
	s := MaintenanceSchedule{
		{Start: "2026-11-02T03:00:00Z", End: "2026-11-02T04:00:00Z", Reason: "b"}, // contained in a
		{Start: "2026-11-02T04:00:00Z", End: "2026-11-02T06:00:00Z", Reason: "c"}, // touches the merged end
		{Start: "2026-11-02T01:00:00Z", End: "2026-11-02T03:30:00Z", Reason: "a"}, // overlaps b
		{Start: "2026-11-02T08:00:00Z", End: "2026-11-02T09:00:00Z", Reason: "d"}, // separated
	}
	merged := mustWindow(t, s)

	assert.Equal(t, 2, len(merged))
	assert.Equal(t, time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC), merged[0].Start)
	assert.Equal(t, time.Date(2026, 11, 2, 6, 0, 0, 0, time.UTC), merged[0].End)
	assert.Equal(t, "a; b; c", merged[0].Reason)
	assert.Equal(t, time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC), merged[1].Start)

	// duplicated reasons are merged only once
	s = MaintenanceSchedule{
		{Start: "2026-11-02T01:00:00Z", End: "2026-11-02T02:00:00Z", Reason: "same"},
		{Start: "2026-11-02T02:00:00Z", End: "2026-11-02T03:00:00Z", Reason: "same"},
	}
	merged = mustWindow(t, s)
	assert.Equal(t, 1, len(merged))
	assert.Equal(t, "same", merged[0].Reason)
}

func TestWindowAtHalfOpen(t *testing.T) {
	merged := mustWindow(t, MaintenanceSchedule{
		{Start: "2026-11-02T01:00:00Z", End: "2026-11-02T03:00:00Z", Reason: "win"},
	})
	start := time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC)
	end := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)

	// the start instant belongs to the window
	assert.True(t, InMaintenance(merged, start))
	assert.Equal(t, "win", WindowAt(merged, start).Reason)
	// the middle belongs to the window
	assert.True(t, InMaintenance(merged, start.Add(time.Hour)))
	// the end instant is excluded
	assert.False(t, InMaintenance(merged, end))
	assert.Nil(t, WindowAt(merged, end))
	// outside both sides
	assert.False(t, InMaintenance(merged, start.Add(-time.Second)))
	assert.False(t, InMaintenance(merged, end.Add(time.Second)))
	// nil schedule means no maintenance
	assert.False(t, InMaintenance(nil, start))
}

func TestOverlapDuration(t *testing.T) {
	merged := mustWindow(t, MaintenanceSchedule{
		{Start: "2026-11-02T01:00:00Z", End: "2026-11-02T03:00:00Z"},
		{Start: "2026-11-02T04:00:00Z", End: "2026-11-02T05:00:00Z"},
	})
	from := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 2, 6, 0, 0, 0, time.UTC)

	// both windows fully covered: 2h + 1h
	assert.Equal(t, 3*time.Hour, OverlapDuration(merged, from, to))
	// partial overlap on the first window only
	assert.Equal(t, time.Hour, OverlapDuration(merged,
		time.Date(2026, 11, 2, 0, 30, 0, 0, time.UTC),
		time.Date(2026, 11, 2, 2, 0, 0, 0, time.UTC)))
	// range only across the gap between two windows
	assert.Equal(t, time.Duration(0), OverlapDuration(merged,
		time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 2, 4, 0, 0, 0, time.UTC)))
	// empty or reversed range
	assert.Equal(t, time.Duration(0), OverlapDuration(merged, to, from))
	assert.Equal(t, time.Duration(0), OverlapDuration(merged, from, from))

	// configured overlapping windows are merged, overlap never counted twice
	overlapped := mustWindow(t, MaintenanceSchedule{
		{Start: "2026-11-02T01:00:00Z", End: "2026-11-02T03:00:00Z"},
		{Start: "2026-11-02T02:00:00Z", End: "2026-11-02T04:00:00Z"},
	})
	assert.Equal(t, 3*time.Hour, OverlapDuration(overlapped, from, to))
}

func TestMaintenanceRegistry(t *testing.T) {
	name := "registry-probe"
	at := time.Date(2026, 11, 2, 2, 0, 0, 0, time.UTC)

	// unknown probe never reports maintenance
	assert.False(t, IsInMaintenance(name, at))
	assert.Nil(t, CurrentMaintenanceWindow(name, at))

	merged := mustWindow(t, MaintenanceSchedule{
		{Start: "2026-11-02T01:00:00Z", End: "2026-11-02T03:00:00Z", Reason: "r"},
	})
	RegisterMaintenance(name, merged)
	assert.True(t, IsInMaintenance(name, at))
	assert.Equal(t, "r", CurrentMaintenanceWindow(name, at).Reason)
	assert.False(t, IsInMaintenance(name, at.Add(10*time.Hour)))

	// unregister / disable keeps the probe working normally
	RegisterMaintenance(name, nil)
	assert.False(t, IsInMaintenance(name, at))
}

func TestMaintenanceStateAndResult(t *testing.T) {
	r := NewResult()
	assert.Equal(t, StatusInit, r.RawStat())

	r.Status = StatusUp
	r.LatestRawStatus = StatusDown
	mw := MergedWindow{
		Start:  time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC),
		End:    time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC),
		Reason: "freeze",
	}
	r.Maintenance = mw.State()
	assert.True(t, r.Maintenance.Active)
	assert.Equal(t, "freeze", r.Maintenance.Reason)
	assert.Equal(t, StatusDown, r.RawStat())

	// clone keeps the raw observation and the maintenance state
	c := r.Clone()
	assert.Equal(t, StatusDown, c.LatestRawStatus)
	assert.NotNil(t, c.Maintenance)
	assert.True(t, c.Maintenance.Active)

	// the runtime maintenance state is never persisted into the SLA data file,
	// while the confirmed service status and statistics are preserved.
	r.Stat.Total = 7
	r.Stat.Status[StatusUp] = 7
	data, err := yaml.Marshal(r)
	assert.Nil(t, err)
	assert.NotContains(t, string(data), "maintenance")
	assert.NotContains(t, string(data), "raw_status")

	loaded := NewResult()
	err = yaml.Unmarshal(data, loaded)
	assert.Nil(t, err)
	assert.Nil(t, loaded.Maintenance)
	assert.Equal(t, StatusUp, loaded.Status)
	assert.Equal(t, int64(7), loaded.Stat.Total)
	assert.Equal(t, int64(7), loaded.Stat.Status[StatusUp])
}
