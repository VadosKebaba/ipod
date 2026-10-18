package main

import (
	"errors"
	"io/ioutil"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oandrew/ipod"
	extremote "github.com/oandrew/ipod/lingo-extremote"
	general "github.com/oandrew/ipod/lingo-general"
)

// fakePhone is a phoneSource: a phone whose state the test sets, which records the calls.
type fakePhone struct {
	mu    sync.Mutex
	st    phoneState
	err   error
	calls []string
	reads int
	// onCall, if set, is the phone acting on a call (under mu)
	onCall func(st *phoneState, method string)
}

func (f *fakePhone) Read() (phoneState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return f.st, f.err
}

func (f *fakePhone) Call(player, method string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if player != f.st.Player {
		return errors.New("no such player " + player)
	}
	f.calls = append(f.calls, method)
	if f.onCall != nil {
		f.onCall(&f.st, method)
	}
	return nil
}

func (f *fakePhone) set(change func(st *phoneState)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(&f.st)
}

func (f *fakePhone) takeCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls
	f.calls = nil
	return c
}

func iPhone(status string, track phoneTrack) phoneState {
	return phoneState{
		Name: "Vadym's iPhone", Address: "AA:BB:CC:DD:EE:FF", Codec: "SBC",
		Player: "/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF/player0", Status: status, Track: track,
	}
}

var trackA = phoneTrack{Title: "Группа крови", Artist: "Кино", Album: "Группа крови", Duration: 285000}

// quiet discards the log for the test.
func quiet(t *testing.T) {
	prev := log.Out
	log.Out = ioutil.Discard
	t.Cleanup(func() { log.Out = prev })
}

// testPhone is a BluezPlayer on fake, with the car streaming as *streaming, no periodic
// refresh and no re-reads after commands. Not started.
func testPhone(t *testing.T, fake *fakePhone, clk *fakeClock, streaming *string) *BluezPlayer {
	quiet(t)
	now := time.Now
	if clk != nil {
		now = clk.now
	}
	p := newBluezPlayer(fake, now, textOut{mode: textAsIs, max: 200}, true)
	p.streaming = func() string { return *streaming }
	p.refreshEvery = time.Hour
	p.rereads = nil
	p.holdFor = 0
	return p
}

func TestPhoneAction(t *testing.T) {
	type c = extremote.PlayControlCmd
	tests := []struct {
		cmd       c
		status    string
		streaming string
		guard     bool
		want      string
	}{
		{extremote.PlayControlToggle, "playing", "Y", true, "Pause"},
		{extremote.PlayControlToggle, "playing", "N", true, "Pause"}, // leaving the source: pause is fine
		{extremote.PlayControlToggle, "forward-seek", "N", true, "Pause"},
		{extremote.PlayControlToggle, "paused", "Y", true, "Play"},
		{extremote.PlayControlToggle, "stopped", "Y", true, "Play"},
		{extremote.PlayControlToggle, "paused", "N", true, ""}, // never starts the phone
		{extremote.PlayControlToggle, "stopped", "N", true, ""},
		{extremote.PlayControlToggle, "paused", "N", false, "Play"},
		{extremote.PlayControlToggle, "paused", "?", true, "Play"}, // no g_ipod_audio: no guard
		{extremote.PlayControlPlay, "paused", "N", true, "Play"},
		{extremote.PlayControlPlay, "playing", "Y", true, "Play"},
		{extremote.PlayControlPause, "playing", "Y", true, "Pause"},
		{extremote.PlayControlPause, "paused", "Y", true, "Pause"},
		{extremote.PlayControlStop, "playing", "Y", true, "Pause"},
		{extremote.PlayControlNextTrack, "playing", "Y", true, "Next"},
		{extremote.PlayControlNext, "paused", "N", true, "Next"},
		{extremote.PlayControlNextChapter, "playing", "Y", true, "Next"},
		{extremote.PlayControlPrevTrack, "playing", "Y", true, "Previous"},
		{extremote.PlayControlPrev, "playing", "Y", true, "Previous"},
		{extremote.PlayControlPrevChapter, "playing", "Y", true, "Previous"},
		{extremote.PlayControlStartFF, "playing", "Y", true, "FastForward"},
		{extremote.PlayControlStartRew, "playing", "Y", true, "Rewind"},
		{extremote.PlayControlEndFFRew, "forward-seek", "Y", true, "Play"},
		{extremote.PlayControlResumeIPod, "paused", "Y", true, ""},
	}
	for _, tt := range tests {
		got, why := phoneAction(tt.cmd, tt.status, tt.streaming, tt.guard)
		if got != tt.want {
			t.Errorf("%s with the phone %s, streaming %s, guard %v: %q (%s), want %q",
				playControlName(tt.cmd), tt.status, tt.streaming, tt.guard, got, why, tt.want)
		}
	}
}

