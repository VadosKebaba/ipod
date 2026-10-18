package main

import (
	"encoding/binary"
	"sync"
	"time"

	extremote "github.com/oandrew/ipod/lingo-extremote"
)

const (
	extRemoteTrackLength = 300 * 1000 // ms, what the placeholder track info has always said
	extRemoteNumTracks   = 1
)

// DevExtRemote is the player the Extended Remote lingo reports. Nothing plays behind it (the
// audio is whatever the Pi sends), so it follows what the head unit asks for: PlayControl
// changes the state, the position runs with the clock while playing, and the changes go out as
// PlayStatusChangeNotifications if the head unit enabled them.
type DevExtRemote struct {
	mu  sync.Mutex
	now func() time.Time

	state     extremote.PlayerState
	track     int32
	numTracks uint32
	length    uint32    // ms
	pos       uint32    // ms, at since
	since     time.Time // pos is up to date at this time

	enabled extremote.PlayStatusChanges // what the head unit asked for
	changed extremote.PlayStatusChanges // to report at the next Notifications
	sentSec uint32                      // last PlayStatusChangeTrackTimeSec value sent
}

var _ Player = &DevExtRemote{}

func NewDevExtRemote(now func() time.Time) *DevExtRemote {
	d := &DevExtRemote{now: now}
	d.Reset()
	return d
}

// Reset puts the player in its on-connect state: track 0 playing from the start, no
// notifications. Playing is what got the Clarion X9U6 (2014 Infiniti Q60) to start the audio.
func (d *DevExtRemote) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.state = extremote.PlayerStatePlaying
	d.track = 0
	d.numTracks = extRemoteNumTracks
	d.length = extRemoteTrackLength
	d.pos = 0
	d.since = d.now()
	d.enabled = 0
	d.changed = 0
	d.sentSec = ^uint32(0)
}

// advance brings pos up to now. Past the end of the track the next one starts (the same one,
// with one track) instead of the player stopping: stopped makes the head unit mute the audio.
func (d *DevExtRemote) advance() {
	now := d.now()
	elapsed := now.Sub(d.since)
	if d.state != extremote.PlayerStatePlaying || elapsed < 0 {
		d.since = now
		return
	}
	ms := elapsed / time.Millisecond
	d.since = d.since.Add(ms * time.Millisecond)
	pos := uint64(d.pos) + uint64(ms)
	if pos >= uint64(d.length) {
		d.track = int32((uint64(d.track) + pos/uint64(d.length)) % uint64(d.numTracks))
		pos %= uint64(d.length)
		d.changed |= 1 << extremote.PlayStatusChangeTrackIndex
	}
	d.pos = uint32(pos)
}

func (d *DevExtRemote) setState(state extremote.PlayerState) {
	if state == d.state {
		return
	}
	d.state = state
	d.changed |= 1 << extremote.PlayStatusChangeExtendedStatus
	if state == extremote.PlayerStateStopped {
		d.changed |= 1 << extremote.PlayStatusChangeStopped
	}
}

func (d *DevExtRemote) setPos(pos uint32) {
	d.pos = pos
	d.changed |= 1<<extremote.PlayStatusChangeTrackTimeMs | 1<<extremote.PlayStatusChangeTrackTimeSec
}

func (d *DevExtRemote) setTrack(index int32) {
	d.track = index
	d.setPos(0)
	d.changed |= 1 << extremote.PlayStatusChangeTrackIndex
}

func (d *DevExtRemote) PlaybackStatus() (trackLength, trackPos uint32, state extremote.PlayerState) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.advance()
	return d.length, d.pos, d.state
}

func (d *DevExtRemote) CurrentTrack() (index int32, numTracks uint32) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.advance()
	return d.track, d.numTracks
}

func (d *DevExtRemote) PlayControl(cmd extremote.PlayControlCmd) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.advance()
	n := int32(d.numTracks)
	switch cmd {
	case extremote.PlayControlToggle:
		if d.state == extremote.PlayerStatePlaying {
			d.setState(extremote.PlayerStatePaused)
		} else {
			d.setState(extremote.PlayerStatePlaying)
		}
	case extremote.PlayControlPlay:
		d.setState(extremote.PlayerStatePlaying)
	case extremote.PlayControlPause:
		if d.state == extremote.PlayerStatePlaying {
			d.setState(extremote.PlayerStatePaused)
		}
	case extremote.PlayControlStop:
		d.setState(extremote.PlayerStateStopped)
		d.setPos(0)
	case extremote.PlayControlNextTrack, extremote.PlayControlNext:
		d.setTrack((d.track + 1) % n)
	case extremote.PlayControlPrevTrack, extremote.PlayControlPrev:
		d.setTrack((d.track + n - 1) % n)
	}
	// fast forward/rewind and chapters: nothing to seek in, ACK only
}

func (d *DevExtRemote) SetCurrentTrack(index int32, play bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.advance()
	if index < 0 || uint32(index) >= d.numTracks {
		index = 0
	}
	d.setTrack(index)
	if play {
		d.setState(extremote.PlayerStatePlaying)
	}
}

func (d *DevExtRemote) SetPlayStatusNotifications(enabled extremote.PlayStatusChanges) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.advance()
	d.enabled = enabled
	d.changed = 0
	d.sentSec = ^uint32(0)
}

func extendedPlayStatus(state extremote.PlayerState) extremote.ExtendedPlayStatus {
	switch state {
	case extremote.PlayerStatePlaying:
		return extremote.ExtendedPlayStatusPlaying
	case extremote.PlayerStatePaused:
		return extremote.ExtendedPlayStatusPaused
	default:
		return extremote.ExtendedPlayStatusStopped
	}
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

// Notifications returns the PlayStatusChangeNotifications to send now, to be called every
// 500 ms as an iPod does: the enabled changes since the last call and, while playing, the
// track position (the seconds one only when it changed).
func (d *DevExtRemote) Notifications() []*extremote.PlayStatusChangeNotification {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.advance()
	if d.state == extremote.PlayerStatePlaying {
		d.changed |= 1 << extremote.PlayStatusChangeTrackTimeMs
		if d.pos/1000 != d.sentSec {
			d.changed |= 1 << extremote.PlayStatusChangeTrackTimeSec
		}
	}
	due := d.changed & d.enabled
	d.changed = 0

	var notes []*extremote.PlayStatusChangeNotification
	for code := extremote.PlayStatusChangeStopped; code <= extremote.PlayStatusChangeTrackTimeSec; code++ {
		if !due.Has(code) {
			continue
		}
		n := &extremote.PlayStatusChangeNotification{Status: code}
		switch code {
		case extremote.PlayStatusChangeTrackIndex:
			n.Data = u32(uint32(d.track))
		case extremote.PlayStatusChangeTrackTimeMs:
			n.Data = u32(d.pos)
		case extremote.PlayStatusChangeExtendedStatus:
			n.Data = []byte{byte(extendedPlayStatus(d.state))}
		case extremote.PlayStatusChangeTrackTimeSec:
			d.sentSec = d.pos / 1000
			n.Data = u32(d.sentSec)
		}
		notes = append(notes, n)
	}
	return notes
}
