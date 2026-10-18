package extremote

import (
	"github.com/oandrew/ipod"
)

// DeviceExtRemote is the player the Extended Remote lingo controls and reports on.
// With dev == nil HandleExtRemote answers with fixed placeholders.
type DeviceExtRemote interface {
	PlaybackStatus() (trackLength, trackPos uint32, state PlayerState)
	// CurrentTrack returns the playing track's index in the playing list and the list's length.
	CurrentTrack() (index int32, numTracks uint32)
	PlayControl(cmd PlayControlCmd)
	// SetCurrentTrack moves to track index (-1: the first one) at position 0, and starts
	// playing if play is set.
	SetCurrentTrack(index int32, play bool)
	// SetPlayStatusNotifications sets which PlayStatusChangeNotifications the accessory wants.
	SetPlayStatusNotifications(enabled PlayStatusChanges)
}

// DeviceTrackInfo is optional for a DeviceExtRemote: the names of the playing list's tracks,
// e.g. from the phone over Bluetooth AVRCP. Without it the answers are "title", "artist" and
// "album".
type DeviceTrackInfo interface {
	TrackInfo(index int32) (title, artist, album string)
}

func trackInfo(dev DeviceExtRemote, index int32) (title, artist, album string) {
	if ti, ok := dev.(DeviceTrackInfo); ok {
		return ti.TrackInfo(index)
	}
	return "title", "artist", "album"
}

func ackSuccess(req *ipod.Command) *ACK {
	return &ACK{Status: ACKStatusSuccess, CmdID: req.ID.CmdID()}
}

// func ackPending(req ipod.Packet, maxWait uint32) ACKPending {
// 	return ACKPending{Status: ACKStatusPending, CmdID: uint8(req.ID.CmdID()), MaxWait: maxWait}
// }

