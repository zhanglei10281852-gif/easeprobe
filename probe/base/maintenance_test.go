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

package base

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/megaease/easeprobe/global"
	"github.com/megaease/easeprobe/probe"
)

func maintSchedule(start, end time.Time, reason string) probe.MaintenanceSchedule {
	return probe.MaintenanceSchedule{
		{
			Start:  start.UTC().Format(time.RFC3339),
			End:    end.UTC().Format(time.RFC3339),
			Reason: reason,
		},
	}
}

func newMaintenanceProbe(t *testing.T, name string, failure, success int,
	schedule probe.MaintenanceSchedule, fn ProbeFuncType) *DefaultProbe {
	p := &DefaultProbe{
		ProbeKind:   "dummy",
		ProbeName:   name,
		Maintenance: schedule,
	}
	err := p.Config(global.ProbeSettings{
		StatusChangeThresholdSettings: global.StatusChangeThresholdSettings{
			Failure: failure,
			Success: success,
		},
		NotificationStrategySettings: global.NotificationStrategySettings{
			Strategy: global.RegularStrategy,
			Factor:   1,
			MaxTimes: 10,
		},
	}, "dummy", "", name, "endpoint", fn)
	assert.Nil(t, err)
	return p
}

// leaveMaintenance emulates the process reaching the first probe after a
// continuous maintenance window: the configured windows are in the past
// (so the schedule is retained for downtime deduction), while the probe
// remembers the previous probe was in maintenance.
func leaveMaintenance(t *testing.T, p *DefaultProbe, now time.Time) {
	merged, err := probe.MaintenanceSchedule{
		{
			Start: now.Add(-2 * time.Hour).UTC().Format(time.RFC3339),
			End:   now.Add(-1 * time.Hour).UTC().Format(time.RFC3339),
		},
	}.Normalize(time.UTC)
	assert.Nil(t, err)
	p.maintenanceWindows = merged
	p.wasInMaintenance = true
}

// during a maintenance window the raw observation is refreshed but the
// confirmed service status and every accounting is frozen.
func TestProbeFrozenDuringMaintenance(t *testing.T) {
	global.InitEaseProbe("MaintProbe", "icon")
	now := time.Now().UTC()

	calls := 0
	p := newMaintenanceProbe(t, "maint-freeze", 2, 2,
		maintSchedule(now.Add(-time.Minute), now.Add(10*time.Minute), "upgrade"),
		func() (bool, string) {
			calls++
			return false, "boom"
		})

	for i := 0; i < 4; i++ {
		r := p.Probe()
		// the real probe still runs at its interval
		assert.Equal(t, i+1, calls)
		// the latest raw observation is a failure ...
		assert.Equal(t, probe.StatusDown, r.LatestRawStatus)
		assert.Contains(t, r.Message, "boom")
		assert.Contains(t, r.Message, "[maintenance: upgrade]")
		// ... but the service status stays unconfirmed and frozen
		assert.Equal(t, probe.StatusInit, r.Status)
		assert.Equal(t, probe.StatusInit, r.PreStatus)
		// no threshold / valid probe / SLA accounting moves
		assert.Equal(t, 0, r.Stat.StatusCounter.StatusCount)
		assert.Equal(t, int64(0), r.Stat.Total)
		assert.Equal(t, int64(0), r.Stat.Status[probe.StatusDown])
		assert.Equal(t, time.Duration(0), r.Stat.DownTime)
		// the maintenance state is exposed on the result
		assert.True(t, r.Maintenance.Active)
		assert.Equal(t, "upgrade", r.Maintenance.Reason)
	}
}

// failures observed only inside a maintenance window must not confirm a
// failure, and after the window the failure threshold starts from scratch.
func TestThresholdReaccumulatedAfterWindow(t *testing.T) {
	global.InitEaseProbe("MaintProbe", "icon")
	now := time.Now().UTC()

	p := newMaintenanceProbe(t, "maint-reaccumulate", 3, 3,
		maintSchedule(now.Add(-time.Minute), now.Add(10*time.Minute), "upgrade"),
		func() (bool, string) { return false, "down" })

	// 5 failing probes inside the window confirm nothing
	for i := 0; i < 5; i++ {
		assert.Equal(t, probe.StatusInit, p.Probe().Status)
	}
	assert.Equal(t, int64(0), p.ProbeResult.Stat.Total)

	// first out-of-window observations re-accumulate the threshold
	leaveMaintenance(t, p, now)
	assert.Equal(t, probe.StatusInit, p.Probe().Status) // fresh failure count 1
	assert.Equal(t, probe.StatusInit, p.Probe().Status) // count 2
	r := p.Probe()                                      // count 3 -> DOWN
	assert.Equal(t, probe.StatusDown, r.Status)
	assert.Equal(t, probe.StatusInit, r.PreStatus)
	// only out-of-window probes are accounted, the confirmation probe is DOWN
	assert.Equal(t, int64(3), r.Stat.Total)
	assert.Equal(t, int64(1), r.Stat.Status[probe.StatusDown])

	// a single success after the maintenance-only failures must not look
	// like a recovery of a failure that was never confirmed: the first UP
	// after an Init->DOWN confirmation is treated as the initial monitoring
	// state ("Monitoring"), never as a "Recovery"
	p.ProbeFunc = func() (bool, string) { return true, "ok" }
	r = p.Probe()
	if r.Status == probe.StatusUp {
		assert.Equal(t, probe.StatusInit, r.PreStatus)
		assert.NotContains(t, r.Title(), "Recovery")
	}
}

