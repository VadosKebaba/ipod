package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"strings"
	"sync"
	"time"

	extremote "github.com/oandrew/ipod/lingo-extremote"
)

// The phone over Bluetooth AVRCP, as BlueZ shows it (org.bluez.MediaPlayer1), as the
// Extended Remote lingo's Player: the car's screen shows the phone's song and the car's
// buttons control the phone.

// phoneTrack is MediaPlayer1.Track.
type phoneTrack struct {
	Title, Artist, Album string
	Duration             uint32 // ms, 0: unknown
	Number, Count        uint32 // TrackNumber, NumberOfTracks; 0: unknown
}

// phoneState is what BlueZ says about the phone that plays.
type phoneState struct {
	Name, Address string
	Player        string // MediaPlayer1's D-Bus path; "": no phone with a player
	Status        string // MediaPlayer1.Status: playing, stopped, paused, forward-seek, reverse-seek, error
	Position      uint32 // ms
	Track         phoneTrack
	Codec         string // the A2DP transport's: SBC, AAC, ...; "": none
}

// phoneSource is BlueZ, or a fake of it in tests.
type phoneSource interface {
	Read() (phoneState, error)
	// Call calls a MediaPlayer1 method (Play, Pause, Next, ...) on player.
	Call(player, method string) error
}

// phoneWatcher is optional for a phoneSource: poke is called whenever BlueZ signals a change.
type phoneWatcher interface {
	Watch(poke func())
}

const (
	phoneWindowTracks  = 4                // the playing list the car sees when the phone has no track numbers
	phoneUnknownLength = 10 * 60 * 1000   // ms past the position: the track length when the phone has none
	phoneStaleAfter    = 10 * time.Second // a cache this old with BlueZ failing: no phone
)

// streamingPath says whether the car takes audio right now (Y or N), from g_ipod_audio.
var streamingPath = "/sys/module/g_ipod_audio/parameters/streaming"

// carStreaming returns Y, N, or ? when g_ipod_audio isn't loaded.
func carStreaming() string {
	b, err := ioutil.ReadFile(streamingPath)
	if err != nil {
		return "?"
	}
	if s := strings.TrimSpace(string(b)); s != "" {
		return s
	}
	return "?"
}

// phoneCmd is a command for the phone, run by the worker; label names it in the log.
type phoneCmd struct {
	cmd   extremote.PlayControlCmd
	label string
}

type phoneReread struct {
	label string
	after time.Duration
}

// BluezPlayer is a Player backed by the phone. The car's requests are answered from a cache
// only, never by a D-Bus call, so a slow bluetoothd can't hold up an iAP reply: a goroutine
// refreshes the cache on BlueZ's signals, every refreshEvery, and after each command.
type BluezPlayer struct {
	src       phoneSource
	now       func() time.Time
	text      textOut
	streaming func() string // carStreaming
	// toggleGuard: a PlayControl toggle while the car takes no audio may pause the phone but
	// never starts it (the car toggles when it leaves the iPod source)
	toggleGuard bool

	refreshEvery time.Duration
	rereads      []time.Duration // after a command

	mu      sync.Mutex
	st      phoneState
	readAt  time.Time // st is from then
	lastOK  time.Time // last successful read
	readSeq uint64    // reads started
	applied uint64    // the latest read applied: older ones finishing later are dropped
	key     string    // the current track, to see it change
	window  int32     // the current index without track numbers: 1 or 2
	state   extremote.PlayerState
	n       notifier
	// after a Play or Pause: the status it should give, until holdUntil (see holdStatus)
	hold      string
	holdUntil time.Time
	holdFor   time.Duration

	cmds    chan phoneCmd
	pokes   chan struct{}
	reReads chan phoneReread
	busy    sync.WaitGroup // queued commands not yet run
	done    chan struct{}
	wg      sync.WaitGroup
	closing sync.Once
}

var _ Player = &BluezPlayer{}
var _ extremote.DeviceTrackInfo = &BluezPlayer{}

