package main

import (
	"encoding/binary"
	"io"
	"io/ioutil"
	"sync"
	"testing"
	"time"

	"github.com/oandrew/ipod"
	extremote "github.com/oandrew/ipod/lingo-extremote"
	general "github.com/oandrew/ipod/lingo-general"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time      { return c.t }
func (c *fakeClock) add(d time.Duration) { c.t = c.t.Add(d) }
func newFakeClock() *fakeClock           { return &fakeClock{t: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)} }
func (d *DevExtRemote) status() (uint32, extremote.PlayerState) {
	_, pos, state := d.PlaybackStatus()
	return pos, state
}

func TestDevExtRemotePlayState(t *testing.T) {
	clk := newFakeClock()
	d := NewDevExtRemote(clk.now)
	check := func(step string, wantPos uint32, wantState extremote.PlayerState) {
		t.Helper()
		if pos, state := d.status(); pos != wantPos || state != wantState {
			t.Fatalf("%s: pos %d state %d, want %d %d", step, pos, state, wantPos, wantState)
		}
	}

	check("on connect", 0, extremote.PlayerStatePlaying)
	clk.add(2500 * time.Millisecond)
	check("playing 2.5 s", 2500, extremote.PlayerStatePlaying)

	d.PlayControl(extremote.PlayControlPause)
	clk.add(10 * time.Second)
	check("paused", 2500, extremote.PlayerStatePaused)
	d.PlayControl(extremote.PlayControlPause)
	check("pause again", 2500, extremote.PlayerStatePaused)

	d.PlayControl(extremote.PlayControlToggle)
	clk.add(1 * time.Second)
	check("toggle to playing", 3500, extremote.PlayerStatePlaying)
	d.PlayControl(extremote.PlayControlToggle)
	clk.add(1 * time.Second)
	check("toggle to paused", 3500, extremote.PlayerStatePaused)

	d.PlayControl(extremote.PlayControlPlay)
	clk.add(500 * time.Millisecond)
	check("play", 4000, extremote.PlayerStatePlaying)

	d.PlayControl(extremote.PlayControlStop)
	clk.add(1 * time.Second)
	check("stop", 0, extremote.PlayerStateStopped)
	d.PlayControl(extremote.PlayControlPause)
	check("pause when stopped", 0, extremote.PlayerStateStopped)
	d.PlayControl(extremote.PlayControlToggle)
	clk.add(1 * time.Second)
	check("toggle when stopped", 1000, extremote.PlayerStatePlaying)

	d.PlayControl(extremote.PlayControlNextTrack)
	clk.add(300 * time.Millisecond)
	check("next", 300, extremote.PlayerStatePlaying)
	d.PlayControl(extremote.PlayControlPrev)
	check("prev", 0, extremote.PlayerStatePlaying)
	if idx, n := d.CurrentTrack(); idx != 0 || n != 1 {
		t.Fatalf("track %d of %d, want 0 of 1", idx, n)
	}

	for _, cmd := range []extremote.PlayControlCmd{extremote.PlayControlStartFF, extremote.PlayControlStartRew,
		extremote.PlayControlEndFFRew, extremote.PlayControlNextChapter, extremote.PlayControlPrevChapter} {
		d.PlayControl(cmd)
	}
	clk.add(time.Second)
	check("seek/chapter commands change nothing", 1000, extremote.PlayerStatePlaying)

	// sub-millisecond steps add up
	for i := 0; i < 1000; i++ {
		clk.add(1500 * time.Microsecond)
		d.PlaybackStatus()
	}
	check("1000 x 1.5 ms", 2500, extremote.PlayerStatePlaying)

	// past the end: starts over, still playing
	clk.add(extRemoteTrackLength*time.Millisecond - 2500*time.Millisecond + 700*time.Millisecond)
	check("wrapped", 700, extremote.PlayerStatePlaying)

	d.PlayControl(extremote.PlayControlPause)
	d.SetCurrentTrack(0, false)
	check("SetCurrentPlayingTrack keeps paused", 0, extremote.PlayerStatePaused)
	clk.add(time.Second)
	d.SetCurrentTrack(-1, true)
	clk.add(time.Second)
	check("PlayCurrentSelection plays", 1000, extremote.PlayerStatePlaying)

	d.PlayControl(extremote.PlayControlStop)
	d.Reset()
	check("reset", 0, extremote.PlayerStatePlaying)
}

