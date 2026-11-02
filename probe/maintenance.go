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
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// MaintenanceWindow is one configured maintenance window of a probe.
//
// The window is an absolute, timezone-aware time interval:
//
//	[Start, End) - the start instant belongs to the window, the end does not.
//
// Start/End can be written either with an explicit UTC offset
// (e.g. "2026-11-02T01:00:00+08:00" or "2026-11-02 01:00:00 +0800"),
// or as a naive local time together with the Timezone field
// (IANA zone such as "Asia/Shanghai", the global timezone is used when empty).
type MaintenanceWindow struct {
	// Timezone is the optional IANA timezone of the naive Start/End values.
	Timezone string `yaml:"timezone,omitempty" json:"timezone,omitempty" jsonschema:"title=Time Zone,description=the IANA time zone of the naive start/end time, such as Asia/Shanghai, the global timezone is used when empty,example=Asia/Shanghai"`
	// Start is the absolute start time of the maintenance window (inclusive).
	Start string `yaml:"start" json:"start" jsonschema:"required,title=Start Time,description=the absolute start time of the maintenance window, the start time is included,example=2026-11-02T01:00:00+08:00"`
	// End is the absolute end time of the maintenance window (exclusive), must be later than Start.
	End string `yaml:"end" json:"end" jsonschema:"required,title=End Time,description=the absolute end time of the maintenance window, the end time is excluded and must be later than the start,example=2026-11-02T03:00:00+08:00"`
	// Reason is the human readable reason of the maintenance window.
	Reason string `yaml:"reason,omitempty" json:"reason,omitempty" jsonschema:"title=Reason,description=the reason of the maintenance,example=kernel upgrade"`

	start time.Time `yaml:"-" json:"-"`
	end   time.Time `yaml:"-" json:"-"`
}

// MaintenanceSchedule is the per-probe maintenance configuration.
type MaintenanceSchedule []MaintenanceWindow

// MergedWindow is a validated, sorted and merged maintenance range.
// Overlapping or touching (end == next start) configured windows are
// merged into one continuous MergedWindow.
type MergedWindow struct {
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	Reason string    `json:"reason,omitempty"`
}

// MaintenanceState is the runtime maintenance information of the latest probe,
// it is derived from the configuration and never persisted into the SLA data file.
type MaintenanceState struct {
	// Active reports whether the latest probe started inside a maintenance window.
	Active bool `json:"active" yaml:"-"`
	// Reason is the reason of the current continuous maintenance window.
	Reason string `json:"reason,omitempty" yaml:"-"`
	// Start is the start time of the current continuous maintenance window.
	Start time.Time `json:"start,omitempty" yaml:"-"`
	// End is the end time of the current continuous maintenance window.
	End time.Time `json:"end,omitempty" yaml:"-"`
}

// State converts the merged window into a runtime maintenance state.
func (w *MergedWindow) State() *MaintenanceState {
	if w == nil {
		return &MaintenanceState{Active: false}
	}
	return &MaintenanceState{
		Active: true,
		Reason: w.Reason,
		Start:  w.Start,
		End:    w.End,
	}
}

// the layouts that carry their own UTC offset / zone token.
var maintenanceZonedLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05 Z07:00",
	"2006-01-02 15:04:05 Z0700",
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04:05Z0700",
}

// the layouts without a zone token, interpreted in the given location.
var maintenanceNaiveLayouts = []string{
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
}

func parseMaintenanceTime(value string, loc *time.Location) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("the maintenance time is empty")
	}
	for _, layout := range maintenanceZonedLayouts {
		if t, err := time.Parse(layout, value); err == nil {
			return t, nil
		}
	}
	for _, layout := range maintenanceNaiveLayouts {
		if t, err := time.ParseInLocation(layout, value, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized maintenance time %q, expect an absolute time such as \"2026-11-02T01:00:00+08:00\"", value)
}