func newBluezPlayer(src phoneSource, now func() time.Time, text textOut, toggleGuard bool) *BluezPlayer {
	return &BluezPlayer{
		src:          src,
		now:          now,
		text:         text,
		streaming:    carStreaming,
		toggleGuard:  toggleGuard,
		refreshEvery: 2 * time.Second,
		holdFor:      2500 * time.Millisecond,
		rereads:      []time.Duration{300 * time.Millisecond, 1500 * time.Millisecond},
		window:       2, // the first track makes it 1
		state:        extremote.PlayerStatePaused,
		cmds:         make(chan phoneCmd, 16),
		pokes:        make(chan struct{}, 1),
		reReads:      make(chan phoneReread, 16),
		done:         make(chan struct{}),
	}
}

// start reads the phone once and starts the worker.
func (p *BluezPlayer) start() {
	if _, err := p.refresh(); err != nil {
		log.Warnf("phone: BlueZ: %v (retrying every %v)", err, p.refreshEvery)
	}
	if w, ok := p.src.(phoneWatcher); ok {
		w.Watch(p.Poke)
	}
	p.wg.Add(1)
	go p.run()
}

// Close stops the worker and closes the source.
func (p *BluezPlayer) Close() error {
	var err error
	p.closing.Do(func() {
		close(p.done)
		p.wg.Wait()
		if c, ok := p.src.(io.Closer); ok {
			err = c.Close()
		}
	})
	return err
}

// Poke asks for a refresh soon (BlueZ signalled a change).
func (p *BluezPlayer) Poke() {
	select {
	case p.pokes <- struct{}{}:
	default:
	}
}

func (p *BluezPlayer) run() {
	defer p.wg.Done()
	t := time.NewTicker(p.refreshEvery)
	defer t.Stop()
	for {
		select {
		case <-p.done:
			return
		case c := <-p.cmds:
			p.execute(c)
			p.busy.Done()
		case r := <-p.reReads:
			st, err := p.refresh()
			if err != nil {
				log.Warnf("phone: after %s +%v: BlueZ: %v", r.label, r.after, err)
			} else {
				told, _ := p.snapshot()
				log.Infof("phone: after %s +%v: phone %s, car told %s, car streaming %s",
					r.label, r.after, describeStatus(st), describeStatus(told), p.streaming())
			}
		case <-p.pokes:
			p.refresh()
		case <-t.C:
			p.refresh()
		}
	}
}

// Wait waits for the queued commands, then the last re-read after them, then reads once more.
func (p *BluezPlayer) Wait() {
	p.busy.Wait()
	if n := len(p.rereads); n > 0 {
		time.Sleep(p.rereads[n-1])
	}
	p.refresh()
}

// refresh reads the phone and updates the cache. On an error the cache stays, until it is
// phoneStaleAfter old: then the phone is gone.
func (p *BluezPlayer) refresh() (phoneState, error) {
	p.mu.Lock()
	p.readSeq++
	seq := p.readSeq
	p.mu.Unlock()

	st, err := p.src.Read()

	p.mu.Lock()
	defer p.mu.Unlock()
	at := p.now()
	if err != nil {
		if p.st.Player != "" && at.Sub(p.lastOK) >= phoneStaleAfter {
			log.Warnf("phone: BlueZ failing for %v: %v; no phone until it answers", at.Sub(p.lastOK).Round(time.Second), err)
			p.apply(phoneState{}, at)
		}
		return p.st, err
	}
	p.lastOK = at
	if seq < p.applied {
		return st, nil
	}
	p.applied = seq
	p.apply(st, at)
	return st, nil
}

// key identifies the track: title, artist and the phone's numbers. Not the album or the
// duration: the iPhone sends those after the title and artist, up to a second later (the
// duration at first sometimes the previous track's), and each would count as a track change,
// the car's index flipping 2, 3, 2 within a second.
func (t phoneTrack) key() string {
	return fmt.Sprintf("%s|%s|%d|%d", t.Title, t.Artist, t.Number, t.Count)
}