func TestPlayerStateMapping(t *testing.T) {
	for status, want := range map[string]extremote.PlayerState{
		"playing": extremote.PlayerStatePlaying, "forward-seek": extremote.PlayerStatePlaying,
		"reverse-seek": extremote.PlayerStatePlaying, "paused": extremote.PlayerStatePaused,
		"stopped": extremote.PlayerStatePaused, "error": extremote.PlayerStatePaused, "": extremote.PlayerStatePaused,
	} {
		if got := playerState(status); got != want {
			t.Errorf("%q: %v, want %v", status, got, want)
		}
	}
}

// A toggle is decided from the phone as it is when the command runs, not from the cache.
func TestBluezPlayerToggleFreshRead(t *testing.T) {
	fake := &fakePhone{st: iPhone("paused", trackA)}
	streaming := "Y"
	p := testPhone(t, fake, nil, &streaming)
	p.start()
	defer p.Close()

	// started from the phone's own screen; the cache still says paused
	fake.set(func(st *phoneState) { st.Status = "playing" })
	if _, _, state := p.PlaybackStatus(); state != extremote.PlayerStatePaused {
		t.Fatalf("cache: %v, want paused", state)
	}
	p.PlayControl(extremote.PlayControlToggle)
	p.busy.Wait()
	if calls := fake.takeCalls(); !reflect.DeepEqual(calls, []string{"Pause"}) {
		t.Fatalf("toggle on a playing phone (stale cache paused): calls %v, want [Pause]", calls)
	}
	if _, _, state := p.PlaybackStatus(); state != extremote.PlayerStatePaused {
		t.Fatalf("after Pause: %v, want paused (expected until the next read)", state)
	}

	// and the other way round
	p.refresh()
	fake.set(func(st *phoneState) { st.Status = "paused" })
	p.PlayControl(extremote.PlayControlToggle)
	p.busy.Wait()
	if calls := fake.takeCalls(); !reflect.DeepEqual(calls, []string{"Play"}) {
		t.Fatalf("toggle on a paused phone: calls %v, want [Play]", calls)
	}
	if _, _, state := p.PlaybackStatus(); state != extremote.PlayerStatePlaying {
		t.Fatalf("after Play: %v, want playing", state)
	}
}

// The source-leave rule: a toggle while the car takes no audio pauses but never plays.
func TestBluezPlayerToggleGuard(t *testing.T) {
	fake := &fakePhone{st: iPhone("playing", trackA)}
	streaming := "N"
	p := testPhone(t, fake, nil, &streaming)
	p.start()
	defer p.Close()

	p.PlayControl(extremote.PlayControlToggle) // leaving the iPod source, the phone plays
	p.busy.Wait()
	if calls := fake.takeCalls(); !reflect.DeepEqual(calls, []string{"Pause"}) {
		t.Fatalf("toggle, phone playing, car not streaming: %v, want [Pause]", calls)
	}
	fake.set(func(st *phoneState) { st.Status = "paused" })
	p.PlayControl(extremote.PlayControlToggle) // leaving the iPod source, the phone paused
	p.busy.Wait()
	if calls := fake.takeCalls(); len(calls) != 0 {
		t.Fatalf("toggle, phone paused, car not streaming: %v, want none", calls)
	}
	p.PlayControl(extremote.PlayControlPlay) // an explicit play still plays
	p.busy.Wait()
	if calls := fake.takeCalls(); !reflect.DeepEqual(calls, []string{"Play"}) {
		t.Fatalf("play, car not streaming: %v, want [Play]", calls)
	}
	streaming = "Y"
	fake.set(func(st *phoneState) { st.Status = "paused" })
	p.PlayControl(extremote.PlayControlToggle)
	p.busy.Wait()
	if calls := fake.takeCalls(); !reflect.DeepEqual(calls, []string{"Play"}) {
		t.Fatalf("toggle, phone paused, car streaming: %v, want [Play]", calls)
	}
}