// a failure confirmed before maintenance keeps emitting exactly one
// recovery after the success threshold is met outside the window, and the
// alert round progress continues for the not-yet-recovered service.
func TestRecoveryAfterMaintenance(t *testing.T) {
	global.InitEaseProbe("MaintProbe", "icon")
	now := time.Now().UTC()

	p := newMaintenanceProbe(t, "maint-recovery", 2, 2, nil,
		func() (bool, string) { return true, "ok" })

	// confirm UP (2 successes)
	p.Probe()
	p.Probe()
	// confirm DOWN (2 failures)
	p.ProbeFunc = func() (bool, string) { return false, "down" }
	p.Probe()
	r := p.Probe()
	assert.Equal(t, probe.StatusDown, r.Status)
	failedBeforeWindow := r.Stat.NotificationStrategyData.Failed
	assert.Greater(t, failedBeforeWindow, 0)

	// enter the window: successes do not recover or move anything
	merged, err := probe.MaintenanceSchedule{
		{
			Start: now.Add(-time.Minute).UTC().Format(time.RFC3339),
			End:   now.Add(10 * time.Minute).UTC().Format(time.RFC3339),
		},
	}.Normalize(time.UTC)
	assert.Nil(t, err)
	p.maintenanceWindows = merged
	p.ProbeFunc = func() (bool, string) { return true, "ok" }
	for i := 0; i < 3; i++ {
		r = p.Probe()
		assert.Equal(t, probe.StatusDown, r.Status)
		assert.Equal(t, probe.StatusDown, r.PreStatus)
		assert.Equal(t, probe.StatusUp, r.LatestRawStatus)
		assert.Equal(t, failedBeforeWindow, r.Stat.NotificationStrategyData.Failed)
		assert.Equal(t, 2, r.Stat.StatusCounter.StatusCount)
	}
	totalDuringWindow := p.ProbeResult.Stat.Total

	// leave the window: successes re-accumulate, the service is still DOWN
	leaveMaintenance(t, p, now)
	r = p.Probe() // fresh success count 1, still DOWN, alert rounds continue
	assert.Equal(t, probe.StatusDown, r.Status)
	assert.Equal(t, failedBeforeWindow+1, r.Stat.NotificationStrategyData.Failed)
	// the out-of-window probe is accounted again
	assert.Equal(t, totalDuringWindow+1, r.Stat.Total)

	// second fresh success meets the recovery threshold exactly once
	r = p.Probe()
	assert.Equal(t, probe.StatusUp, r.Status)
	assert.Equal(t, probe.StatusDown, r.PreStatus)
	assert.GreaterOrEqual(t, r.RecoveryDuration, time.Duration(0))

	// further successes stay UP, no extra recovery transition
	for i := 0; i < 3; i++ {
		r = p.Probe()
		assert.Equal(t, probe.StatusUp, r.Status)
		assert.Equal(t, probe.StatusUp, r.PreStatus)
	}
}