// apply puts st in the cache and marks what changed for the notifications. Under mu.
func (p *BluezPlayer) apply(st phoneState, at time.Time) {
	prev := p.st
	p.st = st
	p.readAt = at
	if st.Player != prev.Player {
		if st.Player == "" {
			log.Infof("phone: no media player (was %s on %s)", prev.Player, prev.Name)
		} else {
			log.Infof("phone: %s (%s), player %s, codec %s", st.Name, st.Address, st.Player, orNone(st.Codec))
		}
	} else if st.Player != "" && st.Codec != prev.Codec {
		log.Infof("phone: codec %s", orNone(st.Codec))
	}
	if st.Player != "" {
		if key := st.Track.key(); key != p.key {
			p.key = key
			p.window = 3 - p.window
			p.n.mark(extremote.PlayStatusChangeTrackIndex, extremote.PlayStatusChangeTrackTimeMs,
				extremote.PlayStatusChangeTrackTimeSec)
			index, num := p.index()
			log.Infof("phone: track %q / %q / %q, %s, the car sees %d of %d",
				st.Track.Title, st.Track.Artist, st.Track.Album, describeTrackNumber(st.Track), index+1, num)
		} else if st.Player == prev.Player && (st.Track.Album != prev.Track.Album || st.Track.Duration != prev.Track.Duration) {
			// the rest of the same track arrived: the same index again, so the car reads the
			// names and the length once more
			p.n.mark(extremote.PlayStatusChangeTrackIndex, extremote.PlayStatusChangeTrackTimeMs,
				extremote.PlayStatusChangeTrackTimeSec)
			log.Infof("phone: track %q / %q / %q, %s (details of the same track)",
				st.Track.Title, st.Track.Artist, st.Track.Album, describeTrackNumber(st.Track))
		}
	}
	p.holdStatus(&st, at)
	p.st = st
	if state := playerState(st.Status); state != p.state {
		p.state = state
		p.n.markState(state)
	}
}

// holdStatus keeps the status a Play or Pause should give for holdFor, against reads that
// still say the opposite: the iPhone reports the change up to about a second late, and the car
// would see playing, paused, playing. A read that agrees ends the hold; one that still
// disagrees when it ends wins (and is logged: the phone didn't do it). Under mu.
func (p *BluezPlayer) holdStatus(st *phoneState, at time.Time) {
	if p.hold == "" {
		return
	}
	switch {
	case st.Player == "" || playerState(st.Status) == playerState(p.hold):
		p.hold = ""
	case at.Before(p.holdUntil):
		st.Status = p.hold
	default:
		log.Infof("phone: still %s %v after the car's command for %s", orNone(st.Status), p.holdFor, p.hold)
		p.hold = ""
	}
}

// playerState maps MediaPlayer1.Status to what the car is told. Stopped is told as paused:
// the car's iPod screen stays sane, and it pulls audio again once the phone plays.
func playerState(status string) extremote.PlayerState {
	switch status {
	case "playing", "forward-seek", "reverse-seek":
		return extremote.PlayerStatePlaying
	default:
		return extremote.PlayerStatePaused
	}
}

// position is the track length, the position extrapolated to now and the state. Under mu.
func (p *BluezPlayer) position() (length, pos uint32, state extremote.PlayerState) {
	state = playerState(p.st.Status)
	pos = p.st.Position
	if state == extremote.PlayerStatePlaying {
		if el := p.now().Sub(p.readAt); el > 0 {
			pos += uint32(el / time.Millisecond)
		}
	}
	if d := p.st.Track.Duration; d > 0 {
		if pos >= d {
			pos = d - 1
		}
		return d, pos, state
	}
	// unknown length: never let the car think the track ended
	return pos + phoneUnknownLength, pos, state
}

