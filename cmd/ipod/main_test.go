package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/oandrew/ipod"
	"github.com/oandrew/ipod/hid"
	general "github.com/oandrew/ipod/lingo-general"
	"github.com/oandrew/ipod/trace"
)

// fakeTransport hands processFrames a list of frames and keeps what it writes back.
type fakeTransport struct {
	in  [][]byte
	out [][]byte
}

func (t *fakeTransport) ReadFrame() ([]byte, error) {
	if len(t.in) == 0 {
		return nil, io.EOF
	}
	f := t.in[0]
	t.in = t.in[1:]
	return f, nil
}

func (t *fakeTransport) WriteFrame(data []byte) error {
	t.out = append(t.out, append([]byte(nil), data...))
	return nil
}

type reportList struct{ reports []hid.Report }

func (r *reportList) ReadReport() (hid.Report, error) {
	if len(r.reports) == 0 {
		return hid.Report{}, io.EOF
	}
	rep := r.reports[0]
	r.reports = r.reports[1:]
	return rep, nil
}

// frames decodes HID reports into frames, as ipod serve does.
func frames(t *testing.T, r hid.ReportReader) [][]byte {
	d := hid.NewDecoder(r, hid.DefaultReportDefs)
	var out [][]byte
	for {
		f, err := d.ReadFrame()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, append([]byte(nil), f...))
	}
}

// serve runs processFrames over the frames and returns the commands it answered with
// and its log.
func serve(t *testing.T, in [][]byte, lenient bool) ([]*ipod.Command, string) {
	var logBuf bytes.Buffer
	// the formatter main() installs, so error lines read "ERRO[...]" as in the journal
	prevOut, prevFormatter, prevLenient := log.Out, log.Formatter, lenientAuth
	log.Out, log.Formatter, lenientAuth = &logBuf, &TextFormatter{}, lenient
	defer func() { log.Out, log.Formatter, lenientAuth = prevOut, prevFormatter, prevLenient }()

	tr := &fakeTransport{in: in}
	processFrames(tr)

	serde := ipod.CommandSerde{TrxEnabled: true}
	var cmds []*ipod.Command
	for _, f := range tr.out {
		pr := ipod.NewPacketReader(f)
		for {
			pkt, err := pr.ReadPacket()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("client wrote an unreadable packet: %v", err)
			}
			cmd, err := serde.UnmarshalCmd(pkt)
			if err != nil {
				t.Fatalf("client wrote an undecodable command: %v", err)
			}
			cmds = append(cmds, cmd)
		}
	}
	return cmds, logBuf.String()
}

func ackFor(cmds []*ipod.Command, cmdID uint8, trx uint16) bool {
	for _, c := range cmds {
		if a, ok := c.Payload.(*general.ACK); ok && a.CmdID == cmdID && a.Status == general.ACKStatusSuccess &&
			c.Transaction != nil && uint16(*c.Transaction) == trx {
			return true
		}
	}
	return false
}

func count(cmds []*ipod.Command, match func(interface{}) bool) int {
	n := 0
	for _, c := range cmds {
		if match(c.Payload) {
			n++
		}
	}
	return n
}

// The raw trace from the 2014 Infiniti Q60 (Clarion X9U6): its RetDevAuthenticationInfo
// (section 0 of 3) arrives 4 bytes short, see ipod-gadget doc/q60-notes.md.
func q60Frames(t *testing.T) [][]byte {
	f, err := os.Open("../../hid/testdata/q60-clarion-x9u6-auth-fail.trace")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	return frames(t, hid.NewReportReader(trace.NewTraceDirReader(trace.NewReader(f), trace.DirIn)))
}

func TestQ60TraceLenientAuth(t *testing.T) {
	cmds, logText := serve(t, q60Frames(t), true)
	if !ackFor(cmds, 0x15, 0x0001) {
		t.Fatalf("no ACK for RetDevAuthenticationInfo (trx 0x0001); log:\n%s", logText)
	}
	if strings.Contains(logText, "ERRO") {
		t.Fatalf("errors logged:\n%s", logText)
	}
	if !strings.Contains(logText, "lenient: accepted damaged frame cmd=0x15 got=248 want=251 chk=bad") {
		t.Fatalf("no lenient warning; log:\n%s", logText)
	}
}

