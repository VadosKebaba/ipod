package hid_test

import (
	"io"
	"io/ioutil"
	"os"
	"reflect"
	"testing"

	"github.com/oandrew/ipod/hid"
	"github.com/oandrew/ipod/trace"
)

type testReportWriter struct {
	reports []hid.Report
}

func (rw *testReportWriter) WriteReport(report hid.Report) error {
	rw.reports = append(rw.reports, report)
	return nil
}

type testReportReader struct {
	offset  int
	reports []hid.Report
}

func (rr *testReportReader) ReadReport() (hid.Report, error) {
	if rr.offset >= len(rr.reports) {
		return hid.Report{}, io.EOF
	}
	defer func() { rr.offset++ }()
	return rr.reports[rr.offset], nil
}

var testReportDefs1 = hid.ReportDefs{
	hid.ReportDef{ID: 0x01, Len: 2, Dir: hid.ReportDirAccIn},
}

var testReportDefs2 = hid.ReportDefs{
	hid.ReportDef{ID: 0x01, Len: 2, Dir: hid.ReportDirAccIn},
	hid.ReportDef{ID: 0x02, Len: 3, Dir: hid.ReportDirAccIn},
}

var testReportDefs3 = hid.ReportDefs{
	hid.ReportDef{ID: 0x01, Len: 4, Dir: hid.ReportDirAccIn},
}

