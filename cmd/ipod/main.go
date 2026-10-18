package main

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"sync"
	"time"

	"os"

	"github.com/davecgh/go-spew/spew"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"

	"github.com/oandrew/ipod"
	"github.com/oandrew/ipod/hid"
	audio "github.com/oandrew/ipod/lingo-audio"
	dispremote "github.com/oandrew/ipod/lingo-dispremote"
	extremote "github.com/oandrew/ipod/lingo-extremote"
	general "github.com/oandrew/ipod/lingo-general"
	_ "github.com/oandrew/ipod/lingo-simpleremote"
	"github.com/oandrew/ipod/trace"
)

var log = logrus.StandardLogger()

func openDevice(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR, os.ModePerm)
	if err != nil {
		return nil, err
	}
	stat, _ := f.Stat()
	if stat.Mode()&os.ModeCharDevice != os.ModeCharDevice {
		return nil, fmt.Errorf("not a char device")
	}
	return f, nil
}

// deviceFile is the iap device (g_ipod_hid's /dev/iapN). Its read() and write() share one
// mutex, and Go opens the device non-blocking: a write() that runs into a pending read() fails
// with EAGAIN, and Go then waits for the device's next wake-up, which may only be the head
// unit's next report. Notifications are written while a read is pending, so writes retry on a
// short deadline instead.
type deviceFile struct {
	*os.File
}

const deviceWriteRetry = 100 * time.Millisecond

func (f deviceFile) Write(p []byte) (int, error) {
	written := 0
	for {
		if err := f.SetWriteDeadline(time.Now().Add(deviceWriteRetry)); err != nil {
			// not pollable: blocking write
			n, err := f.File.Write(p[written:])
			return written + n, err
		}
		n, err := f.File.Write(p[written:])
		written += n
		if errors.Is(err, os.ErrDeadlineExceeded) {
			log.Debugf("device write waited %v, retrying", deviceWriteRetry)
			continue
		}
		return written, err
	}
}

func openTraceFile(path string) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func newTraceFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE, os.ModePerm)
}

type UsageError struct {
	error
}

var hidReportDefs = hid.DefaultReportDefs

// --lenient-auth: accept damaged RetDevAuthenticationInfo/Signature packets
var lenientAuth bool

// newPacketReader reads the packets of one frame, honouring --lenient-auth.
func newPacketReader(frame []byte) *ipod.PacketReader {
	pr := ipod.NewPacketReader(frame)
	pr.LenientAuth = lenientAuth
	pr.OnLenient = func(cmd byte, got, want int) {
		log.Warnf("lenient: accepted damaged frame cmd=0x%02x got=%d want=%d chk=bad", cmd, got, want)
	}
	return pr
}

// --hid layouts, each matching an ipod-gadget g_ipod_hid hid_desc of the same name
var hidLayouts = map[string]hid.ReportDefs{
	"default": hid.DefaultReportDefs,
	"legacy":  hid.LegacyReportDefs,
	"bigout":  hid.BigOutReportDefs,
}

