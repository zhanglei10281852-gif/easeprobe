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

package report

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/megaease/easeprobe/global"
	"github.com/megaease/easeprobe/probe"
)

func maintenanceResult() *probe.Result {
	global.InitEaseProbe("MaintProbe", "icon")
	r := probe.NewResult()
	r.Name = "maint-svc"
	r.Endpoint = "http://example.com"
	r.Status = probe.StatusUp // service status frozen at the last confirmation
	r.PreStatus = probe.StatusUp
	r.Stat.Total = 10
	r.Stat.Status[probe.StatusUp] = 10
	r.Stat.UpTime = 10 * time.Minute
	r.StartTime = time.Now().UTC()
	r.RoundTripTime = 5 * time.Millisecond
	r.LatestRawStatus = probe.StatusDown // the latest raw probe failed
	r.Message = "Error (http): boom"
	r.Maintenance = &probe.MaintenanceState{
		Active: true,
		Reason: "kernel upgrade",
		Start:  time.Now().UTC().Add(-time.Minute),
		End:    time.Now().UTC().Add(30 * time.Minute),
	}
	return r
}

func TestSLAJSONMaintenance(t *testing.T) {
	r := maintenanceResult()
	s := SLAJSONSection(r)

	var sla SLA
	assert.Nil(t, json.Unmarshal([]byte(s), &sla))
	assert.True(t, sla.Maintenance.Active)
	assert.Equal(t, "kernel upgrade", sla.Maintenance.Reason)
	// service status vs latest raw observation are distinguishable
	assert.Equal(t, probe.StatusUp, sla.LatestProbe.Status)
	assert.Equal(t, probe.StatusDown, sla.LatestProbe.RawStatus)
	assert.Greater(t, sla.LatestProbe.RTT, time.Duration(0))
	assert.Contains(t, s, `"maintenance"`)
	assert.Contains(t, s, `"raw_status":"down"`)
	assert.Contains(t, s, `"status":"up"`)

	// without maintenance the maintenance field is omitted
	r2 := probe.NewResult()
	r2.Name = "normal"
	r2.Status = probe.StatusUp
	s2 := SLAJSONSection(r2)
	assert.NotContains(t, s2, `"maintenance"`)
	assert.Contains(t, s2, `"raw_status":"up"`)
}

func TestSLASectionsMaintenance(t *testing.T) {
	r := maintenanceResult()

	html := SLAHTMLSection(r)
	assert.Contains(t, html, "Maintenance")
	assert.Contains(t, html, "kernel upgrade")
	assert.Contains(t, html, "Latest Raw Probe")
	assert.Contains(t, html, "Service Status")

	text := SLATextSection(r)
	assert.Contains(t, text, "Maintenance: kernel upgrade")
	assert.Contains(t, text, "Latest-Raw-Probe")

	logSection := SLALogSection(r)
	assert.Contains(t, logSection, `maintenance="kernel upgrade"`)
	assert.Contains(t, logSection, `latest_raw_status="down"`)
	assert.Contains(t, logSection, `latest_status="up"`)

	md := SLAMarkdownSection(r, MarkdownSocial)
	assert.Contains(t, md, "Maintenance")
	assert.Contains(t, md, "Latest-Raw-Probe")

	csv := SLACSVSection(r)
	assert.Equal(t, 11, len(csv))
	assert.Contains(t, csv[9], "Maintenance")
	assert.Equal(t, "down", csv[10])

	// note: the slack payload keeps the project's pre-existing tab literals,
	// it is consumed by the slack API rather than a strict JSON parser.
	slack := SLASlackSection(r)
	assert.Contains(t, slack, "Maintenance")
	assert.Contains(t, slack, "kernel upgrade")
	assert.Contains(t, slack, "Latest Raw Probe")
	assert.Contains(t, slack, `Latest Probe (Service Status)`)

	lark := SLALarkSection(r)
	assert.Contains(t, lark, "Maintenance")
	assert.Contains(t, lark, "Latest-Raw-Probe")
}

func TestSLASectionsNoMaintenanceUnchanged(t *testing.T) {
	r := probe.NewResult()
	r.Name = "normal-svc"
	r.Status = probe.StatusUp
	r.LatestRawStatus = probe.StatusUp

	assert.NotContains(t, SLAHTMLSection(r), "Maintenance")
	assert.NotContains(t, SLATextSection(r), "Maintenance")
	assert.NotContains(t, SLALogSection(r), "maintenance=")
	assert.NotContains(t, SLAMarkdownSection(r, Markdown), "Maintenance")
	assert.NotContains(t, SLASlackSection(r), "Maintenance")
	assert.NotContains(t, SLALarkSection(r), "Maintenance")
	csv := SLACSVSection(r)
	assert.Equal(t, 11, len(csv))
	assert.Empty(t, csv[9])
}
