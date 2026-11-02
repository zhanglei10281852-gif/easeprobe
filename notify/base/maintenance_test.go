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
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/megaease/easeprobe/global"
	"github.com/megaease/easeprobe/probe"
	"github.com/megaease/easeprobe/report"
	"github.com/stretchr/testify/assert"
)

func newMaintenanceGateNotify() (*DefaultNotify, *int32) {
	var sent int32
	n := &DefaultNotify{
		NotifyKind:   "log",
		NotifyName:   "maint-gate-notify",
		NotifyFormat: report.Text,
		Retry: global.Retry{
			Times:    3,
			Interval: time.Millisecond,
		},
		NotifySendFunc: func(string, string) error {
			atomic.AddInt32(&sent, 1)
			return nil
		},
	}
	return n, &sent
}

func failureResult(name string) probe.Result {
	r := probe.NewResult()
	r.Name = name
	r.Endpoint = "endpoint"
	r.Status = probe.StatusDown
	r.PreStatus = probe.StatusUp
	return *r
}

// status notifications are suppressed while the probe is under maintenance,
// but SLA reports are delivered regardless of maintenance windows.
func TestMaintenanceNotificationGate(t *testing.T) {
	global.InitEaseProbe("MaintProbe", "icon")
	probeName := "maint-gate-probe"

	now := time.Now().UTC()
	merged, err := probe.MaintenanceSchedule{
		{
			Start: now.Add(-time.Minute).UTC().Format(time.RFC3339),
			End:   now.Add(10 * time.Minute).UTC().Format(time.RFC3339),
		},
	}.Normalize(time.UTC)
	assert.Nil(t, err)
	probe.RegisterMaintenance(probeName, merged)

	n, sent := newMaintenanceGateNotify()

	// the failure notification never even starts a send attempt
	n.Notify(failureResult(probeName))
	assert.Equal(t, int32(0), atomic.LoadInt32(sent))

	// the gate returns an ErrNoRetry so a failed send is never retried
	err = n.maintenanceGate(failureResult(probeName))
	assert.Error(t, err)
	var noRetry *global.ErrNoRetry
	assert.True(t, errors.As(err, &noRetry))

	// the periodic SLA report must still work during maintenance
	n.SendWithRetry("Overall SLA Report", "sla body", "SLA")
	assert.Equal(t, int32(1), atomic.LoadInt32(sent))

	// outside the window the status notification is delivered
	probe.RegisterMaintenance(probeName, nil)
	n.Notify(failureResult(probeName))
	assert.Equal(t, int32(2), atomic.LoadInt32(sent))
}

// a failing send that would be retried must stop retrying once the
// maintenance window starts.
func TestMaintenanceStopsRetry(t *testing.T) {
	global.InitEaseProbe("MaintProbe", "icon")
	probeName := "maint-retry-probe"

	var attempts int32
	n := &DefaultNotify{
		NotifyKind:   "log",
		NotifyName:   "maint-retry-notify",
		NotifyFormat: report.Text,
		Retry: global.Retry{
			Times:    5,
			Interval: time.Millisecond,
		},
		NotifySendFunc: func(string, string) error {
			atomic.AddInt32(&attempts, 1)
			// the first attempt runs before the window, it fails;
			// by the retry time the probe is already under maintenance
			now := time.Now().UTC()
			probe.RegisterMaintenance(probeName, []probe.MergedWindow{
				{
					Start: now.Add(-time.Minute),
					End:   now.Add(time.Minute),
				},
			})
			return errors.New("network error")
		},
	}

	n.SendStatusWithRetry(failureResult(probeName), "title", "message")
	// first attempt is allowed to finish, the second attempt is suppressed
	// by maintenance, no further retries happen
	assert.Equal(t, int32(1), atomic.LoadInt32(&attempts))

	// voided notifications are not replayed when the window ends
	probe.RegisterMaintenance(probeName, nil)
	assert.Equal(t, int32(1), atomic.LoadInt32(&attempts))
}