func main() {
	logOut := os.Stdout
	log.Formatter = &TextFormatter{
		Colored: checkIfTerminal(logOut),
	}

	log.Out = logOut

	spew.Config.DisablePointerAddresses = true

	app := cli.NewApp()
	app.Name = "ipod"
	app.Authors = []cli.Author{
		cli.Author{
			Name: "Andrew Onyshchuk",
		},
	}
	app.Usage = "ipod accessory protocol client"
	app.HideVersion = true

	app.ErrWriter = os.Stderr
	app.Flags = []cli.Flag{
		cli.BoolFlag{
			Name:  "debug, d",
			Usage: "verbose logging",
		},
		cli.StringFlag{
			Name:  "hid",
			Value: "default",
			Usage: "hid report layout: default, legacy or bigout (must match g_ipod_hid hid_desc)",
		},
		cli.BoolFlag{
			Name:  "legacy, l",
			Usage: "same as --hid=legacy",
		},
		cli.BoolFlag{
			Name: "lenient-auth",
			Usage: "accept RetDevAuthenticationInfo/Signature packets that are short or fail their checksum " +
				"(the certificate and signature are never verified anyway)",
		},
		cli.StringFlag{
			Name:  "player",
			Value: playerAuto,
			Usage: "what serve tells the head unit: auto (the phone over Bluetooth while one plays, else the stub) " +
				"or stub (Level 2: a player of its own, always playing, nothing from the phone)",
		},
		cli.StringFlag{
			Name:  "text",
			Value: textAsIs,
			Usage: "track names to the head unit: asis or translit (Cyrillic in Latin letters)",
		},
		cli.IntFlag{
			Name:  "text-max",
			Value: 200,
			Usage: "track names to the head unit: at most this many bytes of UTF-8 (0: no limit)",
		},
		cli.StringFlag{
			Name:  "demo-track",
			Usage: "the stub's track names, \"Title|Artist|Album\" (default: title, artist, album)",
		},
		cli.StringFlag{
			Name:  "artwork",
			Value: artworkOff,
			Usage: "album art for the head unit, the same picture for every track: off, test (a colour test " +
				"pattern) or a PNG/JPEG `file`",
		},
		cli.StringFlag{
			Name:  "artwork-pixel",
			Value: artworkPixelLE,
			Usage: "the artwork's pixel format: 565le or 565be (RGB 565, byte order)",
		},
		cli.IntFlag{
			Name:  "artwork-chunk",
			Usage: "picture bytes per artwork packet; 0: as many as fit in one HID report (the Q60 keeps only the first report of a packet)",
		},
		cli.BoolTFlag{
			Name: "toggle-guard",
			Usage: "a play/pause toggle from the head unit while it takes no audio may pause the phone but never " +
				"starts it (it toggles when leaving the iPod source); --toggle-guard=false: toggles always follow the phone",
		},
	}

	app.ExitErrHandler = func(c *cli.Context, err error) {
		if err != nil {
			if _, ok := err.(UsageError); ok {
				fmt.Fprintf(c.App.ErrWriter, "usage error: %v\n\n", err)
				cli.ShowCommandHelp(c, c.Command.Name)
			} else {
				fmt.Fprintf(c.App.ErrWriter, "error: %v\n\n", err)
			}
			os.Exit(1)
		}
	}

	app.Before = func(c *cli.Context) error {
		if c.GlobalBool("debug") {
			log.SetLevel(logrus.DebugLevel)
		}

		layout := c.GlobalString("hid")
		if c.GlobalBool("legacy") {
			if c.GlobalIsSet("hid") && layout != "legacy" {
				return fmt.Errorf("--legacy and --hid=%s disagree", layout)
			}
			layout = "legacy"
		}
		defs, ok := hidLayouts[layout]
		if !ok {
			return fmt.Errorf("--hid=%s: expected default, legacy or bigout", layout)
		}
		hidReportDefs = defs
		log.Infof("hid report layout: %s", layout)
		lenientAuth = c.GlobalBool("lenient-auth")
		if lenientAuth {
			log.Warn("lenient auth: damaged authentication packets from the accessory are accepted")
		}

		opts, err := parsePlayerOptions(c.GlobalString("player"), c.GlobalString("text"), c.GlobalInt("text-max"),
			c.GlobalString("demo-track"), c.GlobalBoolT("toggle-guard"))
		if err != nil {
			return err
		}
		extremote.ArtworkChunk = artworkChunkFor(hidReportDefs)
		if n := c.GlobalInt("artwork-chunk"); n > 0 {
			if n < 20 || n > 240 {
				return fmt.Errorf("--artwork-chunk=%d: expected 20 to 240 (0: one HID report)", n)
			}
			extremote.ArtworkChunk = n &^ 1
		}
		// a bad --artwork costs the picture, not the iPod: the client runs on without it
		if opts.art, err = loadArtwork(c.GlobalString("artwork"), c.GlobalString("artwork-pixel")); err != nil {
			log.Errorf("%v: no artwork", err)
		}
		playerOpts = opts

		return nil
	}

	app.Commands = []cli.Command{
		{
			Name:  "lingos",
			Usage: "print all lingos/commands/ids",
			Action: func(c *cli.Context) error {
				fmt.Println("Registered lingos:")
				fmt.Println(ipod.DumpLingos())
				return nil
			},
		},
		{
			Name:      "serve",
			Aliases:   []string{"s"},
			ArgsUsage: "<dev>",
			Usage:     "respond to requests from a char device i.e. /dev/iap0",
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "write-trace, w",
					Usage: "Write trace to a `file`",
				},
			},
			Action: func(c *cli.Context) error {
				path := c.Args().First()
				if path == "" {
					return UsageError{fmt.Errorf("device path is missing")}
				}
				f, err := openDevice(path)
				le := log.WithField("path", path)
				if err != nil {
					le.WithError(err).Errorf("could not open the device")
					return err
				}
				le.Info("device opened")

				var rw io.ReadWriter = deviceFile{f}
				if tracePath := c.String("write-trace"); tracePath != "" {
					traceFile, err := newTraceFile(tracePath)
					le := log.WithField("path", tracePath)
					if err != nil {
						le.WithError(err).Errorf("could not create a trace file")
						return err
					}
					le.Warningf("writing trace")
					rw = trace.NewTracer(traceFile, deviceFile{f})
				}

				reportR, reportW := hid.NewReportReader(rw), hid.NewReportWriter(rw)
				frameTransport := hid.NewTransport(reportR, reportW, hidReportDefs)
				log.Infof("player: %s", playerOpts)
				newPlayer = func() Player { return playerOpts.newPlayer() }
				processFrames(frameTransport)
				return nil
			},
		},
		btCommand(),
		{
			Name:    "replay",
			Aliases: []string{"r"},
			Usage:   "respond to requests from a trace file",
			Action: func(c *cli.Context) error {
				path := c.Args().First()
				if path == "" {
					return UsageError{cli.NewExitError("trace file path is missing", 1)}
				}

				f, err := openTraceFile(path)
				le := log.WithField("path", path)
				if err != nil {
					le.WithError(err).Errorf("could not open the trace file")
					return err
				}
				le.Warningf("trace file opened")

				tr := trace.NewReader(f)
				tdr := trace.NewTraceDirReader(tr, trace.DirIn)
				reportR, reportW := hid.NewReportReader(tdr), hid.NewReportWriter(ioutil.Discard)
				frameTransport := hid.NewTransport(reportR, reportW, hidReportDefs)
				processFrames(frameTransport)
				return nil
			},
		},
		{
			Name:    "view",
			Aliases: []string{"v"},
			Usage:   "view a trace file",
			Action: func(c *cli.Context) error {
				path := c.Args().First()
				if path == "" {
					return UsageError{cli.NewExitError("trace file path is missing", 1)}
				}

				f, err := openTraceFile(path)
				le := log.WithField("path", path)
				if err != nil {
					le.WithError(err).Errorf("could not open the trace file")
					return err
				}
				le.Warningf("trace file opened")
				tr := trace.NewReader(f)
				dumpTrace(tr)
				return nil
			},
		},
		{
			Name: "send",
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "write-trace, w",
					Usage: "Write trace to a `file`",
				},
			},
			Usage: "acc mode / send accessory requests from a trace file",
			Action: func(c *cli.Context) error {
				path := c.Args().Get(0)
				if path == "" {
					return UsageError{fmt.Errorf("device path is missing")}
				}
				f, err := openDevice(path)
				le := log.WithField("path", path)
				if err != nil {
					le.WithError(err).Errorf("could not open the device")
					return err
				}
				le.Info("device opened")

				tpath := c.Args().Get(1)
				if tpath == "" {
					return UsageError{cli.NewExitError("trace file path is missing", 1)}
				}

				tf, err := openTraceFile(tpath)
				tle := log.WithField("path", tpath)
				if err != nil {
					tle.WithError(err).Errorf("could not open the trace file")
					return err
				}
				tle.Warningf("trace file opened")
				tr := trace.NewReader(tf)
				tdr := trace.NewTraceDirReader(tr, trace.DirIn)

				var rw io.ReadWriter = deviceFile{f}
				if tracePath := c.String("write-trace"); tracePath != "" {
					traceFile, err := newTraceFile(tracePath)
					le := log.WithField("path", tracePath)
					if err != nil {
						le.WithError(err).Errorf("could not create a trace file")
						return err
					}
					le.Warningf("writing trace")
					rw = trace.NewTracer(traceFile, deviceFile{f})
				}
				reportR, reportW := hid.NewReportReader(rw), hid.NewReportWriter(rw)
				dummyW := hid.NewReportWriter(ioutil.Discard)
				traceR := hid.NewReportReader(tdr)

				frameTransport := hid.NewTransport(reportR, dummyW, hidReportDefs)

				go processFrames(frameTransport)

				for {
					report, err := traceR.ReadReport()
					if err != nil {
						break
					}

					reportW.WriteReport(report)
					log.Infof("writing report\n%s", spew.Sdump(report))

					time.Sleep(1000 * time.Millisecond)
				}

				select {}
			},
		},
	}

	app.Setup()
	app.Run(os.Args)

}

