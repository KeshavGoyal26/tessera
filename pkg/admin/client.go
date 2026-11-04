package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
)

// Client is a Twirp (protobuf over HTTP) client for the admin API. It mints a
// short-lived token with the given grant for every call.
type Client struct {
	baseURL, apiKey, secret string
	grant                   auth.RoomGrant
	hc                      *http.Client
}

// NewClient creates a client. grant is the grant put into every token; use
// FullGrant for a full-access one.
func NewClient(baseURL, apiKey, secret string, grant auth.RoomGrant) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, secret: secret, grant: grant,
		hc: &http.Client{Timeout: 60 * time.Second},
	}
}

// FullGrant returns a grant with every admin permission (not the worker one).
func FullGrant() auth.RoomGrant {
	return auth.RoomGrant{RoomCreate: true, RoomList: true, RoomAdmin: true, RoomRecord: true}
}

// WorkerGrant returns the grant used by recorder workers.
func WorkerGrant() auth.RoomGrant { return auth.RoomGrant{RecorderWorker: true} }

// Call invokes service/method with req and decodes into resp.
func (c *Client) Call(ctx context.Context, service, method string, req, resp proto.Message) error {
	g := c.grant
	tok, err := auth.NewAccessToken(c.apiKey, c.secret).SetGrant(&g).SetValidFor(10 * time.Minute).ToJWT()
	if err != nil {
		return err
	}
	body, err := proto.Marshal(req)
	if err != nil {
		return err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s%s%s/%s", c.baseURL, PathPrefix, service, method), bytes.NewReader(body))
	if err != nil {
		return err
	}
	hr.Header.Set("Content-Type", "application/protobuf")
	hr.Header.Set("Authorization", "Bearer "+tok)
	hres, err := c.hc.Do(hr)
	if err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "admin request failed")
	}
	defer func() { _ = hres.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(hres.Body, MaxBodyBytes))
	if err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "reading admin response")
	}
	if hres.StatusCode != http.StatusOK {
		var te twirpError
		if json.Unmarshal(data, &te) != nil || te.Code == "" {
			return errs.New(errs.CodeInternal, "admin API returned HTTP %d", hres.StatusCode)
		}
		return &errs.Error{Code: errs.Code(te.Code), Msg: te.Msg}
	}
	return proto.Unmarshal(data, resp)
}

// RoomService wraps the RoomService RPCs.

// CreateRoom creates a room.
func (c *Client) CreateRoom(ctx context.Context, req *tesserav1.CreateRoomRequest) (*tesserav1.Room, error) {
	var r tesserav1.CreateRoomResponse
	if err := c.Call(ctx, RoomServiceName, "CreateRoom", req, &r); err != nil {
		return nil, err
	}
	return r.Room, nil
}

// ListRooms lists rooms.
func (c *Client) ListRooms(ctx context.Context, names ...string) ([]*tesserav1.Room, error) {
	var r tesserav1.ListRoomsResponse
	if err := c.Call(ctx, RoomServiceName, "ListRooms", &tesserav1.ListRoomsRequest{Names: names}, &r); err != nil {
		return nil, err
	}
	return r.Rooms, nil
}

// DeleteRoom deletes a room.
func (c *Client) DeleteRoom(ctx context.Context, room string) error {
	return c.Call(ctx, RoomServiceName, "DeleteRoom", &tesserav1.DeleteRoomRequest{Room: room}, &tesserav1.DeleteRoomResponse{})
}

// UpdateRoomMetadata updates room metadata.
func (c *Client) UpdateRoomMetadata(ctx context.Context, room, metadata string) (*tesserav1.Room, error) {
	var r tesserav1.UpdateRoomMetadataResponse
	err := c.Call(ctx, RoomServiceName, "UpdateRoomMetadata", &tesserav1.UpdateRoomMetadataRequest{Room: room, Metadata: metadata}, &r)
	return r.Room, err
}

// ListParticipants lists participants of a room.
func (c *Client) ListParticipants(ctx context.Context, room string) ([]*tesserav1.ParticipantInfo, error) {
	var r tesserav1.ListParticipantsResponse
	if err := c.Call(ctx, RoomServiceName, "ListParticipants", &tesserav1.ListParticipantsRequest{Room: room}, &r); err != nil {
		return nil, err
	}
	return r.Participants, nil
}

