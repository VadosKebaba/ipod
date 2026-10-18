# Level 3 handoff (AVRCP bridge): implemented, not yet on the Pi

Branch `level3-wip` in both repos (VadosKebaba/ipod and VadosKebaba/ipod-gadget). `pi5-support`
is untouched (ipod 7b8762c, ipod-gadget 3f55138), which is what runs in the car. Implemented
2026-10-18 in a cloud session with no Pi, car or iPhone: everything below is tested against
fakes and a mocked BlueZ only.

## Safe state / rollback

Both repos carry the tag **`q60-level2-good`** (Level 2 as verified in the car on 2026-10-18).
On the Pi:

```bash
cd ~/ipod && git fetch --tags && git checkout q60-level2-good && cd ~/ipod-gadget && git fetch --tags && git checkout q60-level2-good && sudo bash install-rpi.sh ~/ipod && sudo bash extras/pi-audio/install.sh && sudo reboot
```

No-code rollback with the new client: `--player=stub` in `IPOD_GLOBAL_ARGS`
(`/etc/default/ipod-client`) gives the Level 2 answers.

## What's on the branch

- `cmd/ipod/phone.go`
  - `BluezPlayer`: a `Player` + `extremote.DeviceTrackInfo` + `io.Closer` over a
    `phoneSource` (`Read`, `Call`; optional `Watch`). The car's requests are answered from a
    cache only; one worker goroutine refreshes it on BlueZ signals (`Poke`), every 2 s, and
    300 ms / 1.5 s after each command, and runs the commands from a queue (the iAP ACK goes
    out at once). A read error keeps the cache for 10 s, then there is no phone.
  - Status: playing / forward-seek / reverse-seek → Playing, everything else (stopped too)
    → Paused. Position extrapolated while playing, clamped below Duration; unknown Duration
    → length = position + 10 min.
  - Commands (`phoneAction`): toggle → Pause if the phone plays (fresh read), else Play,
    **except** while the car takes no audio (`/sys/module/g_ipod_audio/parameters/streaming`
    = N): then nothing (the source-leave rule, `--toggle-guard`, default on; `?` = no
    g_ipod_audio, no guard). 10, 7 → Play; 11, 2 → Pause; 3/8/12 → Next; 4/9/13 → Previous;
    5 → FastForward; 6 → Rewind; 14 ignored. After a successful Play/Pause the cache is set
    to the expected status until the next read. SetCurrentTrack: later index → Next,
    earlier → Previous, same (or -1) → Play if asked.
  - Log per command: `phone: PlayControl toggle (1): car streaming N, phone playing ->
    paused, action Pause (toggle, the phone plays)`, then `phone: after PlayControl toggle (1)
    +300ms: phone paused, car streaming N` and the same at +1.5s. Refused toggles:
    `action none (toggle while the car takes no audio never starts the phone)`.
  - Track index: the phone's `TrackNumber - 1` of `NumberOfTracks` when Count ≥ 2 and
    1 ≤ Number ≤ Count, else a list of 4 with the current track at index 1 or 2,
    alternating on every track change (key: title, artist and the phone's numbers; an
    album or duration arriving later re-reports the same index). `TrackInfo(any index)` = the current track through `textOut`; no title → the
    phone's name.
  - `AutoPlayer`: the stub (`DevExtRemote`) and the phone (nil for `--player=stub`). The phone
    answers while it has a player. On a switch: logged (`player: ...`), `markAll` on the new
    side; phone → stub sets the stub playing (`DevExtRemote.takeOver`). Inactive side's
    notifications drained. Notifications enable / Reset go to both. Stub track names:
    `--demo-track` or the Level 2 placeholders. Stub PlayControls are logged too.
  - `playerOptions` / `parsePlayerOptions`: the global flags.
- `cmd/ipod/dbus_phone.go`: `dbusPhones` over godbus, connecting lazily (and again after
  the bus drops). `Read` = GetManagedObjects (3 s) → `pickPhone`; bluetoothd not running
  (ServiceUnknown) = no phone, not an error. `Call` = MediaPlayer1.<method> (5 s). `Watch`:
  PropertiesChanged and ObjectManager signals from org.bluez, NameOwnerChanged for org.bluez.
  `pickPhone`: MediaPlayer1s of connected devices, preferring MediaControl1.Player, then
  playing, then by path; name from Alias (Name, Address); codec from the device's
  MediaTransport1; BlueZ's 0xFFFFFFFF (unknown) Position / Duration → 0.