// index is the current track's index and the playing list's length: the phone's own when it
// has them, otherwise a list of 4 with the current track at index 1 or 2, changing on every
// track change: never at an end (next and previous stay valid), and a car that keeps titles
// by index still asks again. Under mu.
func (p *BluezPlayer) index() (int32, uint32) {
	t := p.st.Track
	if t.Count >= 2 && t.Number >= 1 && t.Number <= t.Count {
		return int32(t.Number - 1), t.Count
	}
	return p.window, phoneWindowTracks
}

// HasPlayer: a connected phone has a media player.
func (p *BluezPlayer) HasPlayer() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st.Player != ""
}

// snapshot returns the cache, and when it was read.
func (p *BluezPlayer) snapshot() (phoneState, time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st, p.readAt
}

// markAll: the phone takes over from the stub, everything is news to the car.
func (p *BluezPlayer) markAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n.markAll(p.state)
}

func (p *BluezPlayer) PlaybackStatus() (trackLength, trackPos uint32, state extremote.PlayerState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.position()
}

func (p *BluezPlayer) CurrentTrack() (index int32, numTracks uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.index()
}

// TrackInfo returns the current track whatever the index: the car may ask for one it saw
// before the track changed. No title: the phone's name.
func (p *BluezPlayer) TrackInfo(index int32) (title, artist, album string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	t := p.st.Track
	title = t.Title
	if title == "" {
		title = p.st.Name
	}
	if title == "" {
		title = "Bluetooth"
	}
	return p.text.apply(title), p.text.apply(t.Artist), p.text.apply(t.Album)
}

func (p *BluezPlayer) PlayControl(cmd extremote.PlayControlCmd) {
	p.enqueue(cmd, "PlayControl "+playControlName(cmd))
}

// SetCurrentTrack: a later index is Next, an earlier one Previous, the same one (or -1)
// Play if play is set.
func (p *BluezPlayer) SetCurrentTrack(index int32, play bool) {
	cur, _ := p.CurrentTrack()
	label := fmt.Sprintf("SetCurrentTrack %d (current %d, play %v)", index, cur, play)
	switch {
	case index >= 0 && index > cur:
		p.enqueue(extremote.PlayControlNextTrack, label)
	case index >= 0 && index < cur:
		p.enqueue(extremote.PlayControlPrevTrack, label)
	case play:
		p.enqueue(extremote.PlayControlPlay, label)
	}
}

func (p *BluezPlayer) SetPlayStatusNotifications(enabled extremote.PlayStatusChanges) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n.enable(enabled)
}

func (p *BluezPlayer) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n.reset()
}

func (p *BluezPlayer) Notifications() []*extremote.PlayStatusChangeNotification {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, pos, state := p.position()
	index, _ := p.index()
	return p.n.due(state, index, pos)
}

// enqueue hands cmd to the worker without waiting: the iAP reply (an ACK) goes out at once.
func (p *BluezPlayer) enqueue(cmd extremote.PlayControlCmd, label string) {
	p.busy.Add(1)
	select {
	case p.cmds <- phoneCmd{cmd: cmd, label: label}:
	default:
		p.busy.Done()
		log.Warnf("phone: %s dropped, %d commands already waiting", label, cap(p.cmds))
	}
}

// phoneAction decides what a car command does to the phone, from the phone's status (read
// just before) and whether the car takes audio (streaming: Y, N or ?). An empty method:
// nothing; why says why either way.
func phoneAction(cmd extremote.PlayControlCmd, status, streaming string, toggleGuard bool) (method, why string) {
	switch cmd {
	case extremote.PlayControlToggle:
		if playerState(status) == extremote.PlayerStatePlaying {
			return "Pause", "toggle, the phone plays"
		}
		if toggleGuard && streaming == "N" {
			return "", "toggle while the car takes no audio never starts the phone"
		}
		return "Play", "toggle, the phone doesn't play"
	case extremote.PlayControlPlay:
		return "Play", "play"
	case extremote.PlayControlEndFFRew:
		return "Play", "end of fast forward / rewind"
	case extremote.PlayControlPause, extremote.PlayControlStop:
		return "Pause", playControlName(cmd)
	case extremote.PlayControlNextTrack, extremote.PlayControlNext, extremote.PlayControlNextChapter:
		return "Next", playControlName(cmd)
	case extremote.PlayControlPrevTrack, extremote.PlayControlPrev, extremote.PlayControlPrevChapter:
		return "Previous", playControlName(cmd)
	case extremote.PlayControlStartFF:
		return "FastForward", playControlName(cmd)
	case extremote.PlayControlStartRew:
		return "Rewind", playControlName(cmd)
	}
	return "", "ignored"
}

