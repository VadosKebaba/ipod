#!/usr/bin/env python3
"""ipod bt against a mocked BlueZ: python-dbusmock's bluez5 template plus a hand-made iPhone
(Device1 connected, MediaControl1, MediaPlayer1 whose methods change its Status / Track and
signal it, an SBC MediaTransport1), on a private system bus. Checks the output and the calls
the mock received.

  go build -o /tmp/ipod ./cmd/ipod
  python3 -m venv --system-site-packages venv && venv/bin/pip install python-dbusmock
  venv/bin/python cmd/ipod/scripts/bt-dbusmock-test.py /tmp/ipod

Needs dbus-daemon, and python3-dbus and python3-gi for the venv's Python."""

import os
import subprocess
import sys
import tempfile
import time

import dbus

IPOD = os.path.abspath(sys.argv[1])
PLAYER = "/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF/player0"
DEVICE = "/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF"
MP = "org.bluez.MediaPlayer1"
MOCK = "org.freedesktop.DBus.Mock"

failures = []


def check(what, ok, detail=""):
    print(("PASS " if ok else "FAIL ") + what + ("" if ok else "\n" + detail))
    if not ok:
        failures.append(what)


tmp = tempfile.mkdtemp()
conf = os.path.join(tmp, "bus.conf")
with open(conf, "w") as f:
    f.write("""<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig><type>system</type><listen>unix:path=%s/bus</listen><auth>EXTERNAL</auth>
<policy context="default"><allow user="*"/><allow own="*"/><allow send_type="method_call"/>
<allow send_type="signal"/><allow send_type="method_return"/><allow send_type="error"/>
<allow receive_type="method_call"/><allow receive_type="signal"/><allow receive_type="method_return"/>
<allow receive_type="error"/></policy></busconfig>
""" % tmp)
daemon = subprocess.Popen(["dbus-daemon", "--config-file", conf, "--nofork", "--print-address"],
                          stdout=subprocess.PIPE, text=True)
address = daemon.stdout.readline().strip()
env = dict(os.environ, DBUS_SYSTEM_BUS_ADDRESS=address)
os.environ["DBUS_SYSTEM_BUS_ADDRESS"] = address


def start_mock():
    m = subprocess.Popen([sys.executable, "-m", "dbusmock", "--system", "--template", "bluez5"], env=env,
                         stdout=subprocess.DEVNULL)
    bus = dbus.SystemBus()
    for _ in range(100):
        if bus.name_has_owner("org.bluez"):
            break
        time.sleep(0.1)
    return m


def setup_phone(bus):
    root = dbus.Interface(bus.get_object("org.bluez", "/"), "org.bluez.Mock")
    root.AddAdapter("hci0", "AUCD")
    root.AddDevice("hci0", "AA:BB:CC:DD:EE:FF", "Vadym's iPhone")
    dev = dbus.Interface(bus.get_object("org.bluez", DEVICE), MOCK)
    dev.UpdateProperties("org.bluez.Device1", {"Connected": True, "Paired": True, "Trusted": True})
    dev.AddProperties("org.bluez.MediaControl1", {"Connected": True, "Player": dbus.ObjectPath(PLAYER)})
    track = dbus.Dictionary({"Title": "Группа крови", "Artist": "Кино", "Album": "Группа крови",
                             "Duration": dbus.UInt32(285000), "TrackNumber": dbus.UInt32(0),
                             "NumberOfTracks": dbus.UInt32(0)}, signature="sv")
    # (Set, not UpdateProperties, for the dict: dbusmock can't wrap one in UpdateProperties)
    nxt = ("self.UpdateProperties('org.bluez.MediaPlayer1', {'Position': dbus.UInt32(0)}); "
           "self.Set('org.bluez.MediaPlayer1', 'Track', "
           "dbus.Dictionary({'Title': 'Кукушка', 'Artist': 'Кино', 'Album': 'Последний герой', "
           "'Duration': dbus.UInt32(400000)}, signature='sv'))")
    root_mock = dbus.Interface(bus.get_object("org.bluez", "/"), MOCK)
    root_mock.AddObject(PLAYER, MP, {
        "Status": "playing", "Position": dbus.UInt32(61000), "Track": track, "Name": "Music",
        "Device": dbus.ObjectPath(DEVICE), "Type": "Audio", "Subtype": "Audio Book",
    }, [
        ("Play", "", "", "self.UpdateProperties('org.bluez.MediaPlayer1', {'Status': 'playing'})"),
        ("Pause", "", "", "self.UpdateProperties('org.bluez.MediaPlayer1', {'Status': 'paused'})"),
        ("Next", "", "", nxt),
        ("Previous", "", "", "self.UpdateProperties('org.bluez.MediaPlayer1', {'Position': dbus.UInt32(0)})"),
        ("FastForward", "", "", ""),
        ("Rewind", "", "", ""),
    ])
    root_mock.AddObject(DEVICE + "/sep1/fd0", "org.bluez.MediaTransport1", {
        "Device": dbus.ObjectPath(DEVICE), "Codec": dbus.Byte(0), "State": "active",
        "UUID": "0000110a-0000-1000-8000-00805f9b34fb",
    }, [])
    return dbus.Interface(bus.get_object("org.bluez", PLAYER), MOCK)


