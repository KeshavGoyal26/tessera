package admin

import (
	"context"
	"net/http"

	"google.golang.org/protobuf/proto"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
)

type callCtx struct {
	r      *http.Request
	grants *auth.RoomGrant
}

func (c *callCtx) ctx() context.Context { return c.r.Context() }

// rpc registers a typed handler.
func rpc[Req, Resp any, PReq interface {
	*Req
	proto.Message
}, PResp interface {
	*Resp
	proto.Message
}](m map[string]*route, name string, authz func(*auth.RoomGrant, PReq) error, h func(context.Context, PReq) (PResp, error)) {
	m[name] = &route{
		newReq: func() proto.Message { return PReq(new(Req)) },
		authorize: func(g *auth.RoomGrant, req proto.Message) error {
			return authz(g, req.(PReq))
		},
		handle: func(c *callCtx, req proto.Message) (proto.Message, error) {
			resp, err := h(c.ctx(), req.(PReq))
			if err != nil {
				return nil, err
			}
			return resp, nil
		},
	}
}

func denied(grant string) error {
	return errs.New(errs.CodePermissionDenied, "token lacks the %s grant", grant)
}

func needCreate(g *auth.RoomGrant) error {
	if !g.RoomCreate {
		return denied("room_create")
	}
	return nil
}

func needList(g *auth.RoomGrant) error {
	if !g.RoomList {
		return denied("room_list")
	}
	return nil
}

// needAdmin requires room_admin; a grant scoped to a room only covers it.
func needAdmin(g *auth.RoomGrant, room string) error {
	if !g.RoomAdmin {
		return denied("room_admin")
	}
	if g.Room != "" && g.Room != room {
		return errs.New(errs.CodePermissionDenied, "token is scoped to room %q", g.Room)
	}
	return nil
}

func needRecord(g *auth.RoomGrant, room string) error {
	if !g.RoomRecord {
		return denied("room_record")
	}
	if room != "" && g.Room != "" && g.Room != room {
		return errs.New(errs.CodePermissionDenied, "token is scoped to room %q", g.Room)
	}
	return nil
}

func needWorker(g *auth.RoomGrant) error {
	if !g.RecorderWorker {
		return denied("recorder_worker")
	}
	return nil
}

