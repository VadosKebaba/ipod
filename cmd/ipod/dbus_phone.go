package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	bluezName          = "org.bluez"
	bluezDevice        = "org.bluez.Device1"
	bluezMediaControl  = "org.bluez.MediaControl1"
	bluezMediaPlayer   = "org.bluez.MediaPlayer1"
	bluezTransport     = "org.bluez.MediaTransport1"
	dbusObjectManager  = "org.freedesktop.DBus.ObjectManager"
	dbusReadTimeout    = 3 * time.Second
	dbusCallTimeout    = 5 * time.Second
	bluezUnknownUint32 = ^uint32(0) // BlueZ's Position while it doesn't know
)

// A2DP codec ids (MediaTransport1.Codec)
var bluezCodecs = map[byte]string{0x00: "SBC", 0x01: "MP3", 0x02: "AAC", 0x04: "ATRAC", 0xff: "vendor"}

type bluezObjects = map[dbus.ObjectPath]map[string]map[string]dbus.Variant

// dbusPhones is the phoneSource over the system bus. It connects on first use and again
// after the bus connection drops, so it can start before the bus or bluetoothd. The client
// runs as root (ipod-client.service has no User=), which the bus policy lets talk to BlueZ.
type dbusPhones struct {
	mu   sync.Mutex
	conn *dbus.Conn
	poke func()
}

func newDBusPhones() *dbusPhones {
	return &dbusPhones{}
}

func (d *dbusPhones) connection() (*dbus.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conn != nil && d.conn.Connected() {
		return d.conn, nil
	}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	d.conn = conn
	if d.poke != nil {
		d.watch(conn, d.poke)
	}
	return conn, nil
}

func (d *dbusPhones) Read() (phoneState, error) {
	conn, err := d.connection()
	if err != nil {
		return phoneState{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), dbusReadTimeout)
	defer cancel()
	var objs bluezObjects
	err = conn.Object(bluezName, "/").CallWithContext(ctx, dbusObjectManager+".GetManagedObjects", 0).Store(&objs)
	if err != nil {
		var dbusErr dbus.Error
		if errors.As(err, &dbusErr) && dbusErr.Name == "org.freedesktop.DBus.Error.ServiceUnknown" {
			// bluetoothd isn't running: no phone, and not a failure to wait out
			return phoneState{}, nil
		}
		return phoneState{}, err
	}
	return pickPhone(objs), nil
}

func (d *dbusPhones) Call(player, method string) error {
	conn, err := d.connection()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), dbusCallTimeout)
	defer cancel()
	return conn.Object(bluezName, dbus.ObjectPath(player)).CallWithContext(ctx, bluezMediaPlayer+"."+method, 0).Err
}

// Watch calls poke on BlueZ's property and object changes, and when bluetoothd comes or goes.
func (d *dbusPhones) Watch(poke func()) {
	d.mu.Lock()
	d.poke = poke
	conn := d.conn
	d.mu.Unlock()
	if conn != nil && conn.Connected() {
		d.watch(conn, poke)
	} else {
		d.connection()
	}
}

func (d *dbusPhones) watch(conn *dbus.Conn, poke func()) {
	matches := [][]dbus.MatchOption{
		{dbus.WithMatchSender(bluezName), dbus.WithMatchInterface("org.freedesktop.DBus.Properties"),
			dbus.WithMatchMember("PropertiesChanged")},
		{dbus.WithMatchSender(bluezName), dbus.WithMatchInterface(dbusObjectManager)},
		{dbus.WithMatchSender("org.freedesktop.DBus"), dbus.WithMatchMember("NameOwnerChanged"),
			dbus.WithMatchArg(0, bluezName)},
	}
	for _, m := range matches {
		if err := conn.AddMatchSignal(m...); err != nil {
			log.Warnf("phone: D-Bus match: %v (polling only)", err)
		}
	}
	ch := make(chan *dbus.Signal, 32)
	conn.Signal(ch)
	go func() {
		// closed with the connection
		for range ch {
			poke()
		}
	}()
}

func (d *dbusPhones) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conn == nil {
		return nil
	}
	err := d.conn.Close()
	d.conn = nil
	return err
}