func TestEncoder(t *testing.T) {
	tests := []struct {
		name       string
		reportDefs hid.ReportDefs
		data       []byte
		want       []hid.Report
		wantErr    bool
	}{
		{"report-1-pkt-1", testReportDefs1, []byte{0x01}, []hid.Report{
			{ID: 0x01, LinkControl: hid.LinkControlDone, Data: []byte{0x01}},
		}, false},
		{"report-1-pkt-2", testReportDefs1, []byte{0x01, 0x02}, []hid.Report{
			{ID: 0x01, LinkControl: hid.LinkControlMoreToFollow, Data: []byte{0x01}},
			{ID: 0x01, LinkControl: hid.LinkControlContinue, Data: []byte{0x02}},
		}, false},
		{"report-1-pkt-3", testReportDefs1, []byte{0x01, 0x02, 0x03}, []hid.Report{
			{ID: 0x01, LinkControl: hid.LinkControlMoreToFollow, Data: []byte{0x01}},
			{ID: 0x01, LinkControl: hid.LinkControlContinue | hid.LinkControlMoreToFollow, Data: []byte{0x02}},
			{ID: 0x01, LinkControl: hid.LinkControlContinue, Data: []byte{0x03}},
		}, false},

		{"report-2-pkt-1", testReportDefs2, []byte{0x01}, []hid.Report{
			{ID: 0x01, LinkControl: hid.LinkControlDone, Data: []byte{0x01}},
		}, false},
		{"report-2-pkt-2", testReportDefs2, []byte{0x01, 0x02}, []hid.Report{
			{ID: 0x02, LinkControl: hid.LinkControlDone, Data: []byte{0x01, 0x02}},
		}, false},
		{"report-2-pkt-3", testReportDefs2, []byte{0x01, 0x02, 0x03}, []hid.Report{
			{ID: 0x02, LinkControl: hid.LinkControlMoreToFollow, Data: []byte{0x01, 0x02}},
			{ID: 0x01, LinkControl: hid.LinkControlContinue, Data: []byte{0x03}},
		}, false},

		{"report-3-pkt-1", testReportDefs3, []byte{0x01, 0x02}, []hid.Report{
			{ID: 0x01, LinkControl: hid.LinkControlDone, Data: []byte{0x01, 0x02, 0x00}},
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rw := &testReportWriter{}
			enc := hid.NewEncoder(rw, tt.reportDefs)
			err := enc.WriteFrame(tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("TestHidEncoder() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(rw.reports, tt.want) {
				t.Errorf("TestHidEncoder() = [%+v], want [%+v]", rw.reports, tt.want)
			}
		})
	}
}

func TestHidDecoder(t *testing.T) {
	tests := []struct {
		name       string
		reportDefs hid.ReportDefs
		want       []byte
		reports    []hid.Report

		wantErr bool
	}{
		{"report-1-pkt-1", testReportDefs1, []byte{0x01}, []hid.Report{
			{ID: 0x01, LinkControl: hid.LinkControlDone, Data: []byte{0x01}},
		}, false},
		{"report-1-pkt-2", testReportDefs1, []byte{0x01, 0x02}, []hid.Report{
			{ID: 0x01, LinkControl: hid.LinkControlMoreToFollow, Data: []byte{0x01}},
			{ID: 0x01, LinkControl: hid.LinkControlContinue, Data: []byte{0x02}},
		}, false},
		{"report-1-pkt-3", testReportDefs1, []byte{0x01, 0x02, 0x03}, []hid.Report{
			{ID: 0x01, LinkControl: hid.LinkControlMoreToFollow, Data: []byte{0x01}},
			{ID: 0x01, LinkControl: hid.LinkControlContinue | hid.LinkControlMoreToFollow, Data: []byte{0x02}},
			{ID: 0x01, LinkControl: hid.LinkControlContinue, Data: []byte{0x03}},
		}, false},

		{"report-2-pkt-1", testReportDefs2, []byte{0x01}, []hid.Report{
			{ID: 0x01, LinkControl: hid.LinkControlDone, Data: []byte{0x01}},
		}, false},
		{"report-2-pkt-2", testReportDefs2, []byte{0x01, 0x02}, []hid.Report{
			{ID: 0x02, LinkControl: hid.LinkControlDone, Data: []byte{0x01, 0x02}},
		}, false},
		{"report-2-pkt-3", testReportDefs2, []byte{0x01, 0x02, 0x03}, []hid.Report{
			{ID: 0x02, LinkControl: hid.LinkControlMoreToFollow, Data: []byte{0x01, 0x02}},
			{ID: 0x01, LinkControl: hid.LinkControlContinue, Data: []byte{0x03}},
		}, false},

		{"report-3-pkt-1", testReportDefs3, []byte{0x01, 0x02, 0x00}, []hid.Report{
			{ID: 0x01, LinkControl: hid.LinkControlDone, Data: []byte{0x01, 0x02, 0x00}},
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			rr := &testReportReader{
				reports: tt.reports,
			}
			dec := hid.NewDecoder(rr, tt.reportDefs)
			payload, err := dec.ReadFrame()
			if (err != nil) != tt.wantErr {
				t.Errorf("TestHidDecoder() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(payload, tt.want) {
				t.Errorf("TestHidDecoder() = %v, want %v", payload, tt.want)
			}
		})
	}
}

func BenchmarkDecoder(b *testing.B) {
	report := []byte{
		0x12, 0x00, 0x55, 0x28, 0x0a, 0x03, 0x03, 0xe7,
		0x00, 0x00, 0x1f, 0x40, 0x00, 0x00, 0x2b, 0x11,
		0x00, 0x00, 0x2e, 0xe0, 0x00, 0x00, 0x3e, 0x80,
		0x00, 0x00, 0x56, 0x22, 0x00, 0x00, 0x5d, 0xc0,
		0x00, 0x00, 0x7d, 0x00, 0x00, 0x00, 0xac, 0x44,
		0x00, 0x00, 0xbb, 0x80, 0x3d, 0x00, 0x00, 0x00,
		0x00,
	}
	r := hid.SingleReport(report)
	d := hid.NewDecoderDefault(r)
	for i := 0; i < b.N; i++ {
		d.ReadFrame()
	}
}

func BenchmarkEncoder(b *testing.B) {
	frame := []byte{
		0x55, 0x28, 0x0a, 0x03, 0x03, 0xe7, 0x00, 0x00,
		0x1f, 0x40, 0x00, 0x00, 0x2b, 0x11, 0x00, 0x00,
		0x2e, 0xe0, 0x00, 0x00, 0x3e, 0x80, 0x00, 0x00,
		0x56, 0x22, 0x00, 0x00, 0x5d, 0xc0, 0x00, 0x00,
		0x7d, 0x00, 0x00, 0x00, 0xac, 0x44, 0x00, 0x00,
		0xbb, 0x80, 0x3d, 0x00, 0x00, 0x00, 0x00,
	}
	e := hid.NewEncoderDefault(hid.NewReportWriter(ioutil.Discard))
	for i := 0; i < b.N; i++ {
		e.WriteFrame(frame)
	}
}

// Frames sent to the accessory must only use the descriptor's Input reports
// (iPod -> accessory). An Output report id on the interrupt IN endpoint is not
// a valid report for the accessory's HID parser.
func TestEncoderUsesInputReports(t *testing.T) {
	tests := []struct {
		name     string
		defs     hid.ReportDefs
		inputIDs map[byte]bool
	}{
		// ipod-gadget's full speed descriptor: 1-4 Input, 5-9 Output
		{"default", hid.DefaultReportDefs, map[byte]bool{1: true, 2: true, 3: true, 4: true}},
		// hid_desc=bigout: the same plus Output 0x0A, which must never be sent
		{"bigout", hid.BigOutReportDefs, map[byte]bool{1: true, 2: true, 3: true, 4: true}},
		// high speed descriptor of a real iPod: 1-12 Input, 13-21 Output
		{"legacy", hid.LegacyReportDefs, map[byte]bool{1: true, 2: true, 3: true, 4: true, 5: true, 6: true,
			7: true, 8: true, 9: true, 10: true, 11: true, 12: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for size := 1; size <= 2000; size++ {
				w := &testReportWriter{}
				if err := hid.NewEncoder(w, tt.defs).WriteFrame(make([]byte, size)); err != nil {
					t.Fatalf("frame of %d bytes: %v", size, err)
				}
				for _, r := range w.reports {
					if !tt.inputIDs[r.ID] {
						t.Fatalf("frame of %d bytes sent with report id %d, which is not an Input report", size, r.ID)
					}
				}
			}
		})
	}
}

// iapFrame returns an iAP frame of n bytes (0x55, length, payload, checksum) with a
// recognisable payload.
func iapFrame(n int) []byte {
	f := make([]byte, n)
	f[0], f[1] = 0x55, byte(n-3)
	for i := 2; i < n-1; i++ {
		f[i] = byte(i)
	}
	return f
}

func TestDecoderBigOutReport(t *testing.T) {
	// hid_desc=bigout: a 251-byte frame from the accessory arrives in one 0x0A report
	frame := iapFrame(251)
	data := make([]byte, 254) // Len 255 = link control + 254 bytes, padded
	copy(data, frame)
	report := hid.Report{ID: 0x0A, LinkControl: hid.LinkControlDone, Data: data}
	r := &testReportReader{reports: []hid.Report{report}}
	got, err := hid.NewDecoder(r, hid.BigOutReportDefs).ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got[:len(frame)], frame) {
		t.Fatalf("frame not decoded intact")
	}
	r = &testReportReader{reports: []hid.Report{report}}
	if _, err := hid.NewDecoder(r, hid.DefaultReportDefs).ReadFrame(); err == nil || err == io.EOF {
		t.Fatalf("report 0x0A accepted without the bigout layout: %v", err)
	}
}

// split cuts a frame into 62-byte report payloads (report 0x09 of the default layout).
func split(frame []byte) [][]byte {
	var parts [][]byte
	for len(frame) > 62 {
		parts = append(parts, frame[:62])
		frame = frame[62:]
	}
	return append(parts, frame)
}

func TestDecoderMoreToFollowMidFrame(t *testing.T) {
	// How the Clarion X9U6 (2014 Infiniti Q60) splits a long frame: every report but the
	// last is marked 0x02, "first, more to follow", instead of 0x03 for the middle ones.
	frame := iapFrame(251)
	parts := split(frame)
	var reports []hid.Report
	for i, p := range parts {
		lc := hid.LinkControlMoreToFollow
		if i == len(parts)-1 {
			lc = hid.LinkControlContinue
		}
		reports = append(reports, hid.Report{ID: 0x09, LinkControl: lc, Data: p})
	}
	// and a correctly marked short frame after it must still start a frame of its own
	short := iapFrame(20)
	reports = append(reports,
		hid.Report{ID: 0x09, LinkControl: hid.LinkControlMoreToFollow, Data: short[:10]},
		hid.Report{ID: 0x09, LinkControl: hid.LinkControlContinue, Data: short[10:]})

	d := hid.NewDecoder(&testReportReader{reports: reports}, hid.DefaultReportDefs)
	got, err := d.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, frame) {
		t.Fatalf("long frame: got %d bytes, want the %d-byte frame", len(got), len(frame))
	}
	got, err = d.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, short) {
		t.Fatalf("short frame after it: got % x", got)
	}
}

// The raw HID trace of the failing session in the Q60 (ipod-gadget
// doc/traces/q60-clarion-x9u6-auth-fail.trace). Skipped until it is copied here.
func TestQ60ClarionTrace(t *testing.T) {
	f, err := os.Open("testdata/q60-clarion-x9u6-auth-fail.trace")
	if err != nil {
		t.Skip("no testdata/q60-clarion-x9u6-auth-fail.trace")
	}
	defer f.Close()
	in := trace.NewTraceDirReader(trace.NewReader(f), trace.DirIn)
	d := hid.NewDecoder(hid.NewReportReader(in), hid.DefaultReportDefs)
	longest := 0
	for {
		frame, err := d.ReadFrame()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(frame) > longest {
			longest = len(frame)
		}
	}
	if longest == 0 {
		t.Skip("trace file is empty")
	}
	// RetDevAuthenticationInfo is a 251-byte frame; the head unit drops 4 bytes of it, but
	// its reports must still come out as one frame rather than as separate pieces
	if longest < 240 {
		t.Fatalf("longest frame %d bytes: RetDevAuthenticationInfo was not assembled", longest)
	}
}