func TestBluezPlayerCommands(t *testing.T) {
	fake := &fakePhone{st: iPhone("playing", trackA)}
	streaming := "Y"
	p := testPhone(t, fake, nil, &streaming)
	p.start()
	defer p.Close()

	for _, cmd := range []extremote.PlayControlCmd{
		extremote.PlayControlNextTrack, extremote.PlayControlPrevTrack, extremote.PlayControlNext,
		extremote.PlayControlPrev, extremote.PlayControlStartFF, extremote.PlayControlEndFFRew,
		extremote.PlayControlStartRew, extremote.PlayControlResumeIPod, extremote.PlayControlStop,
		extremote.PlayControlPlay, extremote.PlayControlPause,
	} {
		p.PlayControl(cmd)
	}
	p.busy.Wait()
	want := []string{"Next", "Previous", "Next", "Previous", "FastForward", "Play", "Rewind", "Pause", "Play", "Pause"}
	if calls := fake.takeCalls(); !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls %v, want %v", calls, want)
	}

	// SetCurrentTrack against the current index (2 of 4 here: no track numbers)
	if idx, n := p.CurrentTrack(); idx != 1 || n != 4 {
		t.Fatalf("track %d of %d, want index 1 of 4", idx, n)
	}
	p.SetCurrentTrack(2, false)
	p.SetCurrentTrack(0, false)
	p.SetCurrentTrack(1, false)
	p.SetCurrentTrack(1, true)
	p.SetCurrentTrack(-1, true)
	p.busy.Wait()
	want = []string{"Next", "Previous", "Play", "Play"}
	if calls := fake.takeCalls(); !reflect.DeepEqual(calls, want) {
		t.Fatalf("SetCurrentTrack calls %v, want %v", calls, want)
	}

	// no phone: nothing called, nothing stuck
	fake.set(func(st *phoneState) { *st = phoneState{} })
	p.PlayControl(extremote.PlayControlNextTrack)
	p.busy.Wait()
	if calls := fake.takeCalls(); len(calls) != 0 {
		t.Fatalf("no phone: calls %v", calls)
	}
}

func TestBluezPlayerIndex(t *testing.T) {
	fake := &fakePhone{st: iPhone("playing", trackA)}
	streaming := "Y"
	p := testPhone(t, fake, nil, &streaming)
	p.SetPlayStatusNotifications(extremote.PlayStatusChangesFromMask(0x04)) // track index only
	check := func(step string, wantIndex int32, wantNum uint32, wantNotes ...string) {
		t.Helper()
		p.refresh()
		if idx, n := p.CurrentTrack(); idx != wantIndex || n != wantNum {
			t.Fatalf("%s: index %d of %d, want %d of %d", step, idx, n, wantIndex, wantNum)
		}
		if got := notes(p.Notifications()); !eq(got, wantNotes) {
			t.Fatalf("%s: notifications %q, want %q", step, got, wantNotes)
		}
	}
	check("first track, no numbers", 1, 4, "1:1")
	check("same track", 1, 4)
	fake.set(func(st *phoneState) { st.Track.Title = "Кукушка" })
	check("next track", 2, 4, "1:2")
	fake.set(func(st *phoneState) { st.Track.Title = "Звезда по имени Солнце" })
	check("and the next", 1, 4, "1:1")
	fake.set(func(st *phoneState) { st.Position = 5000 })
	check("position only", 1, 4)
	// the rest of the same track arriving late: the same index, reported again so the car
	// reads the names once more
	fake.set(func(st *phoneState) { st.Track.Duration = 1190000 })
	check("duration only (the iPhone sends it late): same track", 1, 4, "1:1")
	fake.set(func(st *phoneState) { st.Track.Album = "" })
	check("album gone", 1, 4, "1:1")
	fake.set(func(st *phoneState) { st.Track.Album = "Звезда по имени Солнце" })
	check("album arrives late: same track", 1, 4, "1:1")
	check("then quiet", 1, 4)

	fake.set(func(st *phoneState) { st.Track.Number, st.Track.Count = 5, 12 })
	check("phone's numbers", 4, 12, "1:4")
	fake.set(func(st *phoneState) { st.Track.Number = 12 })
	check("phone's last track", 11, 12, "1:11")
	fake.set(func(st *phoneState) { st.Track.Number, st.Track.Count = 1, 1 })
	// the window kept alternating through the numbered changes: 1, 2, 1, now 2
	check("a list of one: the window", 2, 4, "1:2")
	fake.set(func(st *phoneState) { st.Track.Number, st.Track.Count = 13, 12 })
	check("number past the count: the window", 1, 4, "1:1")
	fake.set(func(st *phoneState) { st.Track.Number, st.Track.Count = 0, 12 })
	check("no number: the window", 2, 4, "1:2")
}