// pickPhone finds the phone that plays in BlueZ's objects (GetManagedObjects): the
// MediaPlayer1s whose device is connected, preferring the device's current player
// (MediaControl1.Player), then one that plays, then the first by path.
func pickPhone(objs bluezObjects) phoneState {
	type candidate struct {
		path, device dbus.ObjectPath
		current      bool
		playing      bool
	}
	var cands []candidate
	for path, ifs := range objs {
		mp, ok := ifs[bluezMediaPlayer]
		if !ok {
			continue
		}
		dev := propPath(mp, "Device")
		if dev == "" {
			dev = parentPath(path)
		}
		devIfs, ok := objs[dev]
		if !ok || !propBool(devIfs[bluezDevice], "Connected") {
			continue
		}
		cands = append(cands, candidate{
			path:    path,
			device:  dev,
			current: propPath(devIfs[bluezMediaControl], "Player") == path,
			playing: propString(mp, "Status") == "playing",
		})
	}
	if len(cands) == 0 {
		return phoneState{}
	}
	sort.Slice(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.current != b.current {
			return a.current
		}
		if a.playing != b.playing {
			return a.playing
		}
		return a.path < b.path
	})
	c := cands[0]
	mp := objs[c.path][bluezMediaPlayer]
	dev := objs[c.device][bluezDevice]

	st := phoneState{
		Name:     propString(dev, "Alias"),
		Address:  propString(dev, "Address"),
		Player:   string(c.path),
		Status:   propString(mp, "Status"),
		Position: propU32(mp, "Position"),
	}
	if st.Name == "" {
		st.Name = propString(dev, "Name")
	}
	if st.Name == "" {
		st.Name = st.Address
	}
	if st.Position == bluezUnknownUint32 {
		st.Position = 0
	}
	if v, ok := mp["Track"]; ok {
		if track, ok := v.Value().(map[string]dbus.Variant); ok {
			st.Track = phoneTrack{
				Title:    propString(track, "Title"),
				Artist:   propString(track, "Artist"),
				Album:    propString(track, "Album"),
				Duration: propU32(track, "Duration"),
				Number:   propU32(track, "TrackNumber"),
				Count:    propU32(track, "NumberOfTracks"),
			}
			if st.Track.Duration == bluezUnknownUint32 {
				st.Track.Duration = 0
			}
		}
	}
	// the device's A2DP transport (its path is under the device's, e.g. .../sep1/fd0)
	var transports []dbus.ObjectPath
	for path, ifs := range objs {
		if t, ok := ifs[bluezTransport]; ok {
			if propPath(t, "Device") == c.device || strings.HasPrefix(string(path), string(c.device)+"/") {
				transports = append(transports, path)
			}
		}
	}
	if len(transports) > 0 {
		sort.Slice(transports, func(i, j int) bool { return transports[i] < transports[j] })
		t := objs[transports[0]][bluezTransport]
		if v, ok := t["Codec"]; ok {
			if id, ok := v.Value().(byte); ok {
				st.Codec = bluezCodecs[id]
				if st.Codec == "" {
					st.Codec = fmt.Sprintf("codec 0x%02x", id)
				}
			}
		}
	}
	return st
}

func parentPath(p dbus.ObjectPath) dbus.ObjectPath {
	s := string(p)
	if i := strings.LastIndexByte(s, '/'); i > 0 {
		return dbus.ObjectPath(s[:i])
	}
	return ""
}

func propString(props map[string]dbus.Variant, name string) string {
	if v, ok := props[name]; ok {
		if s, ok := v.Value().(string); ok {
			return s
		}
	}
	return ""
}

func propBool(props map[string]dbus.Variant, name string) bool {
	if v, ok := props[name]; ok {
		if b, ok := v.Value().(bool); ok {
			return b
		}
	}
	return false
}

func propPath(props map[string]dbus.Variant, name string) dbus.ObjectPath {
	if v, ok := props[name]; ok {
		if p, ok := v.Value().(dbus.ObjectPath); ok {
			return p
		}
	}
	return ""
}

// propU32 takes any integer type: BlueZ sends u, a mock may not.
func propU32(props map[string]dbus.Variant, name string) uint32 {
	v, ok := props[name]
	if !ok {
		return 0
	}
	switch n := v.Value().(type) {
	case uint32:
		return n
	case byte:
		return uint32(n)
	case uint16:
		return uint32(n)
	case uint64:
		return uint32(n)
	case int16:
		return uint32(n)
	case int32:
		return uint32(n)
	case int64:
		return uint32(n)
	}
	return 0
}