func logFrame(frame []byte, err error, msg string) {
	le := FrameLogEntry(logrus.NewEntry(log), frame)
	if err != nil {
		le.WithError(err).Errorf(msg)
		return
	}
	le.Infof(msg)
	if log.Level == logrus.DebugLevel {
		spew.Fdump(log.Out, frame)
	}

}

func logPacket(pkt []byte, err error, msg string) {
	//le := PacketLogEntry(logrus.NewEntry(log), frame)
	le := log.WithField("len", len(pkt))
	if err != nil {
		le.WithError(err).Errorf(msg)
		return
	}
	le.Infof(msg)
	if log.Level == logrus.DebugLevel {
		spew.Fdump(log.Out, pkt)
	}
}

func logCmd(cmd *ipod.Command, err error, msg string) {
	le := CommandLogEntry(logrus.NewEntry(log), cmd)
	if err != nil {
		le.WithError(err).Errorf(msg)
		return
	}
	le.Infof(msg)
	if log.Level == logrus.DebugLevel {
		spew.Fdump(log.Out, cmd)
	}

}

// notifyInterval is how often PlayStatusChangeNotifications go out (an iPod: about 500 ms).
var notifyInterval = 500 * time.Millisecond

// Player is what the Extended Remote lingo reports and controls: DevExtRemote, a play state
// of its own, or something that follows a real player (the phone over Bluetooth AVRCP, say;
// it can also implement extremote.DeviceTrackInfo for the track names).
type Player interface {
	extremote.DeviceExtRemote
	// Notifications returns the PlayStatusChangeNotifications due now; called every
	// notifyInterval.
	Notifications() []*extremote.PlayStatusChangeNotification
	// Reset: the head unit (re)connects.
	Reset()
}

