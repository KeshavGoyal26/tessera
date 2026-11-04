package hls

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// PlaylistSegment is one media playlist entry.
type PlaylistSegment struct {
	URI             string
	Duration        float64
	ProgramDateTime time.Time
	Discontinuity   bool
	// Map is the init segment URI to emit before this segment (EXT-X-MAP).
	Map string
}

// MediaPlaylist encodes an HLS media playlist (RFC 8216).
type MediaPlaylist struct {
	Segments []PlaylistSegment
	// Live: EXT-X-PLAYLIST-TYPE:EVENT without ENDLIST. Otherwise VOD + ENDLIST.
	Live bool
	// TargetMin is a lower bound for EXT-X-TARGETDURATION.
	TargetMin int
}

// Encode renders the playlist.
func (p *MediaPlaylist) Encode() []byte {
	target := p.TargetMin
	for _, s := range p.Segments {
		if v := int(math.Ceil(s.Duration - 1e-9)); v > target {
			target = v
		}
	}
	if target < 1 {
		target = 1
	}
	var b bytes.Buffer
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:0\n", target)
	if p.Live {
		b.WriteString("#EXT-X-PLAYLIST-TYPE:EVENT\n")
	} else {
		b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	}
	b.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n")
	for i, s := range p.Segments {
		if s.Discontinuity && i > 0 {
			b.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		if s.Map != "" {
			fmt.Fprintf(&b, "#EXT-X-MAP:URI=%q\n", s.Map)
		}
		if !s.ProgramDateTime.IsZero() {
			fmt.Fprintf(&b, "#EXT-X-PROGRAM-DATE-TIME:%s\n", s.ProgramDateTime.UTC().Format("2006-01-02T15:04:05.000Z"))
		}
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n%s\n", s.Duration, s.URI)
	}
	if !p.Live {
		b.WriteString("#EXT-X-ENDLIST\n")
	}
	return b.Bytes()
}

// MasterPlaylist encodes a master playlist with demuxed audio (EXT-X-MEDIA).
// Video renditions reference the audio group; audio-only recordings are
// emitted as plain variants.
func MasterPlaylist(renditions []Rendition) ([]byte, error) {
	if len(renditions) == 0 {
		return nil, errors.New("hls: no renditions")
	}
	var audio, video []Rendition
	for _, r := range renditions {
		if r.IsAudio {
			audio = append(audio, r)
		} else {
			video = append(video, r)
		}
	}
	sort.SliceStable(video, func(i, j int) bool { return video[i].Bandwidth > video[j].Bandwidth })
	var b bytes.Buffer
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-INDEPENDENT-SEGMENTS\n")
	group := "audio"
	if len(audio) > 0 && audio[0].AudioGroup != "" {
		group = audio[0].AudioGroup
	}
	if len(video) == 0 {
		for _, a := range audio {
			fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,CODECS=%q\n%s\n", a.Bandwidth, a.Codecs, a.URI)
		}
		return b.Bytes(), nil
	}
	for i, a := range audio {
		name := a.Name
		if name == "" {
			name = fmt.Sprintf("audio%d", i+1)
		}
		def := "NO"
		if i == 0 {
			def = "YES"
		}
		fmt.Fprintf(&b, "#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=%q,NAME=%q,DEFAULT=%s,AUTOSELECT=YES,URI=%q\n", group, name, def, a.URI)
	}
	for _, v := range video {
		codecs := v.Codecs
		bw := v.Bandwidth
		attrs := ""
		if len(audio) > 0 {
			codecs = strings.Join([]string{v.Codecs, audio[0].Codecs}, ",")
			bw += audio[0].Bandwidth
			attrs = fmt.Sprintf(",AUDIO=%q", group)
		}
		res := ""
		if v.Width > 0 && v.Height > 0 {
			res = fmt.Sprintf(",RESOLUTION=%dx%d", v.Width, v.Height)
		}
		fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,CODECS=%q%s%s\n%s\n", bw, codecs, res, attrs, v.URI)
	}
	return b.Bytes(), nil
}
