# ipod
[![Join the chat at https://gitter.im/ipod-gadget/Lobby](https://badges.gitter.im/ipod-gadget/Lobby.svg)](https://gitter.im/ipod-gadget/Lobby?utm_source=badge&utm_medium=badge&utm_campaign=pr-badge&utm_content=badge)
[![GoDoc](https://godoc.org/github.com/oandrew/ipod?status.svg)](https://godoc.org/github.com/oandrew/ipod)
[![Build Status](https://travis-ci.org/oandrew/ipod.svg?branch=master)](https://travis-ci.org/oandrew/ipod)

ipod is a golang userspace library implementation of the ipod accessory protocol.
It includes an example client for use with https://github.com/oandrew/ipod-gadget

This is a total rewrite of what was included with the  ipod-gadget project. 
It should work as a drop-in replacement for the old app.

New features:
- Storing and replaying traces
- Detailed verbose logging for debug
- Better codebase with message type definitions
- Tests

### update 03/2020
kernel module needs to be recompiled due to a breaking change (hid descriptor).  
This should fix the issue with hanging after `GetDevAuthenticationInfo` on some devices.  
At least it's finally working in my own car :)

# build and run
```
GO111MODULE=on go build github.com/oandrew/ipod/cmd/ipod
# or cross compiling
GO111MODULE=on GOOS=linux GOARCH=arm GOARM=6 go build github.com/oandrew/ipod/cmd/ipod
# Raspberry Pi 5 / any 64-bit Raspberry Pi OS (static binary, from a checkout of this repo)
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./cmd/ipod

# with debug logging
./ipod -d serve /dev/iap0

# save a trace file
./ipod -d serve -w ipod.trace /dev/iap0

# simulate incoming requests from a trace file
./ipod -d replay ./ipod.trace

# view a trace file
./ipod -d view ./ipod.trace

# HID report layout: default, legacy or bigout. Global flag (before the command), and it
# must match the descriptor ipod-gadget offers (g_ipod_hid hid_desc=); --legacy = --hid=legacy
./ipod -d --hid=bigout serve /dev/iap0

# accept RetDevAuthenticationInfo/Signature packets that arrive short or with a bad
# checksum (some head units damage them; they are never verified). Other packets stay strict.
./ipod -d --lenient-auth serve /dev/iap0

# the phone over Bluetooth (BlueZ AVRCP, org.bluez.MediaPlayer1) on the head unit's screen
# and buttons: the default, --player=auto. With no phone connected (or no D-Bus) the stub
# player answers, always playing; --player=stub uses only the stub
./ipod -d --lenient-auth --player=stub serve /dev/iap0

# track names to the head unit: as they are, or Cyrillic in Latin letters, at most
# --text-max bytes of UTF-8 (default 200, cut on a character boundary)
./ipod -d --text=translit --text-max=120 serve /dev/iap0

# a play/pause toggle while the head unit takes no audio (g_ipod_audio streaming=N) may pause
# the phone but never starts it (default); =false: every toggle follows the phone
./ipod -d --toggle-guard=false serve /dev/iap0

# the stub's track names, to see the head unit's font without a phone
./ipod -d --player=stub --demo-track "Группа крови|Кино|Группа крови" serve /dev/iap0

# the phone as the head unit sees it, and the head unit's buttons (sent as its PlayControl,
# through the same code); global flags as for serve
./ipod bt now
./ipod bt next      # also prev, play, pause, toggle
./ipod bt watch     # a line on every change, ctrl-c to stop
```

The Bluetooth side needs the system D-Bus and BlueZ (`github.com/godbus/dbus/v5`, pure Go: the
first build downloads it). `cmd/ipod/scripts/bt-dbusmock-test.py` runs `ipod bt` against a
mocked BlueZ.

Client app godoc https://godoc.org/github.com/oandrew/ipod/cmd/ipod

Refer to https://github.com/oandrew/ipod-gadget for more info on how to get the kernel part working.