// Normalize validates every window and returns the windows sorted by start
// time and merged when they overlap or touch each other.
//
// Invalid times, an invalid timezone or an end not later than the start
// produce an explicit configuration error.
func (s MaintenanceSchedule) Normalize(defaultLoc *time.Location) ([]MergedWindow, error) {
	if len(s) == 0 {
		return nil, nil
	}
	if defaultLoc == nil {
		defaultLoc = time.UTC
	}

	windows := make([]MaintenanceWindow, len(s))
	copy(windows, s)

	for i := range windows {
		w := &windows[i]
		loc := defaultLoc
		if tz := strings.TrimSpace(w.Timezone); tz != "" {
			l, err := time.LoadLocation(tz)
			if err != nil {
				return nil, fmt.Errorf("maintenance window #%d has an invalid timezone %q: %v", i+1, tz, err)
			}
			loc = l
		}

		start, err := parseMaintenanceTime(w.Start, loc)
		if err != nil {
			return nil, fmt.Errorf("maintenance window #%d start is invalid: %v", i+1, err)
		}
		end, err := parseMaintenanceTime(w.End, loc)
		if err != nil {
			return nil, fmt.Errorf("maintenance window #%d end is invalid: %v", i+1, err)
		}
		if !end.After(start) {
			return nil, fmt.Errorf("maintenance window #%d end (%s) must be later than start (%s)",
				i+1, end.UTC().Format(time.RFC3339), start.UTC().Format(time.RFC3339))
		}
		w.start = start.UTC()
		w.end = end.UTC()
	}

	sort.SliceStable(windows, func(i, j int) bool {
		return windows[i].start.Before(windows[j].start)
	})

	// merge overlapping or touching windows into continuous windows.
	merged := make([]MergedWindow, 0, len(windows))
	for _, w := range windows {
		if len(merged) == 0 || w.start.After(merged[len(merged)-1].End) {
			merged = append(merged, MergedWindow{
				Start:  w.start,
				End:    w.end,
				Reason: strings.TrimSpace(w.Reason),
			})
			continue
		}
		cur := &merged[len(merged)-1]
		if w.end.After(cur.End) {
			cur.End = w.end
		}
		cur.Reason = mergeReasons(cur.Reason, w.Reason)
	}
	return merged, nil
}

func mergeReasons(a, b string) string {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if b == "" {
		return a
	}
	if a == "" {
		return b
	}
	for _, r := range strings.Split(a, ";") {
		if strings.TrimSpace(r) == b {
			return a
		}
	}
	return a + "; " + b
}

// WindowAt returns the continuous maintenance window containing the given
// instant, or nil when the instant is outside every window.
// The window is half-open [start, end): an instant equal to the end is outside.
func WindowAt(windows []MergedWindow, t time.Time) *MergedWindow {
	for i := range windows {
		// merged windows are sorted by start and never overlap each other
		if !t.Before(windows[i].Start) && t.Before(windows[i].End) {
			return &windows[i]
		}
	}
	return nil
}

// InMaintenance reports whether the instant belongs to one of the windows.
func InMaintenance(windows []MergedWindow, t time.Time) bool {
	return WindowAt(windows, t) != nil
}

// OverlapDuration returns the total time covered by the windows between from
// and to. The merged windows are disjoint, therefore overlapping parts can
// never be deducted twice.
func OverlapDuration(windows []MergedWindow, from, to time.Time) time.Duration {
	if !to.After(from) {
		return 0
	}
	var total time.Duration
	for _, w := range windows {
		start := from
		if w.Start.After(start) {
			start = w.Start
		}
		end := to
		if w.End.Before(end) {
			end = w.End
		}
		if end.After(start) {
			total += end.Sub(start)
		}
	}
	return total
}

// ----------------------------------------------------------------------------------------
//   The global registry lets the notification pipeline look up the
//   maintenance schedule of a probe by its name without importing the
//   probe/base package.
// ----------------------------------------------------------------------------------------

var (
	maintenanceMu      = &sync.RWMutex{}
	maintenanceWindows = map[string][]MergedWindow{}
)

// RegisterMaintenance registers the merged maintenance windows of a probe.
func RegisterMaintenance(name string, windows []MergedWindow) {
	maintenanceMu.Lock()
	defer maintenanceMu.Unlock()
	maintenanceWindows[name] = windows
}

// CurrentMaintenanceWindow returns the continuous maintenance window of the
// probe active at the given instant, or nil when the probe is not configured
// with a maintenance window or the instant is outside.
func CurrentMaintenanceWindow(name string, at time.Time) *MergedWindow {
	maintenanceMu.RLock()
	defer maintenanceMu.RUnlock()
	return WindowAt(maintenanceWindows[name], at)
}

// IsInMaintenance reports whether the probe is under maintenance at the instant.
func IsInMaintenance(name string, at time.Time) bool {
	return CurrentMaintenanceWindow(name, at) != nil
}