func TestBluezPlayerPosition(t *testing.T) {
	clk := newFakeClock()
	track := trackA
	track.Duration = 5000
	fake := &fakePhone{st: iPhone("playing", track)}
	fake.st.Position = 1000
	streaming := "Y"
	p := testPhone(t, fake, clk, &streaming)
	p.refresh()
	check := func(step string, wantLen, wantPos uint32, wantState extremote.PlayerState) {
		t.Helper()
		if l, pos, state := p.PlaybackStatus(); l != wantLen || pos != wantPos || state != wantState {
			t.Fatalf("%s: %d / %d %v, want %d / %d %v", step, pos, l, state, wantPos, wantLen, wantState)
		}
	}
	check("read", 5000, 1000, extremote.PlayerStatePlaying)
	clk.add(1500 * time.Millisecond)
	check("1.5 s later", 5000, 2500, extremote.PlayerStatePlaying)
	clk.add(10 * time.Second)
	check("past the end: clamped", 5000, 4999, extremote.PlayerStatePlaying)

	fake.set(func(st *phoneState) { st.Status, st.Position = "paused", 3000 })
	p.refresh()
	clk.add(10 * time.Second)
	check("paused", 5000, 3000, extremote.PlayerStatePaused)
	fake.set(func(st *phoneState) { st.Status = "stopped" })
	p.refresh()
	check("stopped is paused", 5000, 3000, extremote.PlayerStatePaused)

	fake.set(func(st *phoneState) { st.Status, st.Track.Duration = "playing", 0 })
	p.refresh()
	clk.add(2 * time.Second)
	check("no length", 5000+phoneUnknownLength, 5000, extremote.PlayerStatePlaying)

	// notifications: state changes and the position while playing
	p.SetPlayStatusNotifications(extremote.PlayStatusChangesFromMask(0x1f))
	if got := notes(p.Notifications()); !eq(got, []string{"4:5000", "7:5"}) {
		t.Fatalf("playing: %q", got)
	}
	fake.set(func(st *phoneState) { st.Status = "paused" })
	p.refresh()
	if got := notes(p.Notifications()); !eq(got, []string{"6:0b"}) {
		t.Fatalf("paused: %q", got)
	}
	fake.set(func(st *phoneState) { st.Status = "stopped" })
	p.refresh()
	if got := notes(p.Notifications()); len(got) != 0 {
		t.Fatalf("stopped after paused (both paused to the car): %q", got)
	}
	fake.set(func(st *phoneState) { st.Status = "playing" })
	p.refresh()
	// a fresh read: the phone's 3000 again
	if got := notes(p.Notifications()); !eq(got, []string{"4:3000", "6:0a", "7:3"}) {
		t.Fatalf("playing again: %q", got)
	}
}