// newPlayer makes the Player for each processFrames run. serve sets it from the flags
// (playerOptions); the default, the stub, never touches D-Bus (tests).
var newPlayer = func() Player { return NewDevExtRemote(time.Now) }

// session is one processFrames run. Replies (processFrames) and notifications (notify) are
// sent from two goroutines, so anything that uses the serde or writes frames holds mu.
type session struct {
	mu    sync.Mutex
	serde ipod.CommandSerde
	w     ipod.FrameWriter
	ext   Player
}

func processFrames(frameTransport ipod.FrameReadWriter) {
	s := &session{w: frameTransport, ext: newPlayer()}
	if c, ok := s.ext.(io.Closer); ok {
		defer c.Close()
	}
	var notifier sync.WaitGroup
	done := make(chan struct{})
	notifier.Add(1)
	go func() {
		defer notifier.Done()
		s.notify(done)
	}()
	defer notifier.Wait()
	defer close(done)

	for {
		inFrame, err := frameTransport.ReadFrame()
		if err == io.EOF {
			break
		}
		logFrame(inFrame, err, "<< FRAME")
		if err != nil {
			continue
		}
		s.handleFrame(inFrame)
	}
	log.Warnf("EOF")
}

func (s *session) handleFrame(inFrame []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	packetReader := newPacketReader(inFrame)
	inCmdBuf := ipod.CmdBuffer{}
	for {
		inPacket, err := packetReader.ReadPacket()
		if err == io.EOF {
			break
		}
		logPacket(inPacket, err, "<< PACKET")
		if err != nil {
			continue
		}

		inCmd, err := s.serde.UnmarshalCmd(inPacket)
		logCmd(inCmd, err, "<< CMD")
		inCmdBuf.WriteCommand(inCmd)
	}

	outCmdBuf := ipod.CmdBuffer{}
	for i := range inCmdBuf.Commands {
		//todo: check return error
		handlePacket(&outCmdBuf, inCmdBuf.Commands[i], s.ext)
	}

	start := time.Now()
	var artPackets, artBytes int
	for i := range outCmdBuf.Commands {
		cmd := outCmdBuf.Commands[i]
		// a picture is a hundred packets or more: only the first is logged
		art, isArt := cmd.Payload.(*extremote.RetTrackArtworkData)
		if isArt {
			artPackets++
			artBytes += len(art.Data)
		}
		s.send(cmd, !isArt || art.PacketIndex == 0)
	}
	if artPackets > 0 {
		log.Infof("artwork: sent %d bytes in %d packets in %v", artBytes, artPackets, time.Since(start).Round(time.Millisecond))
	}
}