// notes renders notifications as "status:data" for comparison.
func notes(ns []*extremote.PlayStatusChangeNotification) []string {
	var out []string
	for _, n := range ns {
		s := string("0123456789abcdef"[n.Status])
		switch len(n.Data) {
		case 1:
			s += ":" + string("0123456789abcdef"[n.Data[0]>>4]) + string("0123456789abcdef"[n.Data[0]&0xf])
		case 4:
			s += ":" + itoa(binary.BigEndian.Uint32(n.Data))
		}
		out = append(out, s)
	}
	return out
}

func itoa(v uint32) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for ; v > 0; v /= 10 {
		b = append([]byte{byte('0' + v%10)}, b...)
	}
	return string(b)
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDevExtRemoteNotifications(t *testing.T) {
	clk := newFakeClock()
	d := NewDevExtRemote(clk.now)
	tick := func(step string, want ...string) {
		t.Helper()
		if got := notes(d.Notifications()); !eq(got, want) {
			t.Fatalf("%s: notifications %q, want %q", step, got, want)
		}
	}

	clk.add(time.Second)
	tick("not enabled")

	// 1-byte form: stopped, track index, seek stops, position (ms), chapter
	d.SetPlayStatusNotifications(extremote.PlayStatusChangesShort)
	tick("enabled", "4:1000")
	clk.add(500 * time.Millisecond)
	tick("500 ms later", "4:1500")
	d.PlayControl(extremote.PlayControlPause)
	clk.add(500 * time.Millisecond)
	tick("paused: no extended status in the 1-byte set, position frozen")
	tick("paused, again")
	d.PlayControl(extremote.PlayControlPlay)
	clk.add(250 * time.Millisecond)
	tick("playing", "4:1750")
	d.PlayControl(extremote.PlayControlNextTrack)
	tick("next track", "1:0", "4:0")
	d.PlayControl(extremote.PlayControlStop)
	tick("stop", "0", "4:0")
	tick("stopped")

	// 4-byte form: basic, extended, track index, position (ms), position (s)
	d.SetPlayStatusNotifications(extremote.PlayStatusChangesFromMask(0x1f))
	tick("mask enabled, stopped")
	d.PlayControl(extremote.PlayControlToggle)
	clk.add(400 * time.Millisecond)
	tick("toggle", "4:400", "6:0a", "7:0")
	clk.add(400 * time.Millisecond)
	tick("same second", "4:800")
	clk.add(400 * time.Millisecond)
	tick("next second", "4:1200", "7:1")
	d.PlayControl(extremote.PlayControlPause)
	tick("pause", "6:0b")
	d.PlayControl(extremote.PlayControlStop)
	tick("stop", "0", "4:0", "6:02", "7:0")

	// only the extended status
	d.SetPlayStatusNotifications(extremote.PlayStatusChangesFromMask(0x02))
	d.PlayControl(extremote.PlayControlPlay)
	clk.add(time.Second)
	tick("extended only", "6:0a")
	clk.add(extRemoteTrackLength * time.Millisecond)
	tick("wrap: no track index asked for")

	d.SetPlayStatusNotifications(extremote.PlayStatusChangesFromMask(0x04))
	clk.add(extRemoteTrackLength * time.Millisecond)
	tick("wrap reports the next track", "1:0")

	d.SetPlayStatusNotifications(0)
	d.PlayControl(extremote.PlayControlStop)
	tick("disabled")

	d.SetPlayStatusNotifications(extremote.PlayStatusChangesShort)
	d.Reset()
	d.PlayControl(extremote.PlayControlStop)
	tick("reset disables notifications")
}

