package hid

import (
	"errors"
	"fmt"
)

// ReportDir is the report direction
type ReportDir uint8

const (
	// Device to host (ipod->accessory)
	ReportDirAccIn ReportDir = 0
	// Host to device (accessory->ipod)
	ReportDirAccOut ReportDir = 1
)

// ReportDef represents a hid report type from the descriptor
type ReportDef struct {
	// id
	ID int
	// lenth of the report not including the id.
	Len int
	// direction
	Dir ReportDir
}

// MaxPayload returns the maximum payload that a report can fit
func (def ReportDef) MaxPayload() int {
	return def.Len - 1
}

// ReportDefs is a collection of ReportDef
type ReportDefs []ReportDef

// DefaultReportDefs is a default set of report types
// for use with ipod-gadget
var DefaultReportDefs = ReportDefs{
	ReportDef{ID: 0x01, Len: 12, Dir: ReportDirAccIn},
	ReportDef{ID: 0x02, Len: 14, Dir: ReportDirAccIn},
	ReportDef{ID: 0x03, Len: 20, Dir: ReportDirAccIn},
	ReportDef{ID: 0x04, Len: 63, Dir: ReportDirAccIn},

	// Output reports in ipod-gadget's descriptor: Pick() must never send these
	ReportDef{ID: 0x05, Len: 8, Dir: ReportDirAccOut},
	ReportDef{ID: 0x06, Len: 10, Dir: ReportDirAccOut},
	ReportDef{ID: 0x07, Len: 14, Dir: ReportDirAccOut},
	ReportDef{ID: 0x08, Len: 20, Dir: ReportDirAccOut},
	ReportDef{ID: 0x09, Len: 63, Dir: ReportDirAccOut},
}

// BigOutReportDefs is DefaultReportDefs plus one 255-byte Output report 0x0A, for
// ipod-gadget's hid_desc=bigout: a long frame from the accessory fits in one report,
// using only 1-byte Report Count items. Pick() never sends it (accessory -> iPod only).
var BigOutReportDefs = append(append(ReportDefs{}, DefaultReportDefs...),
	ReportDef{ID: 0x0A, Len: 255, Dir: ReportDirAccOut},
)

var LegacyReportDefs = ReportDefs{
	ReportDef{ID: 0x01, Len: 5, Dir: ReportDirAccIn},
	ReportDef{ID: 0x02, Len: 9, Dir: ReportDirAccIn},
	ReportDef{ID: 0x03, Len: 13, Dir: ReportDirAccIn},
	ReportDef{ID: 0x04, Len: 17, Dir: ReportDirAccIn},
	ReportDef{ID: 0x05, Len: 25, Dir: ReportDirAccIn},
	ReportDef{ID: 0x06, Len: 49, Dir: ReportDirAccIn},
	ReportDef{ID: 0x07, Len: 95, Dir: ReportDirAccIn},
	ReportDef{ID: 0x08, Len: 193, Dir: ReportDirAccIn},
	ReportDef{ID: 0x09, Len: 257, Dir: ReportDirAccIn},
	ReportDef{ID: 0x0A, Len: 385, Dir: ReportDirAccIn},
	ReportDef{ID: 0x0B, Len: 513, Dir: ReportDirAccIn},
	ReportDef{ID: 0x0C, Len: 767, Dir: ReportDirAccIn},

	ReportDef{ID: 0x0D, Len: 5, Dir: ReportDirAccOut},
	ReportDef{ID: 0x0E, Len: 9, Dir: ReportDirAccOut},
	ReportDef{ID: 0x0F, Len: 13, Dir: ReportDirAccOut},
	ReportDef{ID: 0x10, Len: 17, Dir: ReportDirAccOut},
	ReportDef{ID: 0x11, Len: 25, Dir: ReportDirAccOut},
	ReportDef{ID: 0x12, Len: 49, Dir: ReportDirAccOut},
	ReportDef{ID: 0x13, Len: 95, Dir: ReportDirAccOut},
	ReportDef{ID: 0x14, Len: 193, Dir: ReportDirAccOut},
	ReportDef{ID: 0x15, Len: 255, Dir: ReportDirAccOut},
}

// Pick finds the best matching report type
func (defs ReportDefs) Pick(payloadSize int, dir ReportDir) (ReportDef, error) {
	var def *ReportDef

	for i := range defs {
		if defs[i].Dir == dir {
			def = &defs[i]
			if defs[i].MaxPayload() >= payloadSize {
				break
			}
		}
	}

	if def == nil {
		return ReportDef{}, errors.New("no matching report found")
	} else {
		return *def, nil
	}
}

// Find finds the report type based on id
func (defs ReportDefs) Find(id int) (ReportDef, error) {
	for i := range defs {
		if defs[i].ID == id {
			return defs[i], nil
		}
	}
	return ReportDef{}, fmt.Errorf("report id no found: %#v", id)
}