// execute runs a command: the phone is read again first, so the decision never rests on a
// stale cache (a toggle never toggles blind), only on a Play or Pause still held. Logged with the car's streaming state and the
// phone's status before and after, then again at each re-read.
func (p *BluezPlayer) execute(c phoneCmd) {
	streaming := p.streaming()
	if _, err := p.refresh(); err != nil {
		log.Warnf("phone: %s: BlueZ: %v; deciding from the cache", c.label, err)
	}
	// fresh, unless a Play or Pause the phone hasn't reported yet is held (a second press
	// right after the first must not repeat it)
	before, _ := p.snapshot()
	method, why := phoneAction(c.cmd, before.Status, streaming, p.toggleGuard)
	switch {
	case method == "":
		log.Infof("phone: %s: car streaming %s, phone %s, action none (%s)", c.label, streaming, describeStatus(before), why)
		return
	case before.Player == "":
		log.Infof("phone: %s: car streaming %s, no phone player, action none", c.label, streaming)
		return
	}
	if err := p.src.Call(before.Player, method); err != nil {
		log.Warnf("phone: %s: car streaming %s, phone %s, action %s (%s) failed: %v",
			c.label, streaming, describeStatus(before), method, why, err)
	} else {
		after := p.expect(method)
		log.Infof("phone: %s: car streaming %s, phone %s -> %s, action %s (%s)",
			c.label, streaming, describeStatus(before), describeStatus(after), method, why)
	}
	for _, d := range p.rereads {
		d, label := d, c.label
		time.AfterFunc(d, func() {
			select {
			case p.reReads <- phoneReread{label: label, after: d}:
			default:
			}
		})
	}
}

// expect updates the cache with what a successful call should do, until the next read says.
func (p *BluezPlayer) expect(method string) phoneState {
	p.mu.Lock()
	defer p.mu.Unlock()
	status := ""
	switch method {
	case "Play":
		status = "playing"
	case "Pause":
		status = "paused"
	default:
		return p.st
	}
	st := p.st
	_, st.Position, _ = p.position()
	st.Status = status
	now := p.now()
	p.hold = ""
	p.apply(st, now)
	p.hold, p.holdUntil = status, now.Add(p.holdFor)
	return p.st
}

func describeStatus(st phoneState) string {
	if st.Player == "" {
		return "none"
	}
	return orNone(st.Status)
}

func describeTrackNumber(t phoneTrack) string {
	s := "no track number"
	if t.Number > 0 || t.Count > 0 {
		s = fmt.Sprintf("track %d of %d", t.Number, t.Count)
	}
	if t.Duration > 0 {
		return s + ", " + msTime(t.Duration)
	}
	return s + ", no length"
}