// chanTransport feeds processFrames frames until closed, and keeps what it writes.
type chanTransport struct {
	in chan []byte

	mu  sync.Mutex
	out [][]byte
}

func (t *chanTransport) ReadFrame() ([]byte, error) {
	f, ok := <-t.in
	if !ok {
		return nil, io.EOF
	}
	return f, nil
}

func (t *chanTransport) WriteFrame(data []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.out = append(t.out, append([]byte(nil), data...))
	return nil
}

// written decodes everything written so far, as the head unit does after StartIDPS.
func (t *chanTransport) written(tb testing.TB) []*ipod.Command {
	t.mu.Lock()
	defer t.mu.Unlock()
	serde := ipod.CommandSerde{TrxEnabled: true}
	var cmds []*ipod.Command
	for _, f := range t.out {
		pr := ipod.NewPacketReader(f)
		for {
			pkt, err := pr.ReadPacket()
			if err == io.EOF {
				break
			}
			if err != nil {
				tb.Fatalf("client wrote an unreadable packet: %v", err)
			}
			cmd, err := serde.UnmarshalCmd(pkt)
			if err != nil {
				tb.Fatalf("client wrote an undecodable command: %v", err)
			}
			cmds = append(cmds, cmd)
		}
	}
	return cmds
}

func TestExtRemoteNotificationsServed(t *testing.T) {
	prevInterval := notifyInterval
	notifyInterval = 20 * time.Millisecond
	defer func() { notifyInterval = prevInterval }()
	prevOut := log.Out
	log.Out = ioutil.Discard
	defer func() { log.Out = prevOut }()

	tr := &chanTransport{in: make(chan []byte)}
	served := make(chan struct{})
	go func() {
		processFrames(tr)
		close(served)
	}()

	carSerde := ipod.CommandSerde{}
	var trx uint16
	send := func(payload interface{}) uint16 {
		t.Helper()
		cmd, err := ipod.BuildCommand(payload)
		if err != nil {
			t.Fatal(err)
		}
		cmd.Transaction = ipod.NewTransaction(trx)
		pkt, err := carSerde.MarshalCmd(cmd)
		if err != nil {
			t.Fatal(err)
		}
		pw := ipod.NewPacketWriter()
		pw.WritePacket(pkt)
		tr.in <- pw.Bytes()
		trx++
		return trx - 1
	}
	// waitFor waits until match has seen n of the commands written since from
	waitFor := func(what string, from, n int, match func(*ipod.Command) bool) int {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			cmds := tr.written(t)
			seen := 0
			for i := from; i < len(cmds); i++ {
				if match(cmds[i]) {
					seen++
					if seen == n {
						return i + 1
					}
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s; written: %s", what, spewCmds(cmds[from:]))
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	ack := func(trx uint16, cmdID uint16) func(*ipod.Command) bool {
		return func(c *ipod.Command) bool {
			a, ok := c.Payload.(*extremote.ACK)
			return ok && a.CmdID == cmdID && a.Status == extremote.ACKStatusSuccess &&
				c.Transaction != nil && uint16(*c.Transaction) == trx
		}
	}
	note := func(status extremote.PlayStatusChange) func(*ipod.Command) bool {
		return func(c *ipod.Command) bool {
			n, ok := c.Payload.(*extremote.PlayStatusChangeNotification)
			return ok && n.Status == status
		}
	}
	notePos := func(c *ipod.Command) uint32 {
		return binary.BigEndian.Uint32(c.Payload.(*extremote.PlayStatusChangeNotification).Data)
	}

	idps := send(&general.StartIDPS{})
	at := waitFor("ACK StartIDPS", 0, 1, func(c *ipod.Command) bool {
		a, ok := c.Payload.(*general.ACK)
		return ok && a.CmdID == 0x38 && c.Transaction != nil && uint16(*c.Transaction) == idps
	})

	at = waitFor("ACK SetPlayStatusChangeNotification (1 byte)", at, 1,
		ack(send(&extremote.SetPlayStatusChangeNotificationShort{Enabled: true}), 0x0026))
	from := at
	at = waitFor("3 position notifications", at, 3, note(extremote.PlayStatusChangeTrackTimeMs))
	var positions []uint32
	for _, c := range tr.written(t)[from:at] {
		if note(extremote.PlayStatusChangeTrackTimeMs)(c) {
			positions = append(positions, notePos(c))
			if c.Transaction == nil {
				t.Fatalf("notification without a transaction ID")
			}
		}
	}
	if !(positions[0] < positions[1] && positions[1] < positions[2]) {
		t.Fatalf("position not advancing: %v", positions)
	}

	at = waitFor("ACK PlayControl pause", at, 1, ack(send(&extremote.PlayControl{Cmd: extremote.PlayControlPause}), 0x0029))
	status := func() *extremote.ReturnPlayStatus {
		t.Helper()
		trx := send(&extremote.GetPlayStatus{})
		var ret *extremote.ReturnPlayStatus
		at = waitFor("ReturnPlayStatus", at, 1, func(c *ipod.Command) bool {
			r, ok := c.Payload.(*extremote.ReturnPlayStatus)
			if ok && c.Transaction != nil && uint16(*c.Transaction) == trx {
				ret = r
			}
			return ret != nil
		})
		return ret
	}
	paused := status()
	if paused.State != extremote.PlayerStatePaused || paused.TrackLength != extRemoteTrackLength {
		t.Fatalf("after pause: %+v", paused)
	}
	from = at
	time.Sleep(5 * notifyInterval)
	if again := status(); again.TrackPosition != paused.TrackPosition {
		t.Fatalf("position moved while paused: %d -> %d", paused.TrackPosition, again.TrackPosition)
	}
	for _, c := range tr.written(t)[from:] {
		if _, ok := c.Payload.(*extremote.PlayStatusChangeNotification); ok {
			t.Fatalf("notification while paused: %s", spewCmds([]*ipod.Command{c}))
		}
	}

	// 4-byte form: extended status + track index
	at = waitFor("ACK SetPlayStatusChangeNotification (4 bytes)", at, 1,
		ack(send(&extremote.SetPlayStatusChangeNotification{EventMask: 0x06}), 0x0026))
	at = waitFor("ACK PlayControl toggle", at, 1, ack(send(&extremote.PlayControl{Cmd: extremote.PlayControlToggle}), 0x0029))
	at = waitFor("extended status playing", at, 1, func(c *ipod.Command) bool {
		n, ok := c.Payload.(*extremote.PlayStatusChangeNotification)
		return ok && n.Status == extremote.PlayStatusChangeExtendedStatus &&
			len(n.Data) == 1 && n.Data[0] == byte(extremote.ExtendedPlayStatusPlaying)
	})
	at = waitFor("ACK PlayControl next", at, 1, ack(send(&extremote.PlayControl{Cmd: extremote.PlayControlNextTrack}), 0x0029))
	at = waitFor("track index", at, 1, note(extremote.PlayStatusChangeTrackIndex))
	if playing := status(); playing.State != extremote.PlayerStatePlaying {
		t.Fatalf("after toggle: %+v", playing)
	}
	at = waitFor("ACK SetCurrentPlayingTrack", at, 1, ack(send(&extremote.SetCurrentPlayingTrack{TrackIndex: 0}), 0x0037))

	close(tr.in)
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("processFrames did not return after EOF")
	}
	n := len(tr.written(t))
	time.Sleep(3 * notifyInterval)
	if len(tr.written(t)) != n {
		t.Fatal("notifications after processFrames returned")
	}
}

func spewCmds(cmds []*ipod.Command) string {
	s := ""
	for _, c := range cmds {
		s += "\n  " + c.ID.String()
		if n, ok := c.Payload.(*extremote.PlayStatusChangeNotification); ok {
			s += " " + notes([]*extremote.PlayStatusChangeNotification{n})[0]
		}
	}
	return s
}
