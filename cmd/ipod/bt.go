package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/urfave/cli"

	"github.com/oandrew/ipod"
	extremote "github.com/oandrew/ipod/lingo-extremote"
)

// ipod bt: the phone as the head unit sees it, and the head unit's buttons, from a shell.
// The commands go through extremote.HandleExtRemote as an iAP PlayControl: the head unit's
// own code path, decisions and log lines included.

var btPlayControls = map[string]extremote.PlayControlCmd{
	"next":   extremote.PlayControlNextTrack,
	"prev":   extremote.PlayControlPrevTrack,
	"play":   extremote.PlayControlPlay,
	"pause":  extremote.PlayControlPause,
	"toggle": extremote.PlayControlToggle,
}

func btCommand() cli.Command {
	return cli.Command{
		Name:      "bt",
		Usage:     "the phone over Bluetooth as the head unit sees it, and the head unit's buttons",
		ArgsUsage: "now|next|prev|play|pause|toggle|watch",
		Description: "now: the phone, its codec, status and track, and what the head unit is told\n" +
			"   next|prev|play|pause|toggle: sent as the head unit's PlayControl (3, 4, 10, 11, 1), then\n" +
			"   the phone before and after; toggle obeys --toggle-guard like the head unit's\n" +
			"   watch: a line whenever what the head unit is told changes (ctrl-c to stop)\n" +
			"Global flags (--text, --text-max, --toggle-guard) as for serve.",
		Action: func(c *cli.Context) error {
			cmd := c.Args().First()
			if cmd == "" {
				return UsageError{fmt.Errorf("command missing")}
			}
			if _, ok := btPlayControls[cmd]; !ok && cmd != "now" && cmd != "watch" {
				return UsageError{fmt.Errorf("unknown command %q", cmd)}
			}
			src := newDBusPhones()
			if _, err := src.Read(); err != nil {
				return fmt.Errorf("BlueZ: %v", err)
			}
			if cmd == "watch" {
				stop := make(chan os.Signal, 1)
				signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
				return runBT(os.Stdout, src, cmd, stop)
			}
			return runBT(os.Stdout, src, cmd, nil)
		},
	}
}

// runBT runs a bt command against src; watch runs until stop.
func runBT(w io.Writer, src phoneSource, cmd string, stop <-chan os.Signal) error {
	phone := newBluezPlayer(src, time.Now, playerOpts.text, playerOpts.toggleGuard)
	phone.start()
	defer phone.Close()
	auto := newAutoPlayer(NewDevExtRemote(time.Now), phone, playerOpts.text, playerOpts.demo)

	switch cmd {
	case "now":
		printNow(w, phone, auto)
		return nil
	case "watch":
		return watchBT(w, auto, stop)
	}

	if !phone.HasPlayer() {
		printNow(w, phone, auto)
		return fmt.Errorf("no connected phone with a media player: nothing to send %s to", cmd)
	}
	fmt.Fprintln(w, "before:")
	printNow(w, phone, auto)

	req, err := ipod.BuildCommand(&extremote.PlayControl{Cmd: btPlayControls[cmd]})
	if err != nil {
		return err
	}
	var replies ipod.CmdBuffer
	extremote.HandleExtRemote(req, &replies, auto)
	for _, r := range replies.Commands {
		if ack, ok := r.Payload.(*extremote.ACK); ok {
			fmt.Fprintf(w, "\nhead unit's PlayControl %s: ACK status %d\n", playControlName(btPlayControls[cmd]), ack.Status)
		}
	}
	phone.Wait()

	fmt.Fprintln(w, "\nafter:")
	printNow(w, phone, auto)
	return nil
}

func printNow(w io.Writer, phone *BluezPlayer, auto *AutoPlayer) {
	st, at := phone.snapshot()
	streaming := carStreaming()
	if st.Player == "" {
		fmt.Fprintln(w, "  phone:     none (no connected phone with a media player)")
	} else {
		fmt.Fprintf(w, "  phone:     %s (%s), codec %s\n", st.Name, st.Address, orNone(st.Codec))
		fmt.Fprintf(w, "  player:    %s, %s, %s", st.Player, st.Status, msTime(st.Position))
		if st.Track.Duration > 0 {
			fmt.Fprintf(w, " / %s", msTime(st.Track.Duration))
		}
		fmt.Fprintf(w, " (read %s)\n", at.Format("15:04:05.000"))
		fmt.Fprintf(w, "  track:     %s\n", describeTrackNumber(st.Track))
		title, artist, album := phone.TrackInfo(0)
		for _, f := range []struct{ name, raw, sent string }{
			{"title", st.Track.Title, title}, {"artist", st.Track.Artist, artist}, {"album", st.Track.Album, album},
		} {
			fmt.Fprintf(w, "  %-9s  %q", f.name+":", f.raw)
			if f.sent != f.raw {
				fmt.Fprintf(w, ", sent as %q", f.sent)
			}
			fmt.Fprintf(w, " (%d bytes sent)\n", len(f.sent))
		}
	}
	fmt.Fprintf(w, "  head unit: %s\n", carView(auto))
	fmt.Fprintf(w, "  car audio: streaming %s (%s)\n", streaming, streamingPath)
}

// carView is what the head unit is told, without the position.
func carView(a *AutoPlayer) string {
	side := "stub"
	if a.OnPhone() {
		side = "phone"
	}
	length, pos, state := a.PlaybackStatus()
	index, num := a.CurrentTrack()
	title, artist, album := a.TrackInfo(index)
	return fmt.Sprintf("%s, %s, track %d of %d, %s / %s, %q / %q / %q",
		side, stateName(state), index+1, num, msTime(pos), msTime(length), title, artist, album)
}

func watchBT(w io.Writer, a *AutoPlayer, stop <-chan os.Signal) error {
	t := time.NewTicker(notifyInterval)
	defer t.Stop()
	last := ""
	for {
		side := "stub"
		if a.OnPhone() {
			side = "phone"
		}
		_, _, state := a.PlaybackStatus()
		index, num := a.CurrentTrack()
		title, artist, album := a.TrackInfo(index)
		line := fmt.Sprintf("%s, %s, track %d of %d, %q / %q / %q, car streaming %s",
			side, stateName(state), index+1, num, title, artist, album, carStreaming())
		if line != last {
			fmt.Fprintf(w, "%s  %s\n", time.Now().Format("15:04:05.000"), line)
			last = line
		}
		select {
		case <-stop:
			return nil
		case <-t.C:
		}
	}
}
