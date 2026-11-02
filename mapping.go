package main

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"
	"time"
)

// Namespace for the plugin's own UUIDv5 ids. These ids exist only inside this
// plugin's Jellyfin-shaped answers; they are stable across restarts so Tracearr
// sees the same user and item ids every time.
var idNamespace = [16]byte{0x7d, 0x3a, 0x4e, 0x21, 0x5b, 0x9c, 0x4f, 0x6a, 0x8e, 0x10, 0x2c, 0x77, 0x41, 0xb5, 0xd3, 0x09}

// jfID returns a Jellyfin-style id (32 hex characters, no dashes) derived from
// name with UUIDv5.
func jfID(name string) string {
	h := sha1.New()
	h.Write(idNamespace[:])
	h.Write([]byte(name))
	sum := h.Sum(nil)[:16]
	sum[6] = (sum[6] & 0x0f) | 0x50
	sum[8] = (sum[8] & 0x3f) | 0x80
	return hex.EncodeToString(sum)
}

func userJFID(siloUserID string) string { return jfID("user:" + siloUserID) }
func itemJFID(siloItemID string) string { return jfID("item:" + siloItemID) }

// siloUser is the subset of /api/v2/admin/users the plugin reads.
type siloUser struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at"`
}

// siloSession is the subset of /api/v2/admin/sessions the plugin reads.
type siloSession struct {
	SessionID           string  `json:"session_id"`
	UserID              string  `json:"user_id"`
	Username            string  `json:"username"`
	ProfileName         string  `json:"profile_name"`
	MediaFileID         string  `json:"media_file_id"`
	ContentID           string  `json:"content_id"`
	MediaTitle          string  `json:"media_title"`
	MediaType           string  `json:"media_type"`
	SeriesName          string  `json:"series_name"`
	EpisodeName         string  `json:"episode_name"`
	SeasonNumber        *int    `json:"season_number"`
	EpisodeNumber       *int    `json:"episode_number"`
	PosterURL           string  `json:"poster_url"`
	PlayMethod          string  `json:"play_method"`
	EffectivePlayMethod string  `json:"effective_play_method"`
	FileDuration        *int    `json:"file_duration"`
	StartedAt           string  `json:"started_at"`
	UpdatedAt           string  `json:"updated_at"`
	PositionSeconds     float64 `json:"position_seconds"`
	IsPaused            bool    `json:"is_paused"`
	ClientIP            string  `json:"client_ip"`
	ClientName          string  `json:"client_name"`
	ClientVersion       string  `json:"client_version"`
	DeviceID            string  `json:"device_id"`
	DevicePlatform      string  `json:"device_platform"`
	TranscodeAudio      bool    `json:"transcode_audio"`
	StreamBitrateKbps   *int    `json:"stream_bitrate_kbps"`
	TargetVideoCodec    string  `json:"target_video_codec"`
	TargetAudioCodec    string  `json:"target_audio_codec"`
	SourceBitrateKbps   *int    `json:"source_bitrate_kbps"`
	SourceVideoCodec    string  `json:"source_video_codec"`
	SourceVideoHeight   int     `json:"source_video_height"`
	SourceAudioCodec    string  `json:"source_audio_codec"`
	SourceAudioChannels *int    `json:"source_audio_channels"`
	SourceAudioLanguage string  `json:"source_audio_language"`
	VideoDecision       string  `json:"video_decision"`
	AudioDecision       string  `json:"audio_decision"`
}

type obj = map[string]any

const ticksPerSecond = 10_000_000

