package room

import (
	"strings"
	"time"

	"github.com/pion/webrtc/v4"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/rtc"
	"github.com/KeshavGoyal26/tessera/pkg/rtc/types"
	"github.com/KeshavGoyal26/tessera/pkg/stats"
)

// Snapshot implements stats.Provider. It takes only control-plane locks.
func (m *manager) Snapshot() *stats.Snapshot {
	now := time.Now()
	s := &stats.Snapshot{Time: now, Node: m.sampler.Sample(now)}
	if c, ok := m.engine.(rtc.PeerConnectionCounter); ok {
		s.Node.PeerConnections = c.PeerConnections()
	}
	m.mu.RLock()
	rooms := make([]*Room, 0, len(m.rooms))
	for _, r := range m.rooms {
		rooms = append(rooms, r)
	}
	m.mu.RUnlock()
	for _, r := range rooms {
		info := r.ToProto()
		rs := stats.RoomStats{SID: info.Sid, Name: info.Name, CreatedAt: r.created, ActiveRecording: info.ActiveRecording,
			Participants: []stats.ParticipantStats{}, ActiveSpeakers: []stats.SpeakerStats{}}
		for _, p := range r.Participants() {
			ps := p.stats(now)
			s.Node.PublishedTracks += len(ps.Published)
			s.Node.SubscribedTracks += len(ps.Subscribed)
			rs.Participants = append(rs.Participants, ps)
		}
		ids := map[string]string{}
		for _, p := range rs.Participants {
			ids[p.SID] = p.Identity
		}
		for _, sp := range r.activeSpeakers() {
			rs.ActiveSpeakers = append(rs.ActiveSpeakers, stats.SpeakerStats{ParticipantSID: sp.ParticipantSid, Identity: ids[sp.ParticipantSid], Level: float64(sp.Level)})
		}
		s.Node.Participants += len(rs.Participants)
		s.Rooms = append(s.Rooms, rs)
	}
	s.Node.Rooms = len(s.Rooms)
	if s.Rooms == nil {
		s.Rooms = []stats.RoomStats{}
	}
	return s
}

func kindName(k webrtc.RTPCodecType) string {
	if k == webrtc.RTPCodecTypeAudio {
		return "audio"
	}
	return "video"
}

func (p *Participant) stats(now time.Time) stats.ParticipantStats {
	p.mu.Lock()
	ps := stats.ParticipantStats{
		SID: p.sid, Identity: p.identity, Name: p.name, JoinedAt: p.joinedAt,
		State: strings.ToLower(strings.TrimPrefix(p.state.String(), "STATE_")),
		Kind:  strings.ToLower(strings.TrimPrefix(p.kind.String(), "KIND_")), Hidden: p.perm.GetHidden(),
		ConnectionQuality: stats.QualityUnknown, Published: []stats.PublishedTrackStats{}, Subscribed: []stats.SubscribedTrackStats{},
	}
	if p.client != nil {
		ps.ClientSDK = strings.ToLower(strings.TrimPrefix(p.client.GetSdk().String(), "SDK_")) + " " + p.client.GetVersion()
	}
	pub, sub := p.pub, p.sub
	tracks := make([]*pubTrack, 0, len(p.tracks))
	for _, t := range p.tracks {
		tracks = append(tracks, t)
	}
	subs := make(map[string]*subscription, len(p.subs))
	for k, v := range p.subs {
		subs[k] = v
	}
	p.mu.Unlock()

	for _, t := range []rtc.Transport{pub, sub} {
		if t == nil {
			continue
		}
		ts := t.Stats()
		if ts.CandidateType != "" && ps.ICECandidateType == "" {
			ps.ICECandidateType = ts.CandidateType
			ps.ICESetupMs = ts.ICESetup.Milliseconds()
			ps.DTLSSetupMs = ts.DTLSSetup.Milliseconds()
		}
	}
	ps.RTTMs = float64(p.rttMs.Load())
	p.mu.Lock()
	if p.twcc != nil {
		ps.BandwidthEstimateBps = p.twcc.Est.Estimate()
	}
	p.mu.Unlock()
	for _, t := range tracks {
		ps.Published = append(ps.Published, trackStats(t, now))
	}
	var lossSum float64
	for sid, s := range subs {
		st := s.dt.Stats(now)
		ss := stats.SubscribedTrackStats{
			TrackSID: sid, PublisherSID: s.pub.sid, Kind: kindName(s.dt.Kind()), Codec: s.dt.Receiver().Codec().MimeType,
			CurrentSpatial: st.Current.Spatial, CurrentTemporal: st.Current.Temporal,
			TargetSpatial: st.Target.Spatial, TargetTemporal: st.Target.Temporal, MaxSpatial: st.MaxLayer.Spatial,
			BitrateBps: st.BitrateBps, PacketsSent: st.Packets, PacketsLost: st.PacketsLost, LossPercent: st.FractionLost,
			JitterMs: st.JitterMs, RTTMs: st.RTTMs, LayerSwitches: st.LayerSwitches, NACKs: st.NACKs, PLIs: st.PLIs,
			Retransmits: st.Retransmits, Muted: st.Muted, BandwidthEstimateBps: ps.BandwidthEstimateBps, Paused: s.dt.Kind() == webrtc.RTPCodecTypeVideo && !st.Target.IsValid() && !st.Muted,
		}
		if st.RTTMs > 0 {
			ps.RTTMs = st.RTTMs
		}
		lossSum += st.FractionLost
		ps.Subscribed = append(ps.Subscribed, ss)
	}
	ps.ConnectionQuality, ps.QualityScore = qualityFrom(ps, lossSum)
	return ps
}