func buildRoutes(p Params) map[string]*route {
	m := map[string]*route{}
	rs := RoomServiceName + "/"
	rc := RecordingServiceName + "/"
	wk := RecorderWorkerServiceName + "/"

	rpc(m, rs+"CreateRoom", func(g *auth.RoomGrant, _ *tesserav1.CreateRoomRequest) error { return needCreate(g) },
		func(ctx context.Context, r *tesserav1.CreateRoomRequest) (*tesserav1.CreateRoomResponse, error) {
			if r.Name == "" {
				return nil, errs.New(errs.CodeInvalidArgument, "name is required")
			}
			if p.Rooms == nil {
				return nil, errs.NotImplemented("rooms")
			}
			out, err := p.Rooms.CreateRoom(ctx, r)
			if err != nil {
				return nil, err
			}
			return &tesserav1.CreateRoomResponse{Room: out}, nil
		})
	rpc(m, rs+"ListRooms", func(g *auth.RoomGrant, _ *tesserav1.ListRoomsRequest) error { return needList(g) },
		func(ctx context.Context, r *tesserav1.ListRoomsRequest) (*tesserav1.ListRoomsResponse, error) {
			if p.Rooms == nil {
				return nil, errs.NotImplemented("rooms")
			}
			out, err := p.Rooms.ListRooms(ctx, r.Names)
			if err != nil {
				return nil, err
			}
			return &tesserav1.ListRoomsResponse{Rooms: out}, nil
		})
	rpc(m, rs+"DeleteRoom", func(g *auth.RoomGrant, _ *tesserav1.DeleteRoomRequest) error { return needCreate(g) },
		func(ctx context.Context, r *tesserav1.DeleteRoomRequest) (*tesserav1.DeleteRoomResponse, error) {
			if r.Room == "" {
				return nil, errs.New(errs.CodeInvalidArgument, "room is required")
			}
			if p.Rooms == nil {
				return nil, errs.NotImplemented("rooms")
			}
			return &tesserav1.DeleteRoomResponse{}, p.Rooms.DeleteRoom(ctx, r.Room)
		})
	rpc(m, rs+"UpdateRoomMetadata", func(g *auth.RoomGrant, r *tesserav1.UpdateRoomMetadataRequest) error { return needAdmin(g, r.Room) },
		func(ctx context.Context, r *tesserav1.UpdateRoomMetadataRequest) (*tesserav1.UpdateRoomMetadataResponse, error) {
			if r.Room == "" {
				return nil, errs.New(errs.CodeInvalidArgument, "room is required")
			}
			if p.Rooms == nil {
				return nil, errs.NotImplemented("rooms")
			}
			out, err := p.Rooms.UpdateRoomMetadata(ctx, r.Room, r.Metadata)
			if err != nil {
				return nil, err
			}
			return &tesserav1.UpdateRoomMetadataResponse{Room: out}, nil
		})
	rpc(m, rs+"ListParticipants", func(g *auth.RoomGrant, r *tesserav1.ListParticipantsRequest) error { return needAdmin(g, r.Room) },
		func(ctx context.Context, r *tesserav1.ListParticipantsRequest) (*tesserav1.ListParticipantsResponse, error) {
			if r.Room == "" {
				return nil, errs.New(errs.CodeInvalidArgument, "room is required")
			}
			if p.Rooms == nil {
				return nil, errs.NotImplemented("rooms")
			}
			out, err := p.Rooms.ListParticipants(ctx, r.Room)
			if err != nil {
				return nil, err
			}
			return &tesserav1.ListParticipantsResponse{Participants: out}, nil
		})
	rpc(m, rs+"GetParticipant", func(g *auth.RoomGrant, r *tesserav1.GetParticipantRequest) error { return needAdmin(g, r.Room) },
		func(ctx context.Context, r *tesserav1.GetParticipantRequest) (*tesserav1.GetParticipantResponse, error) {
			if r.Room == "" || r.Identity == "" {
				return nil, errs.New(errs.CodeInvalidArgument, "room and identity are required")
			}
			if p.Rooms == nil {
				return nil, errs.NotImplemented("rooms")
			}
			out, err := p.Rooms.GetParticipant(ctx, r.Room, r.Identity)
			if err != nil {
				return nil, err
			}
			return &tesserav1.GetParticipantResponse{Participant: out}, nil
		})
	rpc(m, rs+"RemoveParticipant", func(g *auth.RoomGrant, r *tesserav1.RemoveParticipantRequest) error { return needAdmin(g, r.Room) },
		func(ctx context.Context, r *tesserav1.RemoveParticipantRequest) (*tesserav1.RemoveParticipantResponse, error) {
			if r.Room == "" || r.Identity == "" {
				return nil, errs.New(errs.CodeInvalidArgument, "room and identity are required")
			}
			if p.Rooms == nil {
				return nil, errs.NotImplemented("rooms")
			}
			return &tesserav1.RemoveParticipantResponse{}, p.Rooms.RemoveParticipant(ctx, r.Room, r.Identity)
		})
	rpc(m, rs+"MutePublishedTrack", func(g *auth.RoomGrant, r *tesserav1.MutePublishedTrackRequest) error { return needAdmin(g, r.Room) },
		func(ctx context.Context, r *tesserav1.MutePublishedTrackRequest) (*tesserav1.MutePublishedTrackResponse, error) {
			if r.Room == "" || r.Identity == "" || r.TrackSid == "" {
				return nil, errs.New(errs.CodeInvalidArgument, "room, identity and track_sid are required")
			}
			if p.Rooms == nil {
				return nil, errs.NotImplemented("rooms")
			}
			out, err := p.Rooms.MutePublishedTrack(ctx, r.Room, r.Identity, r.TrackSid, r.Muted)
			if err != nil {
				return nil, err
			}
			return &tesserav1.MutePublishedTrackResponse{Track: out}, nil
		})
	rpc(m, rs+"UpdateParticipant", func(g *auth.RoomGrant, r *tesserav1.UpdateParticipantRequest) error { return needAdmin(g, r.Room) },
		func(ctx context.Context, r *tesserav1.UpdateParticipantRequest) (*tesserav1.UpdateParticipantResponse, error) {
			if r.Room == "" || r.Identity == "" {
				return nil, errs.New(errs.CodeInvalidArgument, "room and identity are required")
			}
			if p.Rooms == nil {
				return nil, errs.NotImplemented("rooms")
			}
			out, err := p.Rooms.UpdateParticipant(ctx, r)
			if err != nil {
				return nil, err
			}
			return &tesserav1.UpdateParticipantResponse{Participant: out}, nil
		})

	// RecordingService.
	rpc(m, rc+"StartRecording", func(g *auth.RoomGrant, r *tesserav1.StartRecordingRequest) error { return needRecord(g, r.Room) },
		func(ctx context.Context, r *tesserav1.StartRecordingRequest) (*tesserav1.StartRecordingResponse, error) {
			if r.Room == "" {
				return nil, errs.New(errs.CodeInvalidArgument, "room is required")
			}
			if p.Recordings == nil {
				return nil, errs.NotImplemented("recordings")
			}
			out, err := p.Recordings.StartRecording(ctx, r)
			if err != nil {
				return nil, err
			}
			return &tesserav1.StartRecordingResponse{Recording: out}, nil
		})
	// Stop/Get on a room-scoped token must check the recording's room.
	scopedByRecording := func(g *auth.RoomGrant, id string) error {
		if err := needRecord(g, ""); err != nil {
			return err
		}
		if g.Room == "" {
			return nil
		}
		if p.Recordings == nil {
			return errs.NotImplemented("recordings")
		}
		info, err := p.Recordings.GetRecording(context.Background(), id)
		if err != nil {
			return err
		}
		return needRecord(g, info.GetRoom())
	}
	rpc(m, rc+"StopRecording", func(g *auth.RoomGrant, r *tesserav1.StopRecordingRequest) error {
		return scopedByRecording(g, r.RecordingId)
	},
		func(ctx context.Context, r *tesserav1.StopRecordingRequest) (*tesserav1.StopRecordingResponse, error) {
			if r.RecordingId == "" {
				return nil, errs.New(errs.CodeInvalidArgument, "recording_id is required")
			}
			if p.Recordings == nil {
				return nil, errs.NotImplemented("recordings")
			}
			out, err := p.Recordings.StopRecording(ctx, r.RecordingId)
			if err != nil {
				return nil, err
			}
			return &tesserav1.StopRecordingResponse{Recording: out}, nil
		})
	rpc(m, rc+"ListRecordings", func(g *auth.RoomGrant, r *tesserav1.ListRecordingsRequest) error {
		if err := needRecord(g, r.Room); err != nil {
			return err
		}
		if g.Room != "" && r.Room == "" {
			return errs.New(errs.CodePermissionDenied, "token is scoped to room %q; room filter required", g.Room)
		}
		return nil
	},
		func(ctx context.Context, r *tesserav1.ListRecordingsRequest) (*tesserav1.ListRecordingsResponse, error) {
			if p.Recordings == nil {
				return nil, errs.NotImplemented("recordings")
			}
			out, err := p.Recordings.ListRecordings(ctx, r)
			if err != nil {
				return nil, err
			}
			return &tesserav1.ListRecordingsResponse{Recordings: out}, nil
		})
	rpc(m, rc+"GetRecording", func(g *auth.RoomGrant, r *tesserav1.GetRecordingRequest) error {
		return scopedByRecording(g, r.RecordingId)
	},
		func(ctx context.Context, r *tesserav1.GetRecordingRequest) (*tesserav1.GetRecordingResponse, error) {
			if r.RecordingId == "" {
				return nil, errs.New(errs.CodeInvalidArgument, "recording_id is required")
			}
			if p.Recordings == nil {
				return nil, errs.NotImplemented("recordings")
			}
			out, err := p.Recordings.GetRecording(ctx, r.RecordingId)
			if err != nil {
				return nil, err
			}
			return &tesserav1.GetRecordingResponse{Recording: out}, nil
		})

	// RecorderWorkerService.
	rpc(m, wk+"ClaimJob", func(g *auth.RoomGrant, _ *tesserav1.ClaimJobRequest) error { return needWorker(g) },
		func(ctx context.Context, r *tesserav1.ClaimJobRequest) (*tesserav1.ClaimJobResponse, error) {
			if p.Workers == nil {
				return nil, errs.NotImplemented("workers")
			}
			return p.Workers.ClaimJob(ctx, r)
		})
	rpc(m, wk+"UpdateJob", func(g *auth.RoomGrant, _ *tesserav1.UpdateJobRequest) error { return needWorker(g) },
		func(ctx context.Context, r *tesserav1.UpdateJobRequest) (*tesserav1.UpdateJobResponse, error) {
			if p.Workers == nil {
				return nil, errs.NotImplemented("workers")
			}
			return &tesserav1.UpdateJobResponse{}, p.Workers.UpdateJob(ctx, r)
		})
	rpc(m, wk+"Heartbeat", func(g *auth.RoomGrant, _ *tesserav1.HeartbeatRequest) error { return needWorker(g) },
		func(ctx context.Context, r *tesserav1.HeartbeatRequest) (*tesserav1.HeartbeatResponse, error) {
			if p.Workers == nil {
				return nil, errs.NotImplemented("workers")
			}
			return p.Workers.Heartbeat(ctx, r)
		})
	return m
}