func msTime(ms uint32) string {
	s := ms / 1000
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

var playControlNames = map[extremote.PlayControlCmd]string{
	extremote.PlayControlToggle:      "toggle",
	extremote.PlayControlStop:        "stop",
	extremote.PlayControlNextTrack:   "next track",
	extremote.PlayControlPrevTrack:   "previous track",
	extremote.PlayControlStartFF:     "start fast forward",
	extremote.PlayControlStartRew:    "start rewind",
	extremote.PlayControlEndFFRew:    "end fast forward / rewind",
	extremote.PlayControlNext:        "next",
	extremote.PlayControlPrev:        "previous",
	extremote.PlayControlPlay:        "play",
	extremote.PlayControlPause:       "pause",
	extremote.PlayControlNextChapter: "next chapter",
	extremote.PlayControlPrevChapter: "previous chapter",
	extremote.PlayControlResumeIPod:  "resume iPod",
}

func playControlName(cmd extremote.PlayControlCmd) string {
	if s, ok := playControlNames[cmd]; ok {
		return fmt.Sprintf("%s (%d)", s, cmd)
	}
	return fmt.Sprintf("%d", cmd)
}

// AutoPlayer answers the car from the phone while a connected phone has a media player, and
// from the Level 2 stub (DevExtRemote, playing) otherwise. On a switch the new side reports
// everything as changed, so the car refreshes the title, the state and the time.
type AutoPlayer struct {
	stub  *DevExtRemote
	phone *BluezPlayer // nil: the stub only (--player=stub, or no D-Bus)
	text  textOut
	demo  *[3]string  // --demo-track: the stub's title, artist and album
	art   *artworkSet // --artwork: the picture for every track; nil: none

	mu      sync.Mutex
	onPhone bool
}

var _ Player = &AutoPlayer{}
var _ extremote.DeviceTrackInfo = &AutoPlayer{}
var _ extremote.DeviceArtwork = &AutoPlayer{}

func newAutoPlayer(stub *DevExtRemote, phone *BluezPlayer, text textOut, demo *[3]string) *AutoPlayer {
	return &AutoPlayer{stub: stub, phone: phone, text: text, demo: demo}
}

// active returns the side that answers now, and the other one (nil without a phone).
func (a *AutoPlayer) active() (active, inactive Player) {
	if a.phone == nil {
		return a.stub, nil
	}
	usePhone := a.phone.HasPlayer()
	a.mu.Lock()
	switched := usePhone != a.onPhone
	a.onPhone = usePhone
	a.mu.Unlock()
	if usePhone {
		if switched {
			st, _ := a.phone.snapshot()
			log.Infof("player: the phone answers the car (%s, %s)", st.Name, orNone(st.Status))
			a.phone.markAll()
		}
		return a.phone, a.stub
	}
	if switched {
		log.Infof("player: no phone player, the stub answers the car (playing)")
		a.stub.takeOver()
	}
	return a.stub, a.phone
}

// OnPhone: the phone answers the car right now.
func (a *AutoPlayer) OnPhone() bool {
	act, _ := a.active()
	return act != Player(a.stub)
}

func (a *AutoPlayer) PlaybackStatus() (trackLength, trackPos uint32, state extremote.PlayerState) {
	act, _ := a.active()
	return act.PlaybackStatus()
}

func (a *AutoPlayer) CurrentTrack() (index int32, numTracks uint32) {
	act, _ := a.active()
	return act.CurrentTrack()
}

func (a *AutoPlayer) PlayControl(cmd extremote.PlayControlCmd) {
	act, _ := a.active()
	if act != Player(a.stub) {
		act.PlayControl(cmd)
		return
	}
	// the phone's side logs in its worker; the stub's PlayControls get the same line
	_, _, before := a.stub.PlaybackStatus()
	a.stub.PlayControl(cmd)
	_, _, after := a.stub.PlaybackStatus()
	log.Infof("stub: PlayControl %s: car streaming %s, no phone player, stub %s -> %s",
		playControlName(cmd), carStreaming(), stateName(before), stateName(after))
}

func (a *AutoPlayer) SetCurrentTrack(index int32, play bool) {
	act, _ := a.active()
	act.SetCurrentTrack(index, play)
}

func (a *AutoPlayer) SetPlayStatusNotifications(enabled extremote.PlayStatusChanges) {
	a.stub.SetPlayStatusNotifications(enabled)
	if a.phone != nil {
		a.phone.SetPlayStatusNotifications(enabled)
	}
}

func (a *AutoPlayer) Reset() {
	a.stub.Reset()
	if a.phone != nil {
		a.phone.Reset()
	}
}

// Notifications: the active side's; the other side's are dropped.
func (a *AutoPlayer) Notifications() []*extremote.PlayStatusChangeNotification {
	act, inactive := a.active()
	if inactive != nil {
		inactive.Notifications()
	}
	return act.Notifications()
}

func (a *AutoPlayer) TrackInfo(index int32) (title, artist, album string) {
	act, _ := a.active()
	if act != Player(a.stub) {
		return a.phone.TrackInfo(index)
	}
	if a.demo != nil {
		return a.text.apply(a.demo[0]), a.text.apply(a.demo[1]), a.text.apply(a.demo[2])
	}
	return "title", "artist", "album"
}

// ArtworkFormats: --artwork's formats, none without it.
func (a *AutoPlayer) ArtworkFormats() []extremote.ArtworkFormat {
	if a.art == nil {
		return nil
	}
	return a.art.formats
}

// TrackArtwork: --artwork's picture, the same for every track, phone or stub.
func (a *AutoPlayer) TrackArtwork(index int32, formatID uint16) *extremote.ArtworkImage {
	if a.art == nil {
		return nil
	}
	return a.art.images[formatID]
}

func (a *AutoPlayer) Close() error {
	if a.phone != nil {
		return a.phone.Close()
	}
	return nil
}

func stateName(state extremote.PlayerState) string {
	switch state {
	case extremote.PlayerStatePlaying:
		return "playing"
	case extremote.PlayerStatePaused:
		return "paused"
	case extremote.PlayerStateStopped:
		return "stopped"
	}
	return fmt.Sprintf("state %d", state)
}

// --player values
const (
	playerAuto = "auto"
	playerStub = "stub"
)

// playerOptions are the global flags behind the Player serve and bt make.
type playerOptions struct {
	mode        string // playerAuto or playerStub
	text        textOut
	demo        *[3]string
	toggleGuard bool
	art         *artworkSet
}

// playerOpts: set in app.Before
var playerOpts = playerOptions{mode: playerAuto, text: textOut{mode: textAsIs, max: 200}, toggleGuard: true}

func parsePlayerOptions(mode, text string, textMax int, demo string, toggleGuard bool) (playerOptions, error) {
	if mode != playerAuto && mode != playerStub {
		return playerOptions{}, fmt.Errorf("--player=%s: expected %s or %s", mode, playerAuto, playerStub)
	}
	t, err := newTextOut(text, textMax)
	if err != nil {
		return playerOptions{}, err
	}
	o := playerOptions{mode: mode, text: t, toggleGuard: toggleGuard}
	if demo != "" {
		parts := strings.SplitN(demo, "|", 3)
		var names [3]string
		copy(names[:], parts)
		o.demo = &names
	}
	return o, nil
}

func (o playerOptions) String() string {
	s := o.mode
	if o.mode == playerAuto {
		s += " (the phone over BlueZ while one has a media player, else the stub)"
		if o.toggleGuard {
			s += ", toggle guard on"
		} else {
			s += ", toggle guard off"
		}
	} else {
		s += " (Level 2, nothing from the phone)"
	}
	s += fmt.Sprintf(", text %s max %d", o.text.mode, o.text.max)
	if o.demo != nil {
		s += fmt.Sprintf(", demo track %q", *o.demo)
	}
	s += ", artwork " + o.art.String()
	if o.art != nil {
		s += fmt.Sprintf(", %d bytes a packet", extremote.ArtworkChunk)
	}
	return s
}

// newPlayer: an AutoPlayer, with the phone side running for --player=auto.
func (o playerOptions) newPlayer() *AutoPlayer {
	stub := NewDevExtRemote(time.Now)
	var phone *BluezPlayer
	if o.mode == playerAuto {
		phone = newBluezPlayer(newDBusPhones(), time.Now, o.text, o.toggleGuard)
		phone.start()
	}
	a := newAutoPlayer(stub, phone, o.text, o.demo)
	a.art = o.art
	return a
}