func trackStats(t *pubTrack, now time.Time) stats.PublishedTrackStats {
	info := t.info
	ts := stats.PublishedTrackStats{
		SID: info.GetSid(), Kind: strings.ToLower(strings.TrimPrefix(info.GetType().String(), "TRACK_TYPE_")),
		Source: strings.ToLower(strings.TrimPrefix(info.GetSource().String(), "TRACK_SOURCE_")),
		Codec:  info.GetMimeType(), Muted: info.GetMuted(), Simulcast: info.GetSimulcast(),
		Subscribers: len(t.recv.DownTracks()), Layers: []stats.LayerStats{},
	}
	clock := float64(t.recv.Codec().ClockRate)
	if clock == 0 {
		clock = 90000
	}
	var expected, lost uint64
	for _, l := range t.recv.LayerStats(now) {
		ls := stats.LayerStats{Spatial: l.Spatial, Quality: qualityName(l.Spatial), RID: l.RID, SSRC: l.SSRC, BitrateBps: l.BitrateBps, Active: l.PPS > 0}
		for _, vl := range info.GetLayers() {
			if types.SpatialForQuality(vl.GetQuality()) == l.Spatial {
				ls.Width, ls.Height = vl.GetWidth(), vl.GetHeight()
			}
		}
		ts.Layers = append(ts.Layers, ls)
		ts.BitrateBps += l.BitrateBps
		ts.PacketsRecv += l.Buffer.PacketsReceived
		ts.PacketsLost += l.Buffer.PacketsLost
		expected += l.Buffer.Expected
		lost += l.Buffer.PacketsLost
		ts.NACKs += l.NACKs
		ts.PLIs += l.PLIs
		if j := l.Buffer.Jitter * 1000 / clock; j > ts.JitterMs {
			ts.JitterMs = j
		}
	}
	if expected > 0 {
		ts.LossPercent = float64(lost) * 100 / float64(expected)
	}
	if info.GetType() == tesserav1.TrackType_TRACK_TYPE_AUDIO {
		ts.Kind = "audio"
	}
	return ts
}

func qualityName(s int32) string {
	switch s {
	case 0:
		return "low"
	case 1:
		return "medium"
	}
	return "high"
}

// qualityFrom derives a 1..5 score from loss and RTT.
func qualityFrom(ps stats.ParticipantStats, subLoss float64) (string, float64) {
	loss := subLoss
	for _, t := range ps.Published {
		if t.LossPercent > loss {
			loss = t.LossPercent
		}
	}
	if len(ps.Published) == 0 && len(ps.Subscribed) == 0 {
		return stats.QualityUnknown, 0
	}
	score := 5.0 - loss/4 - ps.RTTMs/200
	switch {
	case score >= 4:
		return stats.QualityExcellent, score
	case score >= 3:
		return stats.QualityGood, score
	case score >= 1.5:
		return stats.QualityPoor, score
	}
	return stats.QualityLost, max(score, 1)
}
