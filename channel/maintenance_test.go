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

package channel

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/megaease/easeprobe/global"
	baseNotify "github.com/megaease/easeprobe/notify/base"
	"github.com/megaease/easeprobe/probe"
	"github.com/megaease/easeprobe/report"
	"github.com/stretchr/testify/assert"
)

type countingNotify struct {
	baseNotify.DefaultNotify
	sent int32
}

func (n *countingNotify) send() {
	atomic.AddInt32(&n.sent, 1)
}

// a queued failure notification must be voided once the probe enters a
// maintenance window, and notifications after the window still go out.
func TestMaintenanceGateInChannel(t *testing.T) {
	// other tests in this package may leave the global dry run flag enabled
	SetDryNotify(false)

	probeName := "maint-channel-probe"
	channelName := "maint-channel"

	now := time.Now().UTC()
	merged, err := probe.MaintenanceSchedule{
		{
			Start: now.Add(-time.Minute).UTC().Format(time.RFC3339),
			End:   now.Add(10 * time.Minute).UTC().Format(time.RFC3339),
		},
	}.Normalize(time.UTC)
	assert.Nil(t, err)
	probe.RegisterMaintenance(probeName, merged)

	n := &countingNotify{}
	n.NotifyKind = "email"
	n.NotifyName = "maint-counter"
	n.NotifyFormat = report.Text
	n.NotifyChannels = []string{channelName}
	n.Retry = global.Retry{Times: 1, Interval: time.Millisecond}
	n.NotifySendFunc = func(string, string) error {
		n.send()
		return nil
	}

	ch := NewEmpty(channelName)
	ch.Config()
	ch.SetNotify(n)

	var wg sync.WaitGroup
	go ch.WatchEvent(&wg)

	downResult := func() probe.Result {
		r := probe.NewResult()
		r.Name = probeName
		r.Endpoint = "endpoint"
		r.Status = probe.StatusDown
		r.PreStatus = probe.StatusUp
		r.Stat.NotificationStrategyData.IsSent = true
		return *r
	}

	// the old failure notification is still in the channel queue when the
	// maintenance starts, it must be voided and never sent
	ch.Send(downResult())
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int32(0), atomic.LoadInt32(&n.sent))

	// other probes are not affected
	other := downResult()
	other.Name = "maint-channel-other"
	ch.Send(other)
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int32(1), atomic.LoadInt32(&n.sent))

	// after the window, new notifications of this probe go out normally
	probe.RegisterMaintenance(probeName, nil)
	ch.Send(downResult())
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int32(2), atomic.LoadInt32(&n.sent))

	ch.Done() <- true
	wg.Wait()
}
