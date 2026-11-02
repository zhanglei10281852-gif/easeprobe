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

// Package base is the base implementation of the notification.
package base

import (
	"time"

	"github.com/megaease/easeprobe/global"
	"github.com/megaease/easeprobe/probe"
	"github.com/megaease/easeprobe/report"
	log "github.com/sirupsen/logrus"
)

// DefaultNotify is the base struct of the Notify
type DefaultNotify struct {
	NotifyKind     string                     `yaml:"-" json:"-"`
	NotifyFormat   report.Format              `yaml:"-" json:"-"`
	NotifySendFunc func(string, string) error `yaml:"-" json:"-"`
	NotifyName     string                     `yaml:"name" json:"name" jsonschema:"required,title=Notification Name,description=The name of the notification"`
	NotifyChannels []string                   `yaml:"channels,omitempty" json:"channels,omitempty" jsonschema:"title=Notification Channels,description=The channels of the notification"`
	Dry            bool                       `yaml:"dry,omitempty" json:"dry,omitempty" jsonschema:"title=Dry Run,description=If true the notification will not send the message"`
	Timeout        time.Duration              `yaml:"timeout,omitempty" json:"timeout,omitempty" jsonschema:"format=duration,title=Timeout,description=The timeout of the notification"`
	Retry          global.Retry               `yaml:"retry,omitempty" json:"retry,omitempty" jsonschema:"title=Retry,description=The retry of the notification"`
}

// Kind returns the kind of the notification
func (c *DefaultNotify) Kind() string {
	return c.NotifyKind
}

// Config is the default configuration for notification
func (c *DefaultNotify) Config(gConf global.NotifySettings) error {
	mode := "Live"
	if c.Dry {
		mode = "Dry"
	}
	log.Infof("Notification [%s] - [%s] is running on %s mode!", c.NotifyKind, c.NotifyName, mode)
	c.Timeout = gConf.NormalizeTimeOut(c.Timeout)
	c.Retry = gConf.NormalizeRetry(c.Retry)

	if len(c.NotifyChannels) == 0 {
		c.NotifyChannels = append(c.NotifyChannels, global.DefaultChannelName)
	}

	log.Infof("Notification [%s] - [%s] is configured!", c.NotifyKind, c.NotifyName)
	return nil
}

// Name returns the name of the notification
func (c *DefaultNotify) Name() string {
	return c.NotifyName
}

// Channels returns the channels of the notification
func (c *DefaultNotify) Channels() []string {
	return c.NotifyChannels
}

// Notify send the result message to the email
func (c *DefaultNotify) Notify(result probe.Result) {
	if c.Dry {
		c.DryNotify(result)
		return
	}
	title := result.Title()
	message := report.FormatFuncs[c.NotifyFormat].ResultFn(result)

	c.SendStatusWithRetry(result, title, message)
}

// maintenanceGate returns an ErrNoRetry while the probe of the result is
// under maintenance. It applies to status (failure/recovery) notifications
// only, the periodic SLA reports are never gated by it.
// ErrNoRetry makes global.DoRetry stop immediately: a notification whose
// send already started may finish, but it will not be retried once the
// maintenance window has started.
func (c *DefaultNotify) maintenanceGate(result probe.Result) error {
	if mw := probe.CurrentMaintenanceWindow(result.Name, time.Now().UTC()); mw != nil {
		log.Infof("[%s / %s / Notification] - %s is under maintenance, the status notification is suppressed (reason: %s)",
			c.NotifyKind, c.NotifyName, result.Name, mw.Reason)
		return &global.ErrNoRetry{Message: "the probe [" + result.Name + "] is under maintenance"}
	}
	return nil
}

// SendStatusWithRetry sends a probe status (failure/recovery) notification
// with retry, the send is suppressed while the probe is under maintenance.
func (c *DefaultNotify) SendStatusWithRetry(result probe.Result, title, message string) {
	fn := func() error {
		if err := c.maintenanceGate(result); err != nil {
			return err
		}
		log.Debugf("[%s / %s / Notification] - %s", c.NotifyKind, c.NotifyName, title)
		if c.NotifySendFunc == nil {
			log.Errorf("[%s / %s / Notification] - %s SendFunc is nil", c.NotifyKind, c.NotifyName, title)
			return &global.ErrNoRetry{Message: "SendFunc is nil"}
		}
		return c.NotifySendFunc(title, message)
	}
	err := global.DoRetry(c.NotifyKind, c.NotifyName, "Notification", c.Retry, fn)
	report.LogSend(c.NotifyKind, c.NotifyName, "Notification", title, err)
}

// NotifyStat send the stat message into the email
func (c *DefaultNotify) NotifyStat(probers []probe.Prober) {
	if c.Dry {
		c.DryNotifyStat(probers)
		return
	}
	title := "Overall SLA Report"
	message := report.FormatFuncs[c.NotifyFormat].StatFn(probers)
	c.SendWithRetry(title, message, "SLA")
}

// SendWithRetry sends the notification with retry if got error
func (c *DefaultNotify) SendWithRetry(title string, message string, tag string) {
	fn := func() error {
		log.Debugf("[%s / %s / %s] - %s", c.NotifyKind, c.NotifyName, tag, title)
		if c.NotifySendFunc == nil {
			log.Errorf("[%s / %s / %s] - %s SendFunc is nil", c.NotifyKind, c.NotifyName, tag, title)
			return &global.ErrNoRetry{Message: "SendFunc is nil"}
		}
		return c.NotifySendFunc(title, message)
	}
	err := global.DoRetry(c.NotifyKind, c.NotifyName, tag, c.Retry, fn)
	report.LogSend(c.NotifyKind, c.NotifyName, tag, title, err)
}

// DryNotify just log the notification message
func (c *DefaultNotify) DryNotify(result probe.Result) {
	// even in dry run mode, status notifications obey the maintenance windows
	if err := c.maintenanceGate(result); err != nil {
		return
	}
	log.Infof("[%s / %s / dry_notify] - %s", c.NotifyKind, c.NotifyName,
		report.FormatFuncs[c.NotifyFormat].ResultFn(result))
}

// DryNotifyStat just log the notification message
func (c *DefaultNotify) DryNotifyStat(probers []probe.Prober) {
	log.Infof("[%s / %s / dry_notify] - %s", c.NotifyKind, c.NotifyName,
		report.FormatFuncs[c.NotifyFormat].StatFn(probers))
}