// send writes cmd as one frame. Unless verbose only failures are logged: the track position
// notifications go out twice a second while playing.
func (s *session) send(outCmd *ipod.Command, verbose bool) {
	if verbose {
		logCmd(outCmd, nil, ">> CMD")
	}

	outPacket, err := s.serde.MarshalCmd(outCmd)
	if verbose || err != nil {
		logPacket(outPacket, err, ">> PACKET")
	}

	packetWriter := ipod.NewPacketWriter()
	packetWriter.WritePacket(outPacket)
	outFrame := packetWriter.Bytes()
	outFrameErr := s.w.WriteFrame(outFrame)
	if verbose || outFrameErr != nil {
		logFrame(outFrame, outFrameErr, ">> FRAME")
	}
}

// notify sends the PlayStatusChangeNotifications the head unit enabled, every notifyInterval,
// until done is closed.
func (s *session) notify(done <-chan struct{}) {
	t := time.NewTicker(notifyInterval)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
		}
		// under mu: nothing computed before a command changes the state goes out after its reply
		s.mu.Lock()
		notes := s.ext.Notifications()
		cmds := ipod.CmdBuffer{}
		for _, n := range notes {
			ipod.Send(&cmds, n)
		}
		for i, cmd := range cmds.Commands {
			periodic := notes[i].Status == extremote.PlayStatusChangeTrackTimeMs ||
				notes[i].Status == extremote.PlayStatusChangeTrackTimeSec
			s.send(cmd, !periodic)
		}
		s.mu.Unlock()
	}
}

var devGeneral = &DevGeneral{}

func handlePacket(cmdWriter ipod.CommandWriter, cmd *ipod.Command, ext Player) {
	switch cmd.ID.LingoID() {
	case ipod.LingoGeneralID:
		if auth, ok := cmd.Payload.(*general.RetDevAuthenticationInfo); ok {
			if auth.Major >= 2 && auth.CertCurrentSection >= auth.CertMaxSection || auth.Major < 2 {
				audio.Start(cmdWriter)
			}
		}
		switch cmd.Payload.(type) {
		case *general.StartIDPS, *general.IdentifyDeviceLingoes:
			// the head unit (re)connects
			ext.Reset()
		}
		general.HandleGeneral(cmd, cmdWriter, devGeneral)

	case ipod.LingoSimpleRemoteID:
		//todo
		log.Warn("Lingo SimpleRemote is not supported yet")
	case ipod.LingoDisplayRemoteID:
		dispremote.HandleDispRemote(cmd, cmdWriter, nil)
	case ipod.LingoExtRemoteID:
		extremote.HandleExtRemote(cmd, cmdWriter, ext)
	case ipod.LingoDigitalAudioID:
		audio.HandleAudio(cmd, cmdWriter, nil)
	}
}
func dirPrefix(dir trace.Dir, text string) string {
	switch dir {
	case trace.DirIn:
		return "<< " + text
	case trace.DirOut:
		return ">> " + text
	default:
		return "?? " + text
	}
}
func dumpTrace(tr *trace.Reader) {
	q := trace.Queue{}
	for {
		var msg trace.Msg
		err := tr.ReadMsg(&msg)
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatal(err)
		}
		q.Enqueue(&msg)
	}

	serde := ipod.CommandSerde{}

	for {
		head := q.Head()
		if head == nil {
			break
		}
		dir := head.Dir
		tdr := trace.NewQueueDirReader(&q, dir)
		d := hid.NewDecoder(hid.NewReportReader(tdr), hidReportDefs)

		frame, err := d.ReadFrame()
		if err == io.EOF {
			break
		}
		logFrame(frame, err, dirPrefix(dir, "FRAME"))
		if err != nil {
			continue
		}

		packetReader := newPacketReader(frame)
		for {
			packet, err := packetReader.ReadPacket()
			if err == io.EOF {
				break
			}
			logPacket(packet, err, dirPrefix(dir, "PACKET"))
			if err != nil {
				continue
			}

			cmd, err := serde.UnmarshalCmd(packet)
			logCmd(cmd, err, dirPrefix(dir, "CMD"))
		}
	}
	log.Warnf("EOF")
}