def bt(*args, flags=()):
    r = subprocess.run([IPOD] + list(flags) + ["bt"] + list(args), env=env, capture_output=True, text=True,
                       timeout=20)
    return r.returncode, r.stdout + r.stderr


mock = start_mock()
try:
    bus = dbus.SystemBus()

    code, out = bt("now")
    check("bt now, BlueZ without devices: no phone", code == 0 and "phone:     none" in out
          and "head unit: stub, playing, track 1 of 1" in out, out)

    player = setup_phone(bus)

    code, out = bt("now")
    print(out)
    check("bt now: the phone, codec, track", code == 0
          and "Vadym's iPhone (AA:BB:CC:DD:EE:FF), codec SBC" in out
          and '"Группа крови"' in out and "1:01 / 4:45" in out, out)
    check("bt now: what the head unit sees", 'head unit: phone, playing, track 2 of 4' in out
          and '"Группа крови" / "Кино" / "Группа крови"' in out, out)

    code, out = bt("now", flags=["--text=translit", "--text-max=10"])
    check("bt now --text=translit --text-max=10", 'sent as "Gruppa kro"' in out and 'sent as "Kino"' in out, out)

    player.ClearCalls()
    t0 = time.time()
    code, out = bt("next")
    print(out)
    calls = player.GetMethodCalls("Next")
    check("bt next: MediaPlayer1.Next called once", code == 0 and len(calls) == 1, "%s\n%s" % (calls, out))
    after = out.split("after:")[-1]
    check("bt next: after shows the next track, index moved", '"Кукушка"' in after
          and "track 3 of 4" in after and "action Next" in out, out)
    print("bt next took %.1f s" % (time.time() - t0))

    player.ClearCalls()
    code, out = bt("toggle")
    check("bt toggle on a playing phone: Pause", code == 0 and len(player.GetMethodCalls("Pause")) == 1
          and len(player.GetMethodCalls("Play")) == 0 and "head unit: phone, paused" in out.split("after:")[-1], out)
    player.ClearCalls()
    code, out = bt("toggle")
    # no g_ipod_audio here: streaming "?", so the guard does not apply
    check("bt toggle on a paused phone (no g_ipod_audio): Play", code == 0
          and len(player.GetMethodCalls("Play")) == 1 and "car streaming ?" in out, out)
    player.ClearCalls()
    code, out = bt("pause")
    check("bt pause", len(player.GetMethodCalls("Pause")) == 1, out)
    code, out = bt("play")
    check("bt play", len(player.GetMethodCalls("Play")) == 1, out)
    code, out = bt("prev")
    check("bt prev", len(player.GetMethodCalls("Previous")) == 1, out)

    # watch: a track change from the phone shows up from BlueZ's signal, before the 2 s poll
    w = subprocess.Popen([IPOD, "bt", "watch"], env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                         text=True)
    time.sleep(1.0)
    changed = time.time()
    props = dbus.Interface(bus.get_object("org.bluez", PLAYER), "org.freedesktop.DBus.Properties")
    props.Set(MP, "Track", dbus.Dictionary({"Title": "Звезда по имени Солнце", "Artist": "Кино",
                                                           "Album": "Звезда по имени Солнце",
                                                           "Duration": dbus.UInt32(225000),
                                                           "TrackNumber": dbus.UInt32(7),
                                                           "NumberOfTracks": dbus.UInt32(11)}, signature="sv"))
    seen = None
    deadline = time.time() + 5
    lines = []
    os.set_blocking(w.stdout.fileno(), False)
    while time.time() < deadline and seen is None:
        line = w.stdout.readline()
        if line:
            lines.append(line)
            if "Звезда" in line and "track 7 of 11" in line:
                seen = time.time() - changed
        else:
            time.sleep(0.02)
    w.terminate()
    w.wait(5)
    print("".join(lines))
    check("bt watch: track change seen (phone's numbers: 7 of 11)", seen is not None, "".join(lines))
    if seen is not None:
        print("  seen %.2f s after the change" % seen)
        check("bt watch: from the signal, not the 2 s poll", seen < 1.0, "%.2f s" % seen)

    # the phone disconnects: no player
    dev = dbus.Interface(bus.get_object("org.bluez", DEVICE), MOCK)
    dev.UpdateProperties("org.bluez.Device1", {"Connected": False})
    code, out = bt("now")
    check("bt now, phone disconnected: no phone", "phone:     none" in out and "head unit: stub" in out, out)
    code, out = bt("next")
    check("bt next without a phone: error", code != 0 and "no connected phone" in out, out)
    dev.UpdateProperties("org.bluez.Device1", {"Connected": True})

    # bluetoothd gone
    mock.terminate()
    mock.wait(5)
    code, out = bt("now")
    check("bt now, no bluetoothd: no phone", code == 0 and "phone:     none" in out, out)
finally:
    if mock.poll() is None:
        mock.terminate()
    daemon.terminate()

print("\n%d failure(s)" % len(failures))
sys.exit(1 if failures else 0)