func TestQ60TraceStrictUnchanged(t *testing.T) {
	cmds, logText := serve(t, q60Frames(t), false)
	if ackFor(cmds, 0x15, 0x0001) {
		t.Fatal("damaged RetDevAuthenticationInfo acknowledged without --lenient-auth")
	}
	if !strings.Contains(logText, "ERRO") {
		t.Fatalf("damaged packet not reported; log:\n%s", logText)
	}
}

// clarionReports splits a frame the way the Clarion X9U6 does: report 0x09 with every
// report but the last marked 0x02, and 2 bytes lost after each middle report.
func clarionReports(frame []byte) []hid.Report {
	var reports []hid.Report
	for off, i := 0, 0; off < len(frame); i++ {
		end := off + 62
		if end > len(frame) {
			end = len(frame)
		}
		data := make([]byte, 62)
		copy(data, frame[off:end])
		reports = append(reports, hid.Report{ID: 0x09, LinkControl: hid.LinkControlMoreToFollow, Data: data})
		off = end
		if i > 0 && off < len(frame) { // a middle report: the head unit skips 2 bytes
			off += 2
		}
	}
	reports[len(reports)-1].LinkControl = hid.LinkControlContinue
	return reports
}

func TestLenientAuthFullHandshake(t *testing.T) {
	var reports []hid.Report
	add := func(payload []byte) {
		w := ipod.NewPacketWriter()
		w.WritePacket(payload)
		f := w.Bytes()
		if len(f) <= 62 {
			data := make([]byte, 62)
			copy(data, f)
			reports = append(reports, hid.Report{ID: 0x09, LinkControl: hid.LinkControlDone, Data: data})
			return
		}
		reports = append(reports, clarionReports(f)...)
	}
	add([]byte{0x00, 0x38, 0x00, 0x00}) // StartIDPS: transactions from here on
	cert := bytes.Repeat([]byte{0x30, 0x82}, 120)
	for section := byte(0); section <= 3; section++ {
		// RetDevAuthenticationInfo v2.0, section n of 3: a 251-byte frame like the car's
		add(append([]byte{0x00, 0x15, 0x00, 0x10 + section, 0x02, 0x00, section, 0x03}, cert...))
	}
	// RetDevAuthenticationSignature: a 135-byte frame, 3 reports, 2 bytes lost
	add(append([]byte{0x00, 0x18, 0x00, 0x20}, bytes.Repeat([]byte{0x5a}, 128)...))
	in := frames(t, &reportList{reports: reports})

	cmds, logText := serve(t, in, true)
	for section := uint16(0); section < 3; section++ {
		if !ackFor(cmds, 0x15, 0x10+section) {
			t.Errorf("section %d not acknowledged", section)
		}
	}
	if count(cmds, func(p interface{}) bool { _, ok := p.(*general.AckDevAuthenticationInfo); return ok }) != 1 ||
		count(cmds, func(p interface{}) bool { _, ok := p.(*general.GetDevAuthenticationSignatureV2); return ok }) != 1 {
		t.Errorf("last section: want AckDevAuthenticationInfo and GetDevAuthenticationSignatureV2 once each")
	}
	passed := count(cmds, func(p interface{}) bool {
		s, ok := p.(*general.AckDevAuthenticationStatus)
		return ok && s.Status == general.DevAuthStatusPassed
	})
	if passed != 1 {
		t.Errorf("damaged signature: %d AckDevAuthenticationStatus Passed, want 1", passed)
	}
	if n := strings.Count(logText, "lenient: accepted damaged frame"); n != 5 {
		t.Errorf("%d lenient warnings, want 5 (4 sections + signature)", n)
	}
	if t.Failed() {
		t.Logf("log:\n%s", logText)
	}

	cmds, _ = serve(t, in, false)
	if ackFor(cmds, 0x15, 0x10) || count(cmds, func(p interface{}) bool {
		_, ok := p.(*general.AckDevAuthenticationStatus)
		return ok
	}) != 0 {
		t.Error("damaged authentication packets accepted without --lenient-auth")
	}
}
