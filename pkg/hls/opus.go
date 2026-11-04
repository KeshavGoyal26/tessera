package hls

import "time"

// OpusFrameSamples returns the duration of an Opus packet in 48 kHz samples
// from its TOC byte (RFC 6716 section 3.1), or 0 if malformed.
func OpusFrameSamples(p []byte) uint32 {
	if len(p) == 0 {
		return 0
	}
	toc := p[0]
	var frame uint32
	switch cfg := toc >> 3; {
	case cfg < 12: // SILK: 10, 20, 40, 60 ms
		frame = [4]uint32{480, 960, 1920, 2880}[cfg&3]
	case cfg < 16: // hybrid: 10, 20 ms
		frame = [2]uint32{480, 960}[cfg&1]
	default: // CELT: 2.5, 5, 10, 20 ms
		frame = [4]uint32{120, 240, 480, 960}[cfg&3]
	}
	switch toc & 3 {
	case 0:
		return frame
	case 1, 2:
		return frame * 2
	default:
		if len(p) < 2 {
			return 0
		}
		return frame * uint32(p[1]&0x3f)
	}
}

// WallClock maps extended RTP timestamps to wall-clock time using RTCP
// sender reports (NTP/RTP pairs). Before the first report it falls back to
// the arrival time of the first packet.
type WallClock struct {
	Rate uint32

	haveSR   bool
	srRTP    int64
	srWall   time.Time
	firstRTP int64
	firstAt  time.Time
	haveArr  bool
}

// ObserveArrival records the arrival time of a packet; only the first matters.
func (c *WallClock) ObserveArrival(rtpExt int64, at time.Time) {
	if !c.haveArr {
		c.haveArr, c.firstRTP, c.firstAt = true, rtpExt, at
	}
}

// ObserveSR records a sender report. ntp is the 64-bit NTP timestamp.
func (c *WallClock) ObserveSR(ntp uint64, rtpExt int64) {
	sec := int64(ntp >> 32)
	frac := int64(ntp & 0xffffffff)
	if sec == 0 {
		return
	}
	const ntpToUnix = 2208988800
	c.srWall = time.Unix(sec-ntpToUnix, frac*1e9>>32)
	c.srRTP = rtpExt
	c.haveSR = true
}

// HaveSR reports whether wall times derive from RTCP.
func (c *WallClock) HaveSR() bool { return c.haveSR }

// At returns the wall-clock time of an extended RTP timestamp.
func (c *WallClock) At(rtpExt int64) time.Time {
	rate := int64(c.Rate)
	if rate == 0 {
		rate = 90000
	}
	if c.haveSR {
		return c.srWall.Add(time.Duration((rtpExt - c.srRTP) * int64(time.Second) / rate))
	}
	return c.firstAt.Add(time.Duration((rtpExt - c.firstRTP) * int64(time.Second) / rate))
}
