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

// DeviceArtwork is optional for a DeviceExtRemote: album art for the head unit. Without it
// there are no artwork formats and no track has a picture.
type DeviceArtwork interface {
	ArtworkFormats() []ArtworkFormat
	// TrackArtwork returns the picture of track index in format formatID, or nil.
	TrackArtwork(index int32, formatID uint16) *ArtworkImage
}

// ArtworkImage is a picture in one of the ArtworkFormats: Height rows of RowSize bytes.
type ArtworkImage struct {
	Format  ArtworkFormat
	RowSize uint32
	Pixels  []byte
}

// ArtworkChunk is the most picture data per RetTrackArtworkData packet: with the header the
// packet stays in the small (1-byte length) format.
var ArtworkChunk = 240

func artworkFormats(dev DeviceExtRemote) []ArtworkFormat {
	if a, ok := dev.(DeviceArtwork); ok {
		return a.ArtworkFormats()
	}
	return nil
}

func trackArtwork(dev DeviceExtRemote, index int32, formatID uint16) *ArtworkImage {
	if a, ok := dev.(DeviceArtwork); ok {
		return a.TrackArtwork(index, formatID)
	}
	return nil
}

// trackArtworkCounts: the formats track index has a picture in, one each.
func trackArtworkCounts(dev DeviceExtRemote, index int32) []TrackArtworkCount {
	var counts []TrackArtworkCount
	for _, f := range artworkFormats(dev) {
		if trackArtwork(dev, index, f.FormatID) != nil {
			counts = append(counts, TrackArtworkCount{FormatID: f.FormatID, Count: 1})
		}
	}
	return counts
}

// sendArtwork sends img as RetTrackArtworkData packets, all with req's transaction.
func sendArtwork(req *ipod.Command, tr ipod.CommandWriter, img *ArtworkImage) {
	data := img.Pixels
	for i := 0; i == 0 || len(data) > 0; i++ {
		n := ArtworkChunk
		if i == 0 {
			// room for the header, and whole pixels in every packet
			n = (n - (artworkHeaderLen - 2)) &^ 1
		}
		if n > len(data) {
			n = len(data)
		}
		pkt := &RetTrackArtworkData{PacketIndex: uint16(i), Data: data[:n]}
		if i == 0 {
			pkt.PixelFormat = img.Format.PixelFormat
			pkt.ImageWidth, pkt.ImageHeight = img.Format.ImageWidth, img.Format.ImageHeight
			pkt.BottomRightX, pkt.BottomRightY = img.Format.ImageWidth-1, img.Format.ImageHeight-1
			pkt.RowSize = img.RowSize
		}
		ipod.Respond(req, tr, pkt)
		data = data[n:]
	}
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
			var caps uint32
			if len(trackArtworkCounts(dev, msg.TrackIndex)) > 0 {
				caps |= TrackCapsHasArtwork
			}
			info = &TrackCaps{
				Caps:         caps,
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
			if counts := trackArtworkCounts(dev, msg.TrackIndex); len(counts) > 0 {
				info = counts
			} else {
				info = struct{}{}
			}
		default:
			info = []byte{0x00}

		}
		ipod.Respond(req, tr, &ReturnIndexedPlayingTrackInfo{
			InfoType: msg.InfoType,
			Info:     info,
		})
	case *GetArtworkFormats:
		ipod.Respond(req, tr, &RetArtworkFormats{Formats: artworkFormats(dev)})
	case *GetTrackArtworkData:
		if img := trackArtwork(dev, msg.TrackIndex, msg.FormatID); img != nil {
			sendArtwork(req, tr, img)
			break
		}
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
		times := &RetTrackArtworkTimes{}
		// one picture, at the start of the track
		if msg.ArtworkIndex == 0 && msg.ArtworkCount != 0 && trackArtwork(dev, msg.TrackIndex, msg.FormatID) != nil {
			times.Times = []uint32{0}
		}
		ipod.Respond(req, tr, times)
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
