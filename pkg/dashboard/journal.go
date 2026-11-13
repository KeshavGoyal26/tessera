package dashboard

import (
	"cmp"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/KeshavGoyal26/tessera/pkg/stats"
)

// The session journal: one entry per participant connection (sid) seen on this node, with a
// timeline of events and the share of time spent in each connection quality. It is derived
// from the samples (Observe), so nothing on the media or room path changes; other sources can
// add events with Record (for example leave reasons from the room package, later).

// Event kinds recorded by Observe. Record accepts any kind: the dashboard shows unknown kinds
// as they are.
const (
	EvJoined      = "joined"
	EvQuality     = "quality"
	EvICE         = "ice"
	EvPublished   = "published"
	EvUnpublished = "unpublished"
	EvLeft        = "left"
)

const (
	journalEnded  = 5000 // ended sessions kept (newest)
	journalEvents = 200  // events kept per session (newest)
)

// SessionEvent is one entry of a session's timeline.
type SessionEvent struct {
	T      time.Time `json:"t"`
	Kind   string    `json:"kind"`
	Detail string    `json:"detail"`
}

// Share is the fraction of a session's time (0..1) spent in each connection quality.
type Share struct {
	Excellent float64 `json:"excellent"`
	Good      float64 `json:"good"`
	Poor      float64 `json:"poor"`
	Lost      float64 `json:"lost"`
	Unknown   float64 `json:"unknown"`
}

// SessionBrief is a row of the sessions list.
type SessionBrief struct {
	SID            string     `json:"sid"`
	Identity       string     `json:"identity"`
	Name           string     `json:"name,omitempty"`
	Room           string     `json:"room"`
	Role           string     `json:"role"`
	State          string     `json:"state"` // active, ended
	StartedAt      time.Time  `json:"started_at"`
	EndedAt        *time.Time `json:"ended_at,omitempty"`
	DurationMs     int64      `json:"duration_ms"`
	Quality        string     `json:"quality"`
	Share          Share      `json:"share"`
	MaxRTTMs       float64    `json:"max_rtt_ms"`
	MaxLossPercent float64    `json:"max_loss_percent"`
	ICEType        string     `json:"ice_type"`
	ClientSDK      string     `json:"client_sdk,omitempty"`
	Events         int        `json:"events"`
}

type session0 struct {
	brief   SessionBrief
	seen    time.Time                // last sample it was in
	timeIn  map[string]time.Duration // per quality
	tracks  map[string]string        // published track sid -> kind
	events  []SessionEvent
	dropped int // events beyond journalEvents
}

// Journal keeps the sessions of this node.
type Journal struct {
	mu     sync.Mutex
	active map[string]*session0
	ended  []*session0 // oldest first, at most journalEnded
}

func newJournal() *Journal { return &Journal{active: map[string]*session0{}} }

// Record adds an event to a session (active or recently ended); unknown sids are ignored.
func (j *Journal) Record(sid string, e SessionEvent) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if s := j.find(sid); s != nil {
		s.add(e)
	}
}

func (j *Journal) find(sid string) *session0 {
	if s := j.active[sid]; s != nil {
		return s
	}
	for i := len(j.ended) - 1; i >= 0; i-- {
		if j.ended[i].brief.SID == sid {
			return j.ended[i]
		}
	}
	return nil
}

func (s *session0) add(e SessionEvent) {
	s.events = append(s.events, e)
	if len(s.events) > journalEvents {
		s.events = s.events[1:]
		s.dropped++
	}
	s.brief.Events++
}