// After a Play or Pause the car is told the new status until the phone reports it, or for
// holdFor; a second press in that time is decided from it.
func TestBluezPlayerHold(t *testing.T) {
	clk := newFakeClock()
	fake := &fakePhone{st: iPhone("playing", trackA)}
	streaming := "Y"
	p := testPhone(t, fake, clk, &streaming)
	p.holdFor = 2500 * time.Millisecond
	p.start()
	defer p.Close()
	state := func(step string, want extremote.PlayerState) {
		t.Helper()
		if _, _, got := p.PlaybackStatus(); got != want {
			t.Fatalf("%s: %v, want %v", step, got, want)
		}
	}

	p.PlayControl(extremote.PlayControlToggle)
	p.busy.Wait()
	if calls := fake.takeCalls(); !reflect.DeepEqual(calls, []string{"Pause"}) {
		t.Fatalf("calls %v", calls)
	}
	clk.add(300 * time.Millisecond)
	p.refresh() // the iPhone still says playing
	state("300 ms, the phone still says playing", extremote.PlayerStatePaused)

	// pressed again: play, decided from the held paused, not the stale playing
	p.PlayControl(extremote.PlayControlToggle)
	p.busy.Wait()
	if calls := fake.takeCalls(); !reflect.DeepEqual(calls, []string{"Play"}) {
		t.Fatalf("second press: calls %v, want [Play]", calls)
	}
	state("after the second press", extremote.PlayerStatePlaying)

	// the phone agrees: the hold ends, and a later change from the phone shows at once
	p.refresh()
	fake.set(func(st *phoneState) { st.Status = "paused" })
	p.refresh()
	state("paused from the phone", extremote.PlayerStatePaused)

	// a Play the phone never does: held 2.5 s, then the phone's paused wins
	p.PlayControl(extremote.PlayControlPlay)
	p.busy.Wait()
	clk.add(2 * time.Second)
	p.refresh()
	state("2 s after a Play the phone ignored", extremote.PlayerStatePlaying)
	clk.add(time.Second)
	p.refresh()
	state("3 s after", extremote.PlayerStatePaused)
}

// BlueZ failing: the cache holds for phoneStaleAfter, then there is no phone.
func TestBluezPlayerStale(t *testing.T) {
	clk := newFakeClock()
	fake := &fakePhone{st: iPhone("playing", trackA)}
	streaming := "Y"
	p := testPhone(t, fake, clk, &streaming)
	p.refresh()
	fake.set(func(*phoneState) {})
	fake.err = errors.New("timeout")
	clk.add(5 * time.Second)
	p.refresh()
	if !p.HasPlayer() {
		t.Fatal("phone dropped after 5 s of errors")
	}
	clk.add(5 * time.Second)
	p.refresh()
	if p.HasPlayer() {
		t.Fatal("phone kept after 10 s of errors")
	}
	fake.err = nil
	p.refresh()
	if !p.HasPlayer() {
		t.Fatal("phone not back")
	}
}

func TestBluezPlayerTrackInfo(t *testing.T) {
	fake := &fakePhone{st: iPhone("playing", trackA)}
	streaming := "Y"
	p := testPhone(t, fake, nil, &streaming)
	p.refresh()
	check := func(step string, wantTitle, wantArtist, wantAlbum string) {
		t.Helper()
		for _, index := range []int32{0, 1, 3, 77} {
			title, artist, album := p.TrackInfo(index)
			if title != wantTitle || artist != wantArtist || album != wantAlbum {
				t.Fatalf("%s, index %d: %q %q %q, want %q %q %q", step, index, title, artist, album,
					wantTitle, wantArtist, wantAlbum)
			}
		}
	}
	check("asis", "Группа крови", "Кино", "Группа крови")
	p.text = textOut{mode: textTranslit, max: 200}
	check("translit", "Gruppa krovi", "Kino", "Gruppa krovi")
	p.text = textOut{mode: textAsIs, max: 7}
	check("7 bytes: 3 Cyrillic letters", "Гру", "Кин", "Гру")

	fake.set(func(st *phoneState) { st.Track = phoneTrack{} })
	p.refresh()
	p.text = textOut{mode: textAsIs, max: 200}
	check("no title: the phone's name", "Vadym's iPhone", "", "")
}

