// Copyright 2021 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build windows
// +build windows

package simple

import (
	"fmt"
	"strings"
	"syscall"
	"time"

	"github.com/golang/glog"
	"golang.org/x/sys/windows"
	"github.com/google/winops/winlog/wevtapi"
	"github.com/google/winops/winlog"
)

// windowsLocaleEn is the locale code for English.
// https://docs.microsoft.com/en-us/previous-versions/windows/embedded/ms912047(v=winembedded.10)?redirectedfrom=MSDN
const windowsLocaleEn = 1033

// WindowsEvent implements Event interface to subscribe events from Windows Event Log.
type WindowsEvent struct {
	config         *winlog.SubscribeConfig
	subscription   windows.Handle
	publisherCache map[string]windows.Handle
}

// NewWindowsEvent creates a WindowsEvent object.
func NewWindowsEvent() Event {
	return &WindowsEvent{}
}

// Subscribe creates the bookmark from XML formatted string and initializes a subscription for
// Windows Event Log.
func (e *WindowsEvent) Subscribe(bookmark string, query map[string]string) error {
	if e.config != nil || e.subscription != 0 {
		return fmt.Errorf("double subscribed Windows Event for: %+v", e)
	}

	var err error
	e.config, err = winlog.DefaultSubscribeConfig()
	if err != nil {
		return fmt.Errorf("winlog.DefaultSubscribeConfig failed: %w", err)
	}
	if bookmark == "" {
		e.config.Bookmark, err = wevtapi.EvtCreateBookmark(nil)
		if err != nil {
			return fmt.Errorf("wevtapi.EvtCreateBookmark failed: %w", err)
		}
	} else {
		e.config.Bookmark, err = wevtapi.EvtCreateBookmark(syscall.StringToUTF16Ptr(bookmark))
		if err != nil {
			glog.Warningf("Create a new bookmark because the existing bookmark might be corrupted: %s", bookmark)
			e.config.Bookmark, err = wevtapi.EvtCreateBookmark(nil)
			if err != nil {
				return fmt.Errorf("wevtapi.EvtCreateBookmark failed: %w", err)
			}
		}
	}

	if len(query) != 0 {
		channels, err := winlog.AvailableChannels()
		if err != nil {
			return fmt.Errorf("finding available channels: %w", err)
		}
		channelSet := make(map[string]bool)
		for _, c := range channels {
			channelSet[strings.ToLower(c)] = true
		}
		// Filter out channels that are not available
		q := make(map[string]string)
		for k, v := range query {
			if channelSet[strings.ToLower(k)] {
				q[k] = v
			} else {
				glog.Warningf("Ignoring non-existent Windows Event Log channel %q", k)
			}
		}
		xmlQuery, err := winlog.BuildStructuredXMLQuery(q)
		if err != nil {
			return fmt.Errorf("Build structured XML query error: %w", err)
		}
		glog.V(1).Infof("Built the structured XML Query: %s", xmlQuery)
		e.config.Query, err = syscall.UTF16PtrFromString(string(xmlQuery))
		if err != nil {
			return fmt.Errorf("syscall.UTF16PtrFromString failed: %w", err)
		}
	}

	e.config.Flags = wevtapi.EvtSubscribeStartAfterBookmark
	e.subscription, err = winlog.Subscribe(e.config)
	e.publisherCache = make(map[string]windows.Handle)
	return err
}

// WaitForSingleObject waits for new events to arrive. Returns true if the event
// was signalled before the timeout expired. Timeout must be between 0 and 2^32us.
func (e *WindowsEvent) WaitForSingleObject(timeout time.Duration) (bool, error) {
	status, err := windows.WaitForSingleObject(e.config.SignalEvent, uint32(timeout/time.Millisecond))
	if err != nil {
		return false, fmt.Errorf("windows.WaitForSingleObject failed: %w", err)
	}
	return status == syscall.WAIT_OBJECT_0, nil
}

// RenderedEvents returns the rendered events as a slice of UTF8 formatted XML strings. `done` will
// be true if no more events.
func (e *WindowsEvent) RenderedEvents(max int) (events []string, done bool, err error) {
	events, err = winlog.GetRenderedEvents(e.config, e.publisherCache, e.subscription, max, windowsLocaleEn)
	// Windows sometimes reports ERROR_INVALID_OPERATION when there is
	// nothing to read. Look, others have encountered the same:
	// https://github.com/elastic/beats/issues/3076#issuecomment-264449775
	if err == windows.ERROR_INVALID_OPERATION || err == windows.ERROR_NO_MORE_ITEMS {
		return nil, true, nil
	} else if err != nil {
		return nil, false, err
	}
	return events, false, nil
}

// Bookmark returns the bookmark in XML format.
func (e *WindowsEvent) Bookmark() (string, error) {
	return winlog.RenderFragment(e.config.Bookmark, wevtapi.EvtRenderBookmark)
}

// ResetEvent resets the event signal after all events are read.
func (e *WindowsEvent) ResetEvent() error {
	return windows.ResetEvent(e.config.SignalEvent)
}

// Close closes the subscription of Windows Event Log and releases the resource.
func (e *WindowsEvent) Close() error {
	if err := winlog.Close(e.subscription); err != nil {
		return fmt.Errorf("closing subscription: %w", err)
	}
	for _, v := range e.publisherCache {
		if err := winlog.Close(v); err != nil {
			return fmt.Errorf("closing publisher metadata: %w", err)
		}
	}
	e.subscription = 0
	e.publisherCache = nil
	e.config = nil
	return nil
}
