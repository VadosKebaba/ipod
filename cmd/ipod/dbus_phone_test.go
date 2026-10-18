package main

import (
	"testing"

	"github.com/godbus/dbus/v5"
)

// bluez builds a GetManagedObjects reply: an adapter, and per device its Device1, its
// MediaControl1 (current player) and a MediaPlayer1, plus an A2DP transport if codec >= 0.
type bluezDev struct {
	addr, alias string
	connected   bool
	current     string // MediaControl1.Player: a player name under the device, or ""
	players     map[string]map[string]dbus.Variant
	codec       int
}

func bluezObjs(devs ...bluezDev) bluezObjects {
	v := dbus.MakeVariant
	objs := bluezObjects{
		"/org/bluez/hci0": {"org.bluez.Adapter1": {"Address": v("D8:3A:DD:00:00:01"), "Alias": v("AUCD")}},
	}
	for _, d := range devs {
		path := dbus.ObjectPath("/org/bluez/hci0/dev_" + d.addr)
		ifs := map[string]map[string]dbus.Variant{
			bluezDevice: {"Address": v(d.addr), "Alias": v(d.alias), "Connected": v(d.connected)},
		}
		if d.current != "" {
			ifs[bluezMediaControl] = map[string]dbus.Variant{"Connected": v(d.connected),
				"Player": v(path + dbus.ObjectPath("/"+d.current))}
		}
		objs[path] = ifs
		for name, props := range d.players {
			if _, ok := props["Device"]; !ok {
				props["Device"] = v(path)
			}
			objs[path+dbus.ObjectPath("/"+name)] = map[string]map[string]dbus.Variant{bluezMediaPlayer: props}
		}
		if d.codec >= 0 {
			objs[path+"/sep1/fd0"] = map[string]map[string]dbus.Variant{
				bluezTransport: {"Device": v(path), "Codec": v(byte(d.codec)), "State": v("active")},
			}
		}
	}
	return objs
}

func mediaPlayer(status string, track map[string]dbus.Variant) map[string]dbus.Variant {
	v := dbus.MakeVariant
	p := map[string]dbus.Variant{"Status": v(status), "Position": v(uint32(61000)), "Name": v("Music")}
	if track != nil {
		p["Track"] = v(track)
	}
	return p
}

func TestPickPhone(t *testing.T) {
	v := dbus.MakeVariant
	track := map[string]dbus.Variant{
		"Title": v("Группа крови"), "Artist": v("Кино"), "Album": v("Группа крови"),
		"Duration": v(uint32(285000)), "TrackNumber": v(uint32(5)), "NumberOfTracks": v(uint32(12)),
	}

	if st := pickPhone(bluezObjs()); st.Player != "" {
		t.Fatalf("no devices: %+v", st)
	}
	if st := pickPhone(bluezObjs(bluezDev{addr: "AA", alias: "iPhone", connected: false, current: "player0",
		players: map[string]map[string]dbus.Variant{"player0": mediaPlayer("playing", track)}, codec: 0})); st.Player != "" {
		t.Fatalf("disconnected device: %+v", st)
	}

	st := pickPhone(bluezObjs(bluezDev{addr: "AA", alias: "Vadym's iPhone", connected: true, current: "player0",
		players: map[string]map[string]dbus.Variant{"player0": mediaPlayer("paused", track)}, codec: 2}))
	want := phoneState{
		Name: "Vadym's iPhone", Address: "AA", Player: "/org/bluez/hci0/dev_AA/player0", Status: "paused",
		Position: 61000, Codec: "AAC",
		Track: phoneTrack{Title: "Группа крови", Artist: "Кино", Album: "Группа крови", Duration: 285000, Number: 5, Count: 12},
	}
	if st != want {
		t.Fatalf("one phone:\n got %+v\nwant %+v", st, want)
	}

	// the device's current player wins over one that plays
	st = pickPhone(bluezObjs(bluezDev{addr: "AA", alias: "iPhone", connected: true, current: "player1", codec: 0,
		players: map[string]map[string]dbus.Variant{
			"player0": mediaPlayer("playing", nil), "player1": mediaPlayer("paused", nil)}}))
	if st.Player != "/org/bluez/hci0/dev_AA/player1" || st.Codec != "SBC" {
		t.Fatalf("current player: %+v", st)
	}
	// no current player: the one that plays, then the first by path
	st = pickPhone(bluezObjs(
		bluezDev{addr: "AA", alias: "iPad", connected: true, codec: -1,
			players: map[string]map[string]dbus.Variant{"player0": mediaPlayer("paused", nil)}},
		bluezDev{addr: "BB", alias: "iPhone", connected: true, codec: 0,
			players: map[string]map[string]dbus.Variant{"player0": mediaPlayer("playing", nil)}}))
	if st.Player != "/org/bluez/hci0/dev_BB/player0" || st.Name != "iPhone" || st.Codec != "SBC" {
		t.Fatalf("the playing one: %+v", st)
	}
	st = pickPhone(bluezObjs(
		bluezDev{addr: "BB", alias: "iPhone", connected: true, codec: -1,
			players: map[string]map[string]dbus.Variant{"player0": mediaPlayer("paused", nil)}},
		bluezDev{addr: "AA", alias: "iPad", connected: true, codec: -1,
			players: map[string]map[string]dbus.Variant{"player0": mediaPlayer("paused", nil)}}))
	if st.Player != "/org/bluez/hci0/dev_AA/player0" || st.Codec != "" {
		t.Fatalf("first by path: %+v", st)
	}

	// what BlueZ says while it doesn't know, other integer types (a mock), no Device
	// property, no alias
	odd := mediaPlayer("playing", map[string]dbus.Variant{"Title": v("x"), "Duration": v(^uint32(0)),
		"TrackNumber": v(int32(3)), "NumberOfTracks": v(uint64(9))})
	odd["Position"] = v(^uint32(0))
	objs := bluezObjs(bluezDev{addr: "CC", connected: true, codec: 0x55,
		players: map[string]map[string]dbus.Variant{"player0": odd}})
	delete(objs["/org/bluez/hci0/dev_CC/player0"][bluezMediaPlayer], "Device")
	delete(objs["/org/bluez/hci0/dev_CC"][bluezDevice], "Alias")
	st = pickPhone(objs)
	want = phoneState{Name: "CC", Address: "CC", Player: "/org/bluez/hci0/dev_CC/player0", Status: "playing",
		Codec: "codec 0x55", Track: phoneTrack{Title: "x", Number: 3, Count: 9}}
	if st != want {
		t.Fatalf("odd values:\n got %+v\nwant %+v", st, want)
	}
}