func TestAutoPlayerSwitch(t *testing.T) {
	clk := newFakeClock()
	fake := &fakePhone{}
	streaming := "Y"
	phone := testPhone(t, fake, clk, &streaming)
	phone.refresh()
	stub := NewDevExtRemote(clk.now)
	a := newAutoPlayer(stub, phone, textOut{mode: textAsIs, max: 200}, nil)
	a.SetPlayStatusNotifications(extremote.PlayStatusChangesFromMask(0x07)) // basic, extended, track index
	tick := func(step string, want ...string) {
		t.Helper()
		if got := notes(a.Notifications()); !eq(got, want) {
			t.Fatalf("%s: notifications %q, want %q", step, got, want)
		}
	}
	status := func(step string, wantState extremote.PlayerState, wantIndex int32, wantNum uint32, wantTitle string) {
		t.Helper()
		_, _, state := a.PlaybackStatus()
		idx, n := a.CurrentTrack()
		title, _, _ := a.TrackInfo(idx)
		if state != wantState || idx != wantIndex || n != wantNum || title != wantTitle {
			t.Fatalf("%s: %v, %d of %d, %q; want %v, %d of %d, %q", step, state, idx, n, title,
				wantState, wantIndex, wantNum, wantTitle)
		}
	}

	status("no phone: the stub", extremote.PlayerStatePlaying, 0, 1, "title")
	tick("no phone")
	if a.OnPhone() {
		t.Fatal("on the phone without one")
	}
	a.PlayControl(extremote.PlayControlPause) // the car pauses the stub
	tick("stub paused", "6:0b")

	fake.set(func(st *phoneState) { *st = iPhone("paused", trackA) })
	phone.refresh()
	status("phone connected", extremote.PlayerStatePaused, 1, 4, "Группа крови")
	tick("switch to the phone: everything", "1:1", "6:0b")
	tick("then quiet")

	fake.set(func(st *phoneState) { st.Status = "playing" })
	phone.refresh()
	tick("phone plays", "6:0a")

	a.PlayControl(extremote.PlayControlPause) // to the phone, not the stub
	if _, _, state := stub.PlaybackStatus(); state != extremote.PlayerStatePaused {
		t.Fatalf("stub state %v changed by a command for the phone", state)
	}

	fake.set(func(st *phoneState) { *st = phoneState{} })
	phone.refresh()
	status("phone gone: the stub, playing again", extremote.PlayerStatePlaying, 0, 1, "title")
	tick("switch to the stub: everything", "1:0", "6:0a")
	tick("stub quiet")

	a.Reset()
	fake.set(func(st *phoneState) { *st = iPhone("playing", trackA) })
	phone.refresh()
	tick("after Reset, notifications are off on both sides")
}

func TestAutoPlayerDemoTrack(t *testing.T) {
	opts, err := parsePlayerOptions(playerStub, textTranslit, 10, "Звезда по имени Солнце|Кино", true)
	if err != nil {
		t.Fatal(err)
	}
	a := opts.newPlayer()
	defer a.Close()
	if a.phone != nil {
		t.Fatal("--player=stub made a phone player")
	}
	if title, artist, album := a.TrackInfo(0); title != "Zvezda po " || artist != "Kino" || album != "" {
		t.Fatalf("demo track: %q %q %q", title, artist, album)
	}
	a = newAutoPlayer(NewDevExtRemote(time.Now), nil, textOut{mode: textAsIs}, nil)
	if title, artist, album := a.TrackInfo(0); title != "title" || artist != "artist" || album != "album" {
		t.Fatalf("Level 2 placeholders: %q %q %q", title, artist, album)
	}
}