- `cmd/ipod/bt.go`: `ipod bt now|next|prev|play|pause|toggle|watch`. Commands are built as an
  iAP PlayControl and run through `extremote.HandleExtRemote` (the car's code path), then
  `Wait` (queue done + 1.5 s + one read), printing before and after.
- `cmd/ipod/main.go`: global flags `--player=auto|stub` (default auto), `--text=asis|translit`
  (asis), `--text-max` (200), `--demo-track "Title|Artist|Album"`, `--toggle-guard` (on),
  validated in `app.Before`. `serve` sets `newPlayer` to the AutoPlayer; `processFrames`
  closes an `io.Closer` Player. The package default `newPlayer` stays the stub (tests never
  touch a bus). Also removed the unreachable `return nil` in `send` that failed `go vet`.
- `packet.go`: the small format only up to 255 bytes (`smallPacketMaxLen`). A 256-byte payload
  used to get length byte 0x00, the large-packet marker, and came out corrupt; tests for 255,
  256 and a 250–260 round trip (they fail on the old code).
- Tests: `phone_test.go` (fake phoneSource: action table, fresh-read toggle, guard, command
  mapping, SetCurrentTrack, index rules and window alternation, position with a fake clock,
  stale cache, text modes and cap, stub ↔ phone switch and its notifications, demo track,
  flags; end to end through `processFrames`: car next → `Call("Next")`, the phone's names in
  GetIndexedPlayingTrackTitle / ArtistName, the index moving), `dbus_phone_test.go`
  (`pickPhone` on hand-built GetManagedObjects maps), `text_test.go` (translit, truncate).
  `go vet`, `go test -race ./...` pass; builds for linux/arm64 with CGO_ENABLED=0.
- `cmd/ipod/scripts/bt-dbusmock-test.py`: `ipod bt` against python-dbusmock's bluez5 with a
  hand-made iPhone player, on a private system bus (16 checks: now / translit / next / toggle
  both ways / pause / play / prev / watch from the signal in 0.02 s / disconnected / no
  bluetoothd).
- ipod-gadget `level3-wip`: `aucd-bt now|next|prev|play|pause|toggle|watch` exec
  `/usr/local/bin/ipod $IPOD_GLOBAL_ARGS bt <cmd>` (args from `/etc/default/ipod-client`);
  `install-rpi.sh` documents the new flags in a fresh `/etc/default/ipod-client`;
  `doc/q60-notes.md` (the car's controls, open questions); `extras/pi-audio/README.md`
  Level 3 section (install, desk test, car checklist, rollback).

## Not done / to verify on the Pi

- Everything with real hardware: BlueZ's real property types, the iPhone's AVRCP track
  numbers and how quickly it reports changes, the desk test and the car checklist
  (`ipod-gadget/extras/pi-audio/README.md`, *Level 3*).
- The QEMU busybox / fakecar test (a ~180-byte Cyrillic `--demo-track` title through the real
  gadget as a multi-report iPod → car frame) was not rerun: the rig isn't available in the
  cloud session.
- q60-pi-dash `docs/Q60_Pi_Dash_build_guide.md` roadmap (Level 3 status): that repo wasn't
  reachable from the session; update it by hand.

## First car run (2026-10-18)

Next/previous (3/4), the screen's play/pause (toggle 1), names and timer all work. Every
toggle came with `car streaming Y`, so the guard never acted (running with
`--toggle-guard=false` now; it makes no difference in this car). Leaving for FM with the
phone paused: the car sent nothing. Returning to the iPod source: the car sent a toggle and
the phone started, as a real iPod would; Vadym keeps that. The car enables the 1-byte
PlayStatusChangeNotifications (track index, position) and sends GetArtworkFormats on every
connect (Level 4 is worth a try). Steering-wheel "enter" is not a play/pause button.

## Level 4, step 1: album art, one picture for every track (2026-10-18, not yet in the car)

`--artwork=off|test|FILE` (default off), `--artwork-pixel=565le|565be` (default le).
- `lingo-extremote`: `DeviceArtwork` (optional, like `DeviceTrackInfo`): `ArtworkFormats`,
  `TrackArtwork(index, format)` → `ArtworkImage`. GetArtworkFormats lists them; TrackCaps
  gets bit 2 (has artwork); TrackInfoArtworkCount lists {format, 1}; GetTrackArtworkTimes
  answers one picture at 0 ms; GetTrackArtworkData sends RetTrackArtworkData packets with
  the request's transaction: packet 0 with format, size, inset (0,0)-(w-1,h-1) and row size,
  then index + data; at most 240 data bytes each, whole pixels, so every packet stays in
  the small format (≤ 255). Without a picture: ACK failed, as before. Marshal/Unmarshal for
  RetTrackArtworkData, RetTrackArtworkTimes and the artwork-count info.
- `cmd/ipod/artwork.go`: formats 1 = 128×128 and 2 = 240×240 RGB 565; test pattern (black
  frame, red top left, green top right, blue bottom left, white bottom right) or a PNG/JPEG
  cropped to its centre square and area-averaged. `AutoPlayer` serves it for every track,
  phone or stub. Only packet 0 of a picture is logged, then `artwork: sent N bytes in M
  packets in T`.
- Tests: wire formats, the handler (packets, transaction, sizes, no picture), the pattern's
  pixels in both byte orders, a PNG crop/scale, end to end through processFrames (240×240,
  481 packets, all small).
- Speed: the gadget's HID IN endpoint is polled every 1 ms at full speed, 63 bytes a report:
  about 0.6 s for 128×128, 2 s for 240×240, during which the client answers nothing else.

**First car test (2026-10-18, photo):** the X9U6 shows the art (128×128 picked, colours
and orientation right with `565le`), but as thin diagonal stripes on junk: it keeps only
the **first HID report** of each picture packet (52 of 240 bytes; a simulation of exactly
that matches the photo stripe for stripe), although it reassembles multi-report text
replies. Fix: `artworkChunkFor` sizes packets to one HID input report (52 bytes with the
default and bigout layouts, 631 packets for 128×128; 240 with legacy), set in `app.Before`;
`--artwork-chunk` overrides; `TestArtworkOneReportPerPacket` encodes through the real HID
encoder and checks every packet is one report.

**Second car test (2026-10-18, photo): clean.** With one HID report per packet the X9U6
draws the test pattern exactly (quadrants, frame, colours, orientation). The car side of
album art is done; what is left is a source of real covers.

Log of the first run (old build, 240-byte packets): on every track change the car asks caps
(info 0), artwork count (info 7), GetTrackArtworkTimes, then ~300 ms later
GetTrackArtworkData for format 1 (128×128, 32768 bytes); no retries or errors. Sending took
1.22 s for 137 packets = 685 HID reports, i.e. ~1.8 ms a report (not the 1 ms the endpoint
interval allows). The one-report build sends 631 reports: expect ~1.1–1.3 s, unless the car
also spends time per packet; check `artwork: sent ...` in the next log.

Reported by Vadym after several drives (2026-10-18), for later: one or a few audio
hiccups right after start, then fine; a constant ~0.5 s audio delay (Bluetooth A2DP +
PipeWire + gadget buffering; candidates: PipeWire quantum/headroom in 50-aucd-ipod.conf,
A2DP delay reporting so the iPhone compensates video).

Step 2, the phone's own covers: **not available from the iPhone.** With BlueZ 5.82, obexd
installed and running (user service `obex`), and a fresh connection, the MediaPlayer1 has
no `ObexPort` and the Track no `ImgHandle` (2026-10-18), so the iPhone offers no AVRCP cover
art (it gives artwork to CarPlay only). The same fresh connection did show AVRCP browsing
(`Track.Item` = `.../player1/NowPlaying/item…`, plus `Genre`), so the now-playing queue is
reachable for real track numbers later, and an Apple Music track with `Duration` 0 (handled:
length = position + 10 min). Options left for covers: an online lookup by artist + title
(iTunes Search API, cached on the Pi, needs the phone's hotspot for internet; Vadym is
thinking about it), AllYouCanDash's own files, or a fixed picture (`--artwork=FILE`, works).

## Known risks / open questions

- **The guard may block resume from the car.** The car stops taking audio while it's told
  *paused*, so the toggle from its play button (or on returning to the source) may arrive
  with streaming N and be refused. If so: `--toggle-guard=false`, or a rule from the order the
  logs show (e.g. allow a toggle within a few seconds of the car selecting the source).
- Whether the X9U6 enables PlayStatusChangeNotifications, and whether it keeps titles by
  index (decides whether the window alternation is needed).
- Seen on the Pi (2026-10-18): the iPhone sends title and artist first, album and duration
  up to a second later (the duration at first sometimes the previous track's). The track
  key is title + artist (+ numbers), so those no longer flip the window; the late details
  re-report the same index so the car reads them.
- The iPhone reports a Play/Pause up to ~1 s late (the +300 ms re-read still showed the old
  status). After a successful Play/Pause the car is told the new status for 2.5 s against
  reads that disagree (a second press in that time is decided from it); a read that agrees
  ends the hold early. Re-read lines show both (`phone paused, car told playing`).
- `codec none` when the player first appeared: the A2DP transport probably came later; codec
  changes are logged now (`phone: codec SBC`).
- Debian trixie's PipeWire has no AAC Bluetooth codec: the iPhone uses SBC (fine at Level 2).
- Level 4 (album art): GetArtworkFormats / GetTrackArtworkTimes / GetTrackArtworkData would be
  the requests; the client answers GetArtworkFormats with an empty list today.