// maintenance time between the DOWN and UP transitions is deducted from the
// recovery duration, and overlapping windows are never deducted twice.
func TestRecoveryDurationDeduction(t *testing.T) {
	global.InitEaseProbe("MaintProbe", "icon")
	now := time.Now().UTC()

	// two disjoint windows: 1h and 10m inside a 2h outage
	p := newMaintenanceProbe(t, "maint-deduction", 1, 1,
		append(maintSchedule(now.Add(-90*time.Minute), now.Add(-30*time.Minute), "a"),
			probe.MaintenanceWindow{
				Start:  now.Add(-20 * time.Minute).UTC().Format(time.RFC3339),
				End:    now.Add(-10 * time.Minute).UTC().Format(time.RFC3339),
				Reason: "b",
			}),
		func() (bool, string) { return true, "ok" })
	p.ProbeResult.LatestDownTime = now.Add(-2 * time.Hour)
	p.ProbeResult.PreStatus = probe.StatusDown
	p.DownTimeCalculation(probe.StatusUp)
	assert.InDelta(t, (50 * time.Minute).Seconds(),
		p.ProbeResult.RecoveryDuration.Seconds(), 2)

	// overlapping configured windows merge into one continuous 80m window,
	// the deducted time is still computed once
	p2 := newMaintenanceProbe(t, "maint-deduction-overlap", 1, 1,
		append(maintSchedule(now.Add(-90*time.Minute), now.Add(-40*time.Minute), "x"),
			probe.MaintenanceWindow{
				Start:  now.Add(-50 * time.Minute).UTC().Format(time.RFC3339),
				End:    now.Add(-10 * time.Minute).UTC().Format(time.RFC3339),
				Reason: "y",
			}),
		func() (bool, string) { return true, "ok" })
	p2.ProbeResult.LatestDownTime = now.Add(-2 * time.Hour)
	p2.ProbeResult.PreStatus = probe.StatusDown
	p2.DownTimeCalculation(probe.StatusUp)
	assert.InDelta(t, (40 * time.Minute).Seconds(),
		p2.ProbeResult.RecoveryDuration.Seconds(), 2)

	// no maintenance configured keeps the wall clock recovery duration
	p3 := newMaintenanceProbe(t, "maint-deduction-none", 1, 1, nil,
		func() (bool, string) { return true, "ok" })
	p3.ProbeResult.LatestDownTime = now.Add(-30 * time.Minute)
	p3.ProbeResult.PreStatus = probe.StatusDown
	p3.DownTimeCalculation(probe.StatusUp)
	assert.InDelta(t, (30 * time.Minute).Seconds(),
		p3.ProbeResult.RecoveryDuration.Seconds(), 2)
}

// restarting inside a maintenance window keeps the persisted confirmed
// status and accumulated statistics, no catch-up accounting happens.
func TestRestartWithinMaintenanceWindow(t *testing.T) {
	global.InitEaseProbe("MaintProbe", "icon")
	name := "maint-restart"
	now := time.Now().UTC()

	// simulate the data loaded from the SLA data file: service confirmed
	// DOWN before the window with accumulated valid statistics.
	stored := probe.NewResult()
	stored.Name = name
	stored.Endpoint = "endpoint"
	stored.Status = probe.StatusDown
	stored.PreStatus = probe.StatusDown
	stored.LatestDownTime = now.Add(-30 * time.Minute)
	stored.Stat.Total = 42
	stored.Stat.Status[probe.StatusDown] = 42
	stored.Stat.DownTime = 42 * time.Minute
	stored.Stat.NotificationStrategyData.Failed = 3
	stored.Stat.NotificationStrategyData.Notified = 2
	probe.SetResultData(name, stored)

	p := newMaintenanceProbe(t, name, 2, 2,
		maintSchedule(now.Add(-time.Minute), now.Add(10*time.Minute), "upgrade"),
		func() (bool, string) { return true, "ok" })

	// the persisted state is picked up
	assert.Equal(t, probe.StatusDown, p.Result().Status)

	// raw successes during the window never recover or re-account anything
	for i := 0; i < 4; i++ {
		r := p.Probe()
		assert.Equal(t, probe.StatusDown, r.Status)
		assert.Equal(t, probe.StatusUp, r.LatestRawStatus)
		assert.True(t, r.Maintenance.Active)
		assert.Equal(t, int64(42), r.Stat.Total)
		assert.Equal(t, 42*time.Minute, r.Stat.DownTime)
		assert.Equal(t, 3, r.Stat.NotificationStrategyData.Failed)
		assert.Equal(t, 2, r.Stat.NotificationStrategyData.Notified)
	}
}

// invalid maintenance configuration must fail the probe configuration.
func TestMaintenanceConfigError(t *testing.T) {
	global.InitEaseProbe("MaintProbe", "icon")

	p := &DefaultProbe{
		ProbeKind: "dummy",
		ProbeName: "maint-bad-time",
		Maintenance: probe.MaintenanceSchedule{
			{Start: "not-a-time", End: "2026-11-02T03:00:00Z"},
		},
	}
	err := p.Config(global.ProbeSettings{}, "dummy", "", p.ProbeName, "endpoint",
		func() (bool, string) { return true, "ok" })
	assert.Error(t, err)

	p = &DefaultProbe{
		ProbeKind: "dummy",
		ProbeName: "maint-bad-range",
		Maintenance: probe.MaintenanceSchedule{
			{Start: "2026-11-02T03:00:00Z", End: "2026-11-02T01:00:00Z"},
		},
	}
	err = p.Config(global.ProbeSettings{}, "dummy", "", p.ProbeName, "endpoint",
		func() (bool, string) { return true, "ok" })
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "must be later than start")

	// a valid active window is registered for the notification gates
	now := time.Now().UTC()
	p2 := newMaintenanceProbe(t, "maint-registered", 1, 1,
		maintSchedule(now.Add(-time.Minute), now.Add(time.Minute), "r"),
		func() (bool, string) { return true, "ok" })
	assert.True(t, probe.IsInMaintenance(p2.Name(), time.Now().UTC()))
	assert.Equal(t, "r", probe.CurrentMaintenanceWindow(p2.Name(), time.Now().UTC()).Reason)
}