// mapUser builds a Jellyfin user. admin forces IsAdministrator, used for the
// /Users/Me answer so Tracearr's admin check passes.
func mapUser(u siloUser, admin bool) obj {
	return obj{
		"Id":                    userJFID(u.ID),
		"Name":                  u.Username,
		"ServerId":              serverID,
		"HasPassword":           true,
		"HasConfiguredPassword": true,
		"LastLoginDate":         nilIfEmpty(u.CreatedAt),
		"Policy": obj{
			"IsAdministrator":  admin || u.Role == "admin",
			"IsDisabled":       !u.Enabled,
			"IsHidden":         false,
			"EnableAllFolders": true,
			"EnabledFolders":   []string{},
		},
	}
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// playMethod converts Silo's whole-session method to Jellyfin's.
func playMethod(s siloSession) string {
	m := s.EffectivePlayMethod
	if m == "" {
		m = s.PlayMethod
	}
	switch strings.ToLower(m) {
	case "direct", "directplay", "direct_play":
		return "DirectPlay"
	case "remux", "direct_stream", "directstream", "audio":
		return "DirectStream"
	case "transcode", "video":
		return "Transcode"
	}
	return "DirectPlay"
}

func itemType(mediaType string) string {
	switch strings.ToLower(mediaType) {
	case "episode", "tv", "show":
		return "Episode"
	case "audio", "track", "music":
		return "Audio"
	default:
		return "Movie"
	}
}

func ticks(seconds float64) int64 { return int64(seconds * ticksPerSecond) }

// mapSession builds a Jellyfin session for one live Silo playback. Silo's
// admin sessions list only active playback, so every entry has a
// NowPlayingItem. The second return is the item id with its poster path so the
// image route can serve artwork.
func mapSession(s siloSession) (obj, string, string) {
	itemSrc := s.MediaFileID
	if itemSrc == "" {
		itemSrc = s.ContentID
	}
	itemID := itemJFID(itemSrc)
	typ := itemType(s.MediaType)
	method := playMethod(s)

	name := s.MediaTitle
	if typ == "Episode" && s.EpisodeName != "" {
		name = s.EpisodeName
	}

	var streams []obj
	if s.SourceVideoCodec != "" || s.SourceVideoHeight > 0 {
		v := obj{"Type": "Video", "Codec": s.SourceVideoCodec, "Height": s.SourceVideoHeight, "Index": 0}
		if s.SourceBitrateKbps != nil {
			v["BitRate"] = *s.SourceBitrateKbps * 1000
		}
		streams = append(streams, v)
	}
	if s.SourceAudioCodec != "" {
		a := obj{"Type": "Audio", "Codec": s.SourceAudioCodec, "Index": 1, "Language": s.SourceAudioLanguage}
		if s.SourceAudioChannels != nil {
			a["Channels"] = *s.SourceAudioChannels
		}
		streams = append(streams, a)
	}

	item := obj{
		"Id":           itemID,
		"Name":         name,
		"Type":         typ,
		"MediaStreams": streams,
	}
	if s.FileDuration != nil {
		item["RunTimeTicks"] = ticks(float64(*s.FileDuration))
	}
	if typ == "Episode" {
		item["SeriesName"] = s.SeriesName
		item["SeriesId"] = itemJFID("series:" + s.SeriesName)
		if s.SeasonNumber != nil {
			item["ParentIndexNumber"] = *s.SeasonNumber
		}
		if s.EpisodeNumber != nil {
			item["IndexNumber"] = *s.EpisodeNumber
		}
	}
	if s.PosterURL != "" {
		item["ImageTags"] = obj{"Primary": "silo"}
	}

	playState := obj{
		"PositionTicks": ticks(s.PositionSeconds),
		"IsPaused":      s.IsPaused,
		"PlayMethod":    method,
	}

	client := s.ClientName
	if client == "" {
		client = "Silo"
	}
	device := s.DevicePlatform
	if device == "" {
		device = client
	}
	deviceID := s.DeviceID
	if deviceID == "" {
		deviceID = s.SessionID
	}

	remote := s.ClientIP
	out := obj{
		"Id":                    s.SessionID,
		"UserId":                userJFID(s.UserID),
		"UserName":              s.Username,
		"Client":                client,
		"ApplicationVersion":    s.ClientVersion,
		"DeviceName":            device,
		"DeviceId":              deviceID,
		"RemoteEndPoint":        remote,
		"IsActive":              true,
		"SupportsRemoteControl": false,
		"NowPlayingItem":        item,
		"PlayState":             playState,
		"LastActivityDate":      nilIfEmpty(s.UpdatedAt),
	}

	if method != "DirectPlay" {
		videoDirect := method == "DirectStream"
		if method == "Transcode" {
			vd := strings.ToLower(s.VideoDecision)
			videoDirect = vd == "copy" || vd == "direct" || vd == "directplay"
		}
		audioDirect := method == "DirectStream" && !s.TranscodeAudio
		if method == "Transcode" {
			ad := strings.ToLower(s.AudioDecision)
			audioDirect = ad == "copy" || ad == "direct" || ad == "directplay"
		}
		if method == "DirectStream" && s.EffectivePlayMethod == "remux" {
			audioDirect = true
		}
		ti := obj{
			"IsVideoDirect": videoDirect,
			"IsAudioDirect": audioDirect,
			"VideoCodec":    s.TargetVideoCodec,
			"AudioCodec":    s.TargetAudioCodec,
		}
		if s.StreamBitrateKbps != nil {
			ti["Bitrate"] = *s.StreamBitrateKbps * 1000
		}
		out["TranscodingInfo"] = ti
	}

	return out, itemID, s.PosterURL
}

// nowUTC is a variable so tests can pin the clock.
var nowUTC = func() time.Time { return time.Now().UTC() }
