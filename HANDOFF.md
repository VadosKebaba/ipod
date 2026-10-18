# Level 3 handoff (AVRCP bridge): work in progress

Branch `level3-wip` (VadosKebaba/ipod), paused 2026-10-18 at the usage limit. `pi5-support`
is untouched at 7b8762c, which is what runs in the car.

## Safe state / rollback

Both repos carry the tag **`q60-level2-good`** (pushed): ipod 7b8762c, ipod-gadget 3f55138,
i.e. Level 2 as verified in the car on 2026-10-18. On the Pi:

```bash
cd ~/ipod && git fetch --tags && git checkout q60-level2-good && cd ~/ipod-gadget && git fetch --tags && git checkout q60-level2-good && sudo bash install-rpi.sh ~/ipod && sudo bash extras/pi-audio/install.sh && sudo reboot
```

Nothing from this branch has been installed on the Pi, so no rollback is needed yet.

## Done (on this branch)

- `cmd/ipod/extremote.go`: the PlayStatusChangeNotification bookkeeping moved out of
  `DevExtRemote` into a reusable `notifier` (`reset`, `enable`, `mark`, `markState`,
  `markAll`, `due`), so a phone-backed player can share it. Behaviour unchanged: the
  existing DevExtRemote and end-to-end notification tests pass.
- `cmd/ipod/text.go`: `textOut{mode, max}` for track names sent to the car: `--text=asis`
  (default) or `translit` (Russian/Ukrainian/Belarusian Cyrillic -> Latin, "Zh"/"ZH" by
  case), and `truncateUTF8` (byte cap without splitting a character). **No tests yet, and
  not wired to any flag yet.**
- `go.mod` / `go.sum`: `github.com/godbus/dbus/v5 v5.1.0` added (marked indirect until it
  is imported). Note: the Pi build then needs internet once to download it.

`go build ./...`, `go vet` and `go test ./...` pass on this branch (WSL, Go 1.24).

## Not done: the design, ready to implement

The plan was agreed with the task spec ("LEVEL 3" message). Pieces, in order:

1. **`cmd/ipod/phone.go`: `BluezPlayer`**, implementing `Player` (+ `extremote.DeviceTrackInfo`,
   `io.Closer`):
   - Behind a small interface so tests can fake BlueZ:
     `phoneSource { Read() (phoneState, error); Call(player, method string) error }`.
     `phoneState{Name, Address, Player (D-Bus path, "" = none), Status, Position (ms),
     Track{Title, Artist, Album, Duration, Number, Count}, Codec}`.
   - **Requests from the car are answered from a cache only, never by a D-Bus call**, so a
     slow bluetoothd can't stall the iAP replies. A goroutine refreshes the cache on BlueZ
     signals (`Poke`), every 2 s, and after commands.
   - Status mapping: playing / forward-seek / reverse-seek -> Playing; paused / stopped /
     error -> Paused (stopped -> Paused keeps the car's iPod screen sane). Position
     extrapolated from the read time while playing, clamped below Duration. Unknown
     Duration -> report position + 10 min (so the car never thinks the track ended).
   - PlayControl goes into a queue (non-blocking, `sync.WaitGroup busy` for the CLI). The
     worker **re-reads the phone first, then decides**: toggle (1) -> `Pause` if the phone is
     playing, else `Play` (never blind); 10 -> Play; 11 and Stop (2) -> Pause; 3/8/12 ->
     `Next`; 4/9/13 -> `Previous`; 5 -> `FastForward`; 6 -> `Rewind`; 7 -> `Play`; 14 ignored.
     After the call: an optimistic local status update, then re-reads after 300 ms and
     1.5 s. Log each one, with the car's streaming state from
     `/sys/module/g_ipod_audio/parameters/streaming` (evidence for how source-leave toggles
     line up with alt 0/1).
   - Track index: the phone's `TrackNumber/NumberOfTracks` when `Count >= 2` and
     `1 <= Number <= Count` (index = Number-1). Otherwise a window: **4 tracks, current
     alternating between index 1 and 2 on every track change** (shown as "2 of 4" / "3 of 4"):
     never at an end, so next/prev stay valid, and the index changes so a car that caches
     titles by index still refreshes. (The spec suggested 3 with the current in the middle; a
     constant index risks stale titles. Revisit after seeing the car's requests in the logs.)
   - Track change (key = Title|Artist|Album|Duration|Number|Count) -> notifier marks
     TrackIndex + time; state change -> `markState`. `TrackInfo(index)` returns the current
     track for any index (robust against the car asking for a stale index), through
     `textOut`; an empty title becomes the phone's name.
   - `SetCurrentTrack(index, play)`: index above the current -> Next, below -> Previous, same
     -> Play if `play`.

2. **`AutoPlayer`** (same file): holds the Level 2 stub (`DevExtRemote`) and the
   `BluezPlayer` (nil without D-Bus). The phone answers while it has a player; otherwise the
   stub (playing). In `Notifications()`, detect the switch, log it, `markAll` on the
   new side (so the car refreshes title, state and timer), and drain the inactive side. On
   phone -> stub, set the stub back to Playing so the car pulls local audio again.
   `SetPlayStatusNotifications` / `Reset` go to both. `TrackInfo` on the stub: placeholders,
   or `--demo-track` strings.

3. **`cmd/ipod/dbus_phone.go`: `dbusPhones`** (`phoneSource` over godbus):
   `ConnectSystemBus`; `Read` = one `GetManagedObjects` (3 s timeout) -> a pure
   `pickPhone(objs)` (unit-testable): `org.bluez.MediaPlayer1` objects whose `Device` is
   `Connected`, preferring `MediaControl1.Player`, then status playing, then by path; the
   name from `Device1.Alias`; codec from `MediaTransport1.Codec` (0 SBC, 2 AAC). `Call` =
   `org.bluez.MediaPlayer1.<method>` with a 5 s timeout. `Watch(poke)`: match
   PropertiesChanged and ObjectManager signals from org.bluez, plus NameOwnerChanged for
   org.bluez. The client runs as root (ipod-client.service has no User=), so the system
   bus policy allows it.

4. **Flags** (global, validated in `app.Before`): `--player=auto|stub` (default auto; stub =
   Level 2 behaviour, the no-code rollback lever), `--text=asis|translit`, `--text-max`
   (bytes, default 200, keeps every packet in the small format), `--demo-track
   "Title|Artist|Album"` (the stub's names: tests the car's font and long strings without a
   phone). In `serve`, set `newPlayer` to the AutoPlayer factory. In `processFrames`, `defer
   Close()` if the Player is an `io.Closer`. Keep the package default `newPlayer` as the
   stub so tests never touch a real bus.

5. **`ipod bt now|next|prev|play|pause|toggle|watch`** (`cmd/ipod/bt.go`): builds the same
   AutoPlayer. For the commands it builds an iAP `PlayControl` and runs it through
   `extremote.HandleExtRemote`, **the car's own code path** (next=3, prev=4, play=10,
   pause=11, toggle=1). Then `Wait` (busy done + 1.5 s + one re-read) and prints before and
   after. `now` prints the phone, codec, status, title/artist/album (raw and as sent),
   time, track n of m, and what the car sees. In ipod-gadget `extras/pi-audio/bin/aucd-bt`,
   add `now|next|prev|play|pause|toggle|watch` that exec `/usr/local/bin/ipod
   $IPOD_GLOBAL_ARGS bt <cmd>`, with the args read from `/etc/default/ipod-client`.

6. **Fix in `packet.go`**: `WritePacket` uses the small format up to and including 256
   bytes (`len(payload) > largePacketMinLen`), but the small length byte only holds 255;
   **a 256-byte payload is written with length byte 0x00, the large-packet marker, and is
   corrupt.** The condition should be `> 255`. Add a test. The 200-byte text cap avoids it
   for titles, but fix it anyway.

7. **Tests**:
   - Unit tests with a fake `phoneSource`: fallback/switch and its notifications; toggle
     decided from a fresh read (including a stale cache); command mapping; index rules
     (phone numbers / window alternation); position extrapolation with a fake clock;
     TrackInfo text modes and the cap.
   - An end-to-end test through `processFrames` (chanTransport, as in
     `extremote_test.go`): car PlayControl next -> fake `Call("Next")`; GetIndexedPlayingTrack*
     answer the phone's names.
   - `pickPhone` against hand-built `GetManagedObjects` maps.
   - Translit / truncate tests.
   - D-Bus integration in WSL against python-dbusmock (the scratchpad `bt_test.py` pattern):
     `dbusmock --system --template bluez5` plus a hand-added Device1 (Connected),
     MediaControl1 and MediaPlayer1 (methods that update Status/Track and emit
     PropertiesChanged). Run `ipod bt now/next/toggle` built for linux/amd64 and check the
     mock's call log and the output.
   - QEMU busybox VM (scratchpad `vm_run.sh`, fakecar): a long Cyrillic `--demo-track`
     (~180 bytes) answered to GetIndexedPlayingTrackTitle through the real gadget, i.e. a
     multi-report iPod->car frame, decoded byte for byte by fakecar.

8. **Docs**: `ipod-gadget/doc/q60-notes.md` (the car's control behaviour below);
   `ipod-gadget/extras/pi-audio/README.md` Level 3 section (desk checklist with `aucd-bt
   now/next/...`, car checklist, rollback); q60-pi-dash `docs/Q60_Pi_Dash_build_guide.md`
   roadmap (Level 3 status); this repo's README (`bt`, the new flags).

## The car's behaviour (from Vadym's Level 2 logs, 2026-10-18)

- Next / previous: PlayControl 3 / 4 (steering-wheel up/down, head unit buttons).
- Pause / resume: **only PlayControl 1 (toggle)**: from the iPod screen's play/pause button,
  the steering-wheel joystick push while play/pause is highlighted, and **automatically when
  leaving or returning to the iPod source** (FM, audio off). It never sends 10 (play) or
  11 (pause).
- While the client reports Paused, the car stops taking audio (the gadget drops samples
  cleanly since Level 2).
- Level 2 works end to end: iPhone -> A2DP (SBC) -> PipeWire -> gadget -> X9U6 -> Bose.
  The power problem was the power bank browning out; fixed with a 12 V -> 5 V 3 A supply.

## Known problems / open questions

- **Source-leave toggle with the phone already paused**: if the car toggles on leaving the
  source regardless of state, deciding from the phone's real status would *start* the phone
  (Play) while the car is on FM. Blind toggling has the same flaw. The PlayControl log line
  with the car's streaming state (point 1) should show whether toggle and alt 0/1 come in a
  usable order; a rule like "a toggle while the car is not taking audio never starts the
  phone" may be needed. Decide from the logs after the first car run.
- Whether the X9U6 caches track names by index, and whether it enables
  PlayStatusChangeNotifications at all (look for SetPlayStatusChangeNotification in the
  client's journal or trace). That decides whether the window alternation is needed.
- Debian trixie's PipeWire has no AAC Bluetooth codec, so the iPhone uses SBC (Level 2:
  sounds like wired iPod quality, fine).
- **Level 4 (album art) note**: GetColorDisplayImageLimits is the accessory asking for the
  **iPod's** screen size (to upload a logo with SetDisplayImage); it says nothing about the
  car showing artwork. Artwork *to* the car would be GetArtworkFormats / GetTrackArtworkTimes
  / GetTrackArtworkData. The client answers GetArtworkFormats with an empty list today.
  Check the traces for whether the X9U6 sends those, and note the limits it asks for.

## Next concrete step

Write `cmd/ipod/phone.go` (BluezPlayer + AutoPlayer, points 1-2) with its fake-source unit
tests, then `dbus_phone.go`, the flags and `bt.go`. The test harness pieces (WSL, dbusmock,
the QEMU VMs) are described in the ipod-gadget memory notes and the scratchpad scripts named
above. The scratchpad does not survive the session, so recreate the scripts from these
descriptions if they are gone.