func HandleExtRemote(req *ipod.Command, tr ipod.CommandWriter, dev DeviceExtRemote) error {
	//log.Printf("Req: %#v", req)
	switch msg := req.Payload.(type) {

	case *GetCurrentPlayingTrackChapterInfo:
		ipod.Respond(req, tr, &ReturnCurrentPlayingTrackChapterInfo{
			CurrentChapterIndex: 0,
			ChapterCount:        1,
		})
	case *SetCurrentPlayingTrackChapter:
		ipod.Respond(req, tr, ackSuccess(req))
	case *GetCurrentPlayingTrackChapterPlayStatus:
		ipod.Respond(req, tr, &ReturnCurrentPlayingTrackChapterPlayStatus{
			ChapterPosition: 0,
			ChapterLength:   0,
		})
	case *GetCurrentPlayingTrackChapterName:
		ipod.Respond(req, tr, &ReturnCurrentPlayingTrackChapterName{
			ChapterName: ipod.StringToBytes("chapter"),
		})
	case *GetAudiobookSpeed:
		ipod.Respond(req, tr, &ReturnAudiobookSpeed{
			Speed: 0,
		})
	case *SetAudiobookSpeed:
		ipod.Respond(req, tr, ackSuccess(req))
	case *GetIndexedPlayingTrackInfo:
		var info interface{}
		switch msg.InfoType {
		case TrackInfoCaps:
			var trackLength uint32 = 300 * 1000
			if dev != nil {
				trackLength, _, _ = dev.PlaybackStatus()
			}
			info = &TrackCaps{
				Caps:         0x0,
				TrackLength:  trackLength,
				ChapterCount: 1,
			}
		case TrackInfoDescription, TrackInfoLyrics:
			info = &TrackLongText{
				Flags:       0x0,
				PacketIndex: 0,
				Text:        0x00,
			}
		case TrackInfoArtworkCount:
			info = struct{}{}
		default:
			info = []byte{0x00}

		}
		ipod.Respond(req, tr, &ReturnIndexedPlayingTrackInfo{
			InfoType: msg.InfoType,
			Info:     info,
		})
	case *GetArtworkFormats:
		ipod.Respond(req, tr, &RetArtworkFormats{})
	case *GetTrackArtworkData:
		ipod.Respond(req, tr, &ACK{
			Status: ACKStatusFailed,
			CmdID:  req.ID.CmdID(),
		})
	case *ResetDBSelection:
		ipod.Respond(req, tr, ackSuccess(req))
	case *SelectDBRecord:
		ipod.Respond(req, tr, ackSuccess(req))
	case *GetNumberCategorizedDBRecords:
		ipod.Respond(req, tr, &ReturnNumberCategorizedDBRecords{
			RecordCount: 1,
		})
	case *RetrieveCategorizedDatabaseRecords:
		ipod.Respond(req, tr, &ReturnCategorizedDatabaseRecord{})
	case *GetPlayStatus:
		status := &ReturnPlayStatus{
			TrackLength:   300 * 1000,
			TrackPosition: 20 * 1000,
			State:         PlayerStatePaused,
		}
		if dev != nil {
			status.TrackLength, status.TrackPosition, status.State = dev.PlaybackStatus()
		}
		ipod.Respond(req, tr, status)
	case *GetCurrentPlayingTrackIndex:
		var index int32
		if dev != nil {
			index, _ = dev.CurrentTrack()
		}
		ipod.Respond(req, tr, &ReturnCurrentPlayingTrackIndex{
			TrackIndex: index,
		})
	case *GetIndexedPlayingTrackTitle:
		title, _, _ := trackInfo(dev, msg.TrackIndex)
		ipod.Respond(req, tr, &ReturnIndexedPlayingTrackTitle{
			Title: ipod.StringToBytes(title),
		})
	case *GetIndexedPlayingTrackArtistName:
		_, artist, _ := trackInfo(dev, msg.TrackIndex)
		ipod.Respond(req, tr, &ReturnIndexedPlayingTrackArtistName{
			ArtistName: ipod.StringToBytes(artist),
		})
	case *GetIndexedPlayingTrackAlbumName:
		_, _, album := trackInfo(dev, msg.TrackIndex)
		ipod.Respond(req, tr, &ReturnIndexedPlayingTrackAlbumName{
			AlbumName: ipod.StringToBytes(album),
		})
	case *SetPlayStatusChangeNotification:
		if dev != nil {
			dev.SetPlayStatusNotifications(PlayStatusChangesFromMask(msg.EventMask))
		}
		ipod.Respond(req, tr, ackSuccess(req))
	case *SetPlayStatusChangeNotificationShort:
		if dev != nil {
			var enabled PlayStatusChanges
			if msg.Enabled {
				enabled = PlayStatusChangesShort
			}
			dev.SetPlayStatusNotifications(enabled)
		}
		ipod.Respond(req, tr, ackSuccess(req))
	case *PlayCurrentSelection:
		if dev != nil {
			dev.SetCurrentTrack(msg.SelectedTrackIndex, true)
		}
		ipod.Respond(req, tr, ackSuccess(req))
	case *PlayControl:
		if dev != nil {
			dev.PlayControl(msg.Cmd)
		}
		ipod.Respond(req, tr, ackSuccess(req))
	case *GetTrackArtworkTimes:
		ipod.Respond(req, tr, &RetTrackArtworkTimes{})
	case *GetShuffle:
		ipod.Respond(req, tr, &ReturnShuffle{Mode: ShuffleOff})
	case *SetShuffle:
		ipod.Respond(req, tr, ackSuccess(req))

	case *GetRepeat:
		ipod.Respond(req, tr, &ReturnRepeat{Mode: RepeatOff})
	case *SetRepeat:
		ipod.Respond(req, tr, ackSuccess(req))

	case *SetDisplayImage:
		ipod.Respond(req, tr, ackSuccess(req))
	case *GetMonoDisplayImageLimits:
		ipod.Respond(req, tr, &ReturnMonoDisplayImageLimits{
			MaxWidth:    640,
			MaxHeight:   960,
			PixelFormat: 0x01,
		})
	case *GetNumPlayingTracks:
		var numTracks uint32 = 1
		if dev != nil {
			_, numTracks = dev.CurrentTrack()
		}
		ipod.Respond(req, tr, &ReturnNumPlayingTracks{
			NumTracks: numTracks,
		})
	case *SetCurrentPlayingTrack:
		if dev != nil {
			dev.SetCurrentTrack(msg.TrackIndex, false)
		}
		ipod.Respond(req, tr, ackSuccess(req))
	case *SelectSortDBRecord:
	case *GetColorDisplayImageLimits:
		ipod.Respond(req, tr, &ReturnColorDisplayImageLimits{
			MaxWidth:    640,
			MaxHeight:   960,
			PixelFormat: 0x01,
		})
	case *ResetDBSelectionHierarchy:
		ipod.Respond(req, tr, &ACK{Status: ACKStatusFailed, CmdID: req.ID.CmdID()})

	case *GetDBiTunesInfo:
	// RetDBiTunesInfo:
	case *GetUIDTrackInfo:
	// RetUIDTrackInfo:
	case *GetDBTrackInfo:
	// RetDBTrackInfo:
	case *GetPBTrackInfo:
	// RetPBTrackInfo:

	default:
		_ = msg
	}
	return nil
}