// Observe updates the journal from one sample: joins, leaves, quality and ICE changes,
// published tracks, time per quality and the worst RTT and loss. It returns the sessions that
// ended in this sample (for the cluster's session store).
func (j *Journal) Observe(s *stats.Snapshot, now time.Time) (ended []SessionDetail) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for ri := range s.Rooms {
		r := &s.Rooms[ri]
		for pi := range r.Participants {
			p := &r.Participants[pi]
			b := briefOf(r.Name, p)
			if !b.connectedHere() {
				continue
			}
			ses := j.active[p.SID]
			q := b.Quality
			if ses == nil {
				ses = &session0{brief: SessionBrief{SID: p.SID, Identity: p.Identity, Name: p.Name, Room: r.Name, Role: b.Role,
					State: "active", StartedAt: cmp.Or(p.JoinedAt, now), Quality: q, ICEType: p.ICECandidateType, ClientSDK: p.ClientSDK},
					seen: now, timeIn: map[string]time.Duration{}, tracks: map[string]string{}}
				j.active[p.SID] = ses
				ses.add(SessionEvent{T: ses.brief.StartedAt, Kind: EvJoined, Detail: fmt.Sprintf("%s in %s", b.Role, r.Name)})
			} else {
				ses.timeIn[ses.brief.Quality] += now.Sub(ses.seen)
				if q != ses.brief.Quality {
					ses.add(SessionEvent{T: now, Kind: EvQuality, Detail: ses.brief.Quality + " → " + q})
				}
			}
			if t := p.ICECandidateType; t != "" && t != ses.brief.ICEType {
				ses.add(SessionEvent{T: now, Kind: EvICE, Detail: cmp.Or(ses.brief.ICEType, "none") + " → " + t})
			}
			for _, t := range p.Published {
				if _, ok := ses.tracks[t.SID]; !ok {
					ses.tracks[t.SID] = t.Kind
					ses.add(SessionEvent{T: now, Kind: EvPublished, Detail: t.Kind + " " + t.SID})
				}
			}
			if len(ses.tracks) > len(p.Published) {
				for sid, kind := range ses.tracks {
					if !slices.ContainsFunc(p.Published, func(t stats.PublishedTrackStats) bool { return t.SID == sid }) {
						delete(ses.tracks, sid)
						ses.add(SessionEvent{T: now, Kind: EvUnpublished, Detail: kind + " " + sid})
					}
				}
			}
			ses.brief.Quality, ses.brief.Role, ses.brief.ICEType = q, b.Role, cmp.Or(p.ICECandidateType, ses.brief.ICEType)
			ses.brief.MaxRTTMs = max(ses.brief.MaxRTTMs, p.RTTMs)
			ses.brief.MaxLossPercent = max(ses.brief.MaxLossPercent, b.LossPercent)
			ses.seen = now
		}
	}
	for sid, ses := range j.active {
		if ses.seen != now {
			end := ses.seen
			ses.brief.State, ses.brief.EndedAt = "ended", &end
			ses.add(SessionEvent{T: end, Kind: EvLeft, Detail: "left (" + ses.brief.Quality + ")"})
			delete(j.active, sid)
			ev := slices.Clone(ses.events)
			slices.Reverse(ev)
			ended = append(ended, SessionDetail{SessionBrief: ses.row(now), Events: ev})
			j.ended = append(j.ended, ses)
			if len(j.ended) > journalEnded {
				j.ended = j.ended[1:]
			}
		}
	}
	return ended
}

// snapshot copies the list rows (active, then ended newest first), finalising times as of now.
func (j *Journal) rows(now time.Time) []SessionBrief {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]SessionBrief, 0, len(j.active)+len(j.ended))
	for _, s := range j.active {
		out = append(out, s.row(now))
	}
	for i := len(j.ended) - 1; i >= 0; i-- {
		out = append(out, j.ended[i].row(now))
	}
	return out
}

func (s *session0) row(now time.Time) SessionBrief {
	b := s.brief
	end := now
	if b.EndedAt != nil {
		end = *b.EndedAt
	}
	b.DurationMs = end.Sub(b.StartedAt).Milliseconds()
	var total time.Duration
	for _, d := range s.timeIn {
		total += d
	}
	if total > 0 {
		f := func(q string) float64 { return float64(s.timeIn[q]) / float64(total) }
		b.Share = Share{Excellent: f(stats.QualityExcellent), Good: f(stats.QualityGood), Poor: f(stats.QualityPoor), Lost: f(stats.QualityLost), Unknown: f(stats.QualityUnknown)}
	}
	return b
}

// detail is one session with its events (newest first).
func (j *Journal) detail(sid string, now time.Time) (SessionBrief, []SessionEvent, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := j.find(sid)
	if s == nil {
		return SessionBrief{}, nil, false
	}
	ev := slices.Clone(s.events)
	slices.Reverse(ev)
	return s.row(now), ev, true
}

// ---- queries ------------------------------------------------------------------

func (h *Handler) sessionsPage(v url.Values) any {
	p := pageOf(v)
	state, room, role, bad := v.Get("state"), v.Get("room"), v.Get("role"), v.Get("quality") == "poor"
	rows := h.journal.rows(h.p.Now())
	sort := cmp.Or(p.sort, "started")
	if p.sort == "" || (sort == "bad" && v.Get("desc") == "") {
		p.desc = true // newest first, worst first
	}
	total, items := page(p, rows, func(s *SessionBrief) bool {
		return (state == "" || s.State == state) && (room == "" || s.Room == room) && (role == "" || s.Role == role) &&
			(!bad || s.Share.Poor+s.Share.Lost > 0 || s.Quality == stats.QualityPoor || s.Quality == stats.QualityLost) &&
			p.match(s.Identity, s.Name, s.Room, s.SID)
	}, sessionCmp(sort))
	return map[string]any{"version": h.version.Load(), "total": total, "offset": p.offset, "items": items}
}