// GetParticipant returns one participant.
func (c *Client) GetParticipant(ctx context.Context, room, identity string) (*tesserav1.ParticipantInfo, error) {
	var r tesserav1.GetParticipantResponse
	err := c.Call(ctx, RoomServiceName, "GetParticipant", &tesserav1.GetParticipantRequest{Room: room, Identity: identity}, &r)
	return r.Participant, err
}

// RemoveParticipant disconnects a participant.
func (c *Client) RemoveParticipant(ctx context.Context, room, identity string) error {
	return c.Call(ctx, RoomServiceName, "RemoveParticipant", &tesserav1.RemoveParticipantRequest{Room: room, Identity: identity}, &tesserav1.RemoveParticipantResponse{})
}

// MutePublishedTrack mutes or unmutes a published track.
func (c *Client) MutePublishedTrack(ctx context.Context, room, identity, trackSID string, muted bool) (*tesserav1.TrackInfo, error) {
	var r tesserav1.MutePublishedTrackResponse
	err := c.Call(ctx, RoomServiceName, "MutePublishedTrack", &tesserav1.MutePublishedTrackRequest{Room: room, Identity: identity, TrackSid: trackSID, Muted: muted}, &r)
	return r.Track, err
}

// UpdateParticipant updates a participant.
func (c *Client) UpdateParticipant(ctx context.Context, req *tesserav1.UpdateParticipantRequest) (*tesserav1.ParticipantInfo, error) {
	var r tesserav1.UpdateParticipantResponse
	err := c.Call(ctx, RoomServiceName, "UpdateParticipant", req, &r)
	return r.Participant, err
}

// StartRecording starts a recording.
func (c *Client) StartRecording(ctx context.Context, req *tesserav1.StartRecordingRequest) (*tesserav1.RecordingInfo, error) {
	var r tesserav1.StartRecordingResponse
	err := c.Call(ctx, RecordingServiceName, "StartRecording", req, &r)
	return r.Recording, err
}

// StopRecording stops a recording.
func (c *Client) StopRecording(ctx context.Context, id string) (*tesserav1.RecordingInfo, error) {
	var r tesserav1.StopRecordingResponse
	err := c.Call(ctx, RecordingServiceName, "StopRecording", &tesserav1.StopRecordingRequest{RecordingId: id}, &r)
	return r.Recording, err
}

// ListRecordings lists recordings.
func (c *Client) ListRecordings(ctx context.Context, req *tesserav1.ListRecordingsRequest) ([]*tesserav1.RecordingInfo, error) {
	var r tesserav1.ListRecordingsResponse
	if err := c.Call(ctx, RecordingServiceName, "ListRecordings", req, &r); err != nil {
		return nil, err
	}
	return r.Recordings, nil
}

// GetRecording returns one recording.
func (c *Client) GetRecording(ctx context.Context, id string) (*tesserav1.RecordingInfo, error) {
	var r tesserav1.GetRecordingResponse
	err := c.Call(ctx, RecordingServiceName, "GetRecording", &tesserav1.GetRecordingRequest{RecordingId: id}, &r)
	return r.Recording, err
}

// ClaimJob is the worker long-poll.
func (c *Client) ClaimJob(ctx context.Context, req *tesserav1.ClaimJobRequest) (*tesserav1.ClaimJobResponse, error) {
	var r tesserav1.ClaimJobResponse
	err := c.Call(ctx, RecorderWorkerServiceName, "ClaimJob", req, &r)
	return &r, err
}

// UpdateJob reports job status.
func (c *Client) UpdateJob(ctx context.Context, req *tesserav1.UpdateJobRequest) error {
	return c.Call(ctx, RecorderWorkerServiceName, "UpdateJob", req, &tesserav1.UpdateJobResponse{})
}

// Heartbeat sends a worker heartbeat.
func (c *Client) Heartbeat(ctx context.Context, req *tesserav1.HeartbeatRequest) (*tesserav1.HeartbeatResponse, error) {
	var r tesserav1.HeartbeatResponse
	err := c.Call(ctx, RecorderWorkerServiceName, "Heartbeat", req, &r)
	return &r, err
}