func TestParsePlayerOptions(t *testing.T) {
	for _, bad := range [][]interface{}{
		{"phone", textAsIs, 200}, {playerAuto, "latin", 200}, {playerAuto, textAsIs, -1},
	} {
		if _, err := parsePlayerOptions(bad[0].(string), bad[1].(string), bad[2].(int), "", true); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	o, err := parsePlayerOptions(playerAuto, textAsIs, 200, "", true)
	if err != nil || o.demo != nil || !o.toggleGuard || o.text.max != 200 {
		t.Fatalf("%+v %v", o, err)
	}
	if !strings.Contains(o.String(), "toggle guard on") {
		t.Fatalf("%s", o)
	}
}

// End to end: the car's PlayControl reaches the phone, and the car's track name requests
// get the phone's names, through processFrames.
func TestPhoneServed(t *testing.T) {
	prevInterval := notifyInterval
	notifyInterval = 20 * time.Millisecond
	defer func() { notifyInterval = prevInterval }()

	fake := &fakePhone{st: iPhone("playing", trackA)}
	fake.onCall = func(st *phoneState, method string) {
		if method == "Next" {
			st.Track = phoneTrack{Title: "Кукушка", Artist: "Кино", Album: "Последний герой", Duration: 400000}
		}
	}
	streaming := "Y"
	phone := testPhone(t, fake, nil, &streaming)
	phone.rereads = []time.Duration{10 * time.Millisecond}
	prevNewPlayer := newPlayer
	newPlayer = func() Player {
		phone.start()
		return newAutoPlayer(NewDevExtRemote(time.Now), phone, textOut{mode: textAsIs, max: 200}, nil)
	}
	defer func() { newPlayer = prevNewPlayer }()

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
	// reply waits for the reply to transaction trx and returns its payload
	reply := func(what string, trx uint16) interface{} {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			for _, c := range tr.written(t) {
				if c.Transaction != nil && uint16(*c.Transaction) == trx {
					if _, ok := c.Payload.(*extremote.PlayStatusChangeNotification); !ok {
						return c.Payload
					}
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("no reply to %s", what)
		return nil
	}
	title := func() string {
		t.Helper()
		r := reply("GetIndexedPlayingTrackTitle",
			send(&extremote.GetIndexedPlayingTrackTitle{TrackIndex: 1})).(*extremote.ReturnIndexedPlayingTrackTitle)
		return cString(r.Title)
	}

	reply("StartIDPS", send(&general.StartIDPS{}))
	if got := title(); got != "Группа крови" {
		t.Fatalf("title %q", got)
	}
	r := reply("GetIndexedPlayingTrackArtistName",
		send(&extremote.GetIndexedPlayingTrackArtistName{TrackIndex: 1})).(*extremote.ReturnIndexedPlayingTrackArtistName)
	if got := cString(r.ArtistName); got != "Кино" {
		t.Fatalf("artist %q", got)
	}
	ps := reply("GetPlayStatus", send(&extremote.GetPlayStatus{})).(*extremote.ReturnPlayStatus)
	if ps.State != extremote.PlayerStatePlaying || ps.TrackLength != 285000 {
		t.Fatalf("play status %+v", ps)
	}
	idx := reply("GetCurrentPlayingTrackIndex", send(&extremote.GetCurrentPlayingTrackIndex{})).(*extremote.ReturnCurrentPlayingTrackIndex)
	if idx.TrackIndex != 1 {
		t.Fatalf("index %d", idx.TrackIndex)
	}

	if ack := reply("PlayControl next", send(&extremote.PlayControl{Cmd: extremote.PlayControlNextTrack})).(*extremote.ACK); ack.Status != extremote.ACKStatusSuccess {
		t.Fatalf("ack %+v", ack)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		idx := reply("GetCurrentPlayingTrackIndex", send(&extremote.GetCurrentPlayingTrackIndex{})).(*extremote.ReturnCurrentPlayingTrackIndex)
		if idx.TrackIndex == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the track index never moved after next")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if calls := fake.takeCalls(); !reflect.DeepEqual(calls, []string{"Next"}) {
		t.Fatalf("calls %v", calls)
	}
	if got := title(); got != "Кукушка" {
		t.Fatalf("title after next %q", got)
	}

	close(tr.in)
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("processFrames did not return after EOF")
	}
	select {
	case <-phone.done:
	default:
		t.Fatal("processFrames did not close the player")
	}
}

// cString is a null-terminated iAP string.
func cString(b []byte) string {
	return strings.TrimSuffix(string(b), "\x00")
}