func sessionCmp(sort string) func(a, b *SessionBrief) int {
	bySID := func(a, b *SessionBrief) int { return strings.Compare(a.SID, b.SID) }
	endOf := func(s *SessionBrief) time.Time {
		if s.EndedAt != nil {
			return *s.EndedAt
		}
		return time.Time{}
	}
	switch sort {
	case "ended":
		return func(a, b *SessionBrief) int { return cmp.Or(endOf(a).Compare(endOf(b)), bySID(a, b)) }
	case "duration":
		return func(a, b *SessionBrief) int { return cmp.Or(cmp.Compare(a.DurationMs, b.DurationMs), bySID(a, b)) }
	case "bad":
		return func(a, b *SessionBrief) int {
			return cmp.Or(cmp.Compare(a.Share.Poor+a.Share.Lost, b.Share.Poor+b.Share.Lost), bySID(a, b))
		}
	case "identity":
		return func(a, b *SessionBrief) int { return cmp.Or(strings.Compare(a.Identity, b.Identity), bySID(a, b)) }
	case "rtt":
		return func(a, b *SessionBrief) int { return cmp.Or(cmp.Compare(a.MaxRTTMs, b.MaxRTTMs), bySID(a, b)) }
	case "loss":
		return func(a, b *SessionBrief) int {
			return cmp.Or(cmp.Compare(a.MaxLossPercent, b.MaxLossPercent), bySID(a, b))
		}
	}
	return func(a, b *SessionBrief) int { return cmp.Or(a.StartedAt.Compare(b.StartedAt), bySID(a, b)) }
}

// Hops are the three legs of "why was this call bad" for an active session.
type Hops struct {
	Uplink   []HopTrack  `json:"uplink"`
	Downlink []HopTrack  `json:"downlink"`
	Sources  []HopSource `json:"sources"`
}

// HopTrack is one track on a hop.
type HopTrack struct {
	TrackSID    string  `json:"track_sid"`
	Presenter   string  `json:"presenter,omitempty"`
	Kind        string  `json:"kind"`
	LossPercent float64 `json:"loss_percent"`
	JitterMs    float64 `json:"jitter_ms"`
	RTTMs       float64 `json:"rtt_ms,omitempty"`
	BitrateBps  float64 `json:"bitrate_bps"`
	Paused      bool    `json:"paused,omitempty"`
}

// HopSource is the uplink of a presenter whose track the session receives.
type HopSource struct {
	Presenter         string  `json:"presenter"`
	TrackSID          string  `json:"track_sid"`
	Kind              string  `json:"kind"`
	UplinkLossPercent float64 `json:"uplink_loss_percent"`
	OnThisNode        bool    `json:"on_this_node"`
}

const hopsMax = 50

// SessionDetail is one session with its timeline and, while active, its hops.
type SessionDetail struct {
	SessionBrief
	Events []SessionEvent `json:"events"`
	Hops   *Hops          `json:"hops,omitempty"`
}

func (h *Handler) sessionDetail(v url.Values) (any, error) {
	sid := v.Get("sid")
	b, events, ok := h.journal.detail(sid, h.p.Now())
	if !ok {
		return nil, errGone
	}
	res := SessionDetail{SessionBrief: b, Events: events}
	if b.State == "active" {
		res.Hops = hopsOf(h.indexNow().snap, b.Room, sid)
	}
	return res, nil
}

func hopsOf(s *stats.Snapshot, room, sid string) *Hops {
	hp := &Hops{Uplink: []HopTrack{}, Downlink: []HopTrack{}, Sources: []HopSource{}}
	for ri := range s.Rooms {
		r := &s.Rooms[ri]
		if r.Name != room {
			continue
		}
		type src struct {
			who  string
			kind string
			loss float64
			here bool
		}
		pub := map[string]src{} // track sid -> its presenter's uplink on this node
		for pi := range r.Participants {
			p := &r.Participants[pi]
			for _, t := range p.Published {
				pub[t.SID] = src{who: p.Identity, kind: t.Kind, loss: t.LossPercent, here: p.ICECandidateType != ""}
			}
		}
		for pi := range r.Participants {
			p := &r.Participants[pi]
			if p.SID != sid {
				continue
			}
			for _, t := range p.Published {
				hp.Uplink = append(hp.Uplink, HopTrack{TrackSID: t.SID, Kind: t.Kind, LossPercent: t.LossPercent, JitterMs: t.JitterMs, BitrateBps: t.BitrateBps})
			}
			for _, t := range p.Subscribed {
				hp.Downlink = append(hp.Downlink, HopTrack{TrackSID: t.TrackSID, Presenter: pub[t.TrackSID].who, Kind: t.Kind, LossPercent: t.LossPercent,
					JitterMs: t.JitterMs, RTTMs: t.RTTMs, BitrateBps: t.BitrateBps, Paused: t.Paused})
				if sp, ok := pub[t.TrackSID]; ok {
					hp.Sources = append(hp.Sources, HopSource{Presenter: sp.who, TrackSID: t.TrackSID, Kind: sp.kind, UplinkLossPercent: sp.loss, OnThisNode: sp.here})
				}
			}
		}
	}
	slices.SortStableFunc(hp.Downlink, func(a, b HopTrack) int { return cmp.Compare(b.LossPercent, a.LossPercent) })
	slices.SortStableFunc(hp.Sources, func(a, b HopSource) int { return cmp.Compare(b.UplinkLossPercent, a.UplinkLossPercent) })
	hp.Downlink = hp.Downlink[:min(len(hp.Downlink), hopsMax)]
	hp.Sources = hp.Sources[:min(len(hp.Sources), hopsMax)]
	return hp
}
