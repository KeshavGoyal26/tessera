package admin

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/webhook"
)

// Golden contract fixtures shared with the Java SDK (sdk/java contract tests).
// Regenerate with: go test ./pkg/admin -run Golden -update
var update = flag.Bool("update", false, "rewrite testdata/contract fixtures")

const contractDir = "../../testdata/contract"

func writeOrCompare(t *testing.T, name string, v any) {
	t.Helper()
	want, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	path := filepath.Join(contractDir, name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s is stale; run go test ./pkg/admin -run Golden -update", name)
	}
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// ---- tokens ----

type tokenCase struct {
	Name       string            `json:"name"`
	APIKey     string            `json:"api_key"`
	Secret     string            `json:"secret"`
	Now        int64             `json:"now"`
	TTLSeconds int64             `json:"ttl_seconds"`
	Identity   string            `json:"identity,omitempty"`
	DispName   string            `json:"name_claim,omitempty"`
	Metadata   string            `json:"metadata,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
	Grants     *auth.RoomGrant   `json:"grants"`
	Claims     json.RawMessage   `json:"claims"`
	JWT        string            `json:"jwt"`
}

const fixSecret = "contract-secret-0123456789abcdef0123456789"

func TestGoldenTokens(t *testing.T) {
	now := int64(1_700_000_000)
	tr := true
	fl := false
	cases := []tokenCase{
		{Name: "join_basic", Identity: "alice", DispName: "Alice", Grants: &auth.RoomGrant{Room: "standup", RoomJoin: true}},
		{Name: "join_full", Identity: "bob", DispName: "Bob", Metadata: `{"userId":42}`, Attributes: map[string]string{"role": "host", "lang": "en"},
			Grants: &auth.RoomGrant{Room: "r1", RoomJoin: true, CanPublish: &fl, CanSubscribe: &tr, CanPublishData: &fl,
				CanPublishSources: []string{"camera", "microphone"}, CanUpdateOwnMetadata: true, AutoRecord: true,
				RoomConfig: &auth.RoomConfig{EmptyTimeoutSeconds: 300, DepartureTimeoutSeconds: 20, MaxParticipants: 50, RecordingEnabled: true}}},
		{Name: "recorder", Identity: "rec-1", Grants: &auth.RoomGrant{Room: "r1", RoomJoin: true, Hidden: true, Recorder: true, CanPublish: &fl}},
		{Name: "admin_all", Grants: &auth.RoomGrant{RoomCreate: true, RoomList: true, RoomAdmin: true, RoomRecord: true}},
		{Name: "admin_scoped", Grants: &auth.RoomGrant{Room: "r1", RoomAdmin: true, RoomRecord: true}},
		{Name: "worker", Grants: &auth.RoomGrant{RecorderWorker: true}},
	}
	for i := range cases {
		c := &cases[i]
		c.APIKey, c.Secret, c.Now, c.TTLSeconds = "APIcontract", fixSecret, now, 3600
		claims := auth.Claims{Issuer: c.APIKey, Subject: c.Identity, NotBefore: now, Expiry: now + c.TTLSeconds,
			Name: c.DispName, Metadata: c.Metadata, Attributes: c.Attributes, Grants: c.Grants}
		raw, _ := json.Marshal(claims)
		c.Claims = raw
		jwt, err := auth.Sign(&claims, c.Secret)
		if err != nil {
			t.Fatal(err)
		}
		c.JWT = jwt
		// Go verifies its own fixtures.
		v := auth.NewVerifier(auth.StaticKeys{c.APIKey: c.Secret})
		if _, err := v.Verify(jwt); err == nil {
			t.Fatalf("%s: fixture token at fixed past time must be expired for a wall-clock verifier", c.Name)
		}
	}
	writeOrCompare(t, "tokens/cases.json", cases)
}

// ---- webhooks ----

type webhookCase struct {
	Name         string `json:"name"`
	Key          string `json:"key"`
	Secret       string `json:"secret"`
	T            int64  `json:"t"`
	EventID      string `json:"event_id"`
	EventType    string `json:"event_type"`
	JSONBody     string `json:"json_body"`
	JSONSig      string `json:"json_signature"`
	ProtoBodyB64 string `json:"protobuf_body_base64"`
	ProtoSig     string `json:"protobuf_signature"`
	RoomName     string `json:"room_name,omitempty"`
	Identity     string `json:"participant_identity,omitempty"`
	TrackSid     string `json:"track_sid,omitempty"`
	RecordingID  string `json:"recording_id,omitempty"`
}

func TestGoldenWebhooks(t *testing.T) {
	const T = int64(1_700_000_000)
	room := &tesserav1.Room{Name: "standup", Sid: "RM_1", NumParticipants: 2}
	part := &tesserav1.ParticipantInfo{Identity: "alice", Sid: "PA_1", Name: "Alice"}
	track := &tesserav1.TrackInfo{Sid: "TR_1", Muted: false}
	rec := &tesserav1.RecordingInfo{RecordingId: "REC_1", Room: "standup", Status: tesserav1.RecordingStatus_RECORDING_STATUS_COMPLETE, StartedAtMs: 1_700_000_000_123}
	evs := []*tesserav1.WebhookEvent{
		{Id: "ev-room-started", Type: tesserav1.WebhookEventType_WEBHOOK_EVENT_TYPE_ROOM_STARTED, Room: room, CreatedAt: T},
		{Id: "ev-part-joined", Type: tesserav1.WebhookEventType_WEBHOOK_EVENT_TYPE_PARTICIPANT_JOINED, Room: room, Participant: part, CreatedAt: T},
		{Id: "ev-track-pub", Type: tesserav1.WebhookEventType_WEBHOOK_EVENT_TYPE_TRACK_PUBLISHED, Room: room, Participant: part, Track: track, CreatedAt: T, NumDropped: 3},
		{Id: "ev-rec-ended", Type: tesserav1.WebhookEventType_WEBHOOK_EVENT_TYPE_RECORDING_ENDED, Recording: rec, CreatedAt: T},
	}
	names := []string{"room_started", "participant_joined", "track_published", "recording_ended"}
	var cases []webhookCase
	for i, ev := range evs {
		jb, err := protojson.MarshalOptions{Multiline: false}.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		// protojson output is intentionally unstable; normalise through encoding/json.
		var any1 any
		_ = json.Unmarshal(jb, &any1)
		jb, _ = json.Marshal(any1)
		pb, _ := proto.Marshal(ev)
		now := time.Unix(T, 0)
		c := webhookCase{Name: names[i], Key: "APIcontract", Secret: fixSecret, T: T, EventID: ev.Id,
			EventType: ev.Type.String(), JSONBody: string(jb), JSONSig: webhook.Sign(fixSecret, now, jb),
			ProtoBodyB64: b64(pb), ProtoSig: webhook.Sign(fixSecret, now, pb),
			RoomName: ev.GetRoom().GetName(), Identity: ev.GetParticipant().GetIdentity(),
			TrackSid: ev.GetTrack().GetSid(), RecordingID: ev.GetRecording().GetRecordingId()}
		if err := webhook.Verify(c.JSONSig, jb, fixSecret, now, webhook.DefaultTolerance); err != nil {
			t.Fatal(err)
		}
		cases = append(cases, c)
	}
	writeOrCompare(t, "webhook/cases.json", cases)
}

// ---- twirp messages ----

type twirpCase struct {
	Service      string `json:"service"`
	Method       string `json:"method"`
	RequestJSON  string `json:"request_json"`
	RequestPB    string `json:"request_pb_base64"`
	ResponseJSON string `json:"response_json"`
	ResponsePB   string `json:"response_pb_base64"`
}

func mk(svc, method string, req, resp proto.Message) twirpCase {
	enc := func(m proto.Message) (string, string) {
		j, _ := protojson.Marshal(m)
		var v any
		_ = json.Unmarshal(j, &v)
		j, _ = json.Marshal(v)
		b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(m)
		return string(j), b64(b)
	}
	c := twirpCase{Service: svc, Method: method}
	c.RequestJSON, c.RequestPB = enc(req)
	c.ResponseJSON, c.ResponsePB = enc(resp)
	return c
}

func TestGoldenTwirp(t *testing.T) {
	room := &tesserav1.Room{Name: "standup", Sid: "RM_1", Metadata: "m", MaxParticipants: 10, NumParticipants: 1}
	part := &tesserav1.ParticipantInfo{Identity: "alice", Sid: "PA_1", Name: "Alice", Metadata: "x",
		Tracks: []*tesserav1.TrackInfo{{Sid: "TR_1", Muted: true}}}
	name, md := "Alice2", "meta"
	rec := &tesserav1.RecordingInfo{RecordingId: "REC_1", Room: "standup", Status: tesserav1.RecordingStatus_RECORDING_STATUS_ACTIVE,
		StartedAtMs: 1_700_000_000_000, MasterPlaylistLocation: "recordings/REC_1/master.m3u8",
		Outputs: []*tesserav1.RecordingOutput{{TrackSid: "TR_1", ParticipantIdentity: "alice", SegmentCount: 3, DurationMs: 12000, SizeBytes: 1 << 33}}}
	startReq := &tesserav1.StartRecordingRequest{Room: "standup",
		Target:         &tesserav1.StartRecordingRequest_Participant{Participant: &tesserav1.ParticipantRecordingTarget{Identity: "alice", Sources: []string{"camera"}}},
		Hls:            &tesserav1.HLSOptions{SegmentDurationSeconds: 4, PlaylistType: tesserav1.PlaylistType_PLAYLIST_TYPE_EVENT, AudioCodec: tesserav1.AudioCodec_AUDIO_CODEC_OPUS},
		FilenamePrefix: "demo"}
	rs, rc := RoomServiceName, RecordingServiceName
	cases := []twirpCase{
		mk(rs, "CreateRoom", &tesserav1.CreateRoomRequest{Name: "standup", EmptyTimeoutSeconds: 300, MaxParticipants: 10, Metadata: "m", RecordingEnabled: true},
			&tesserav1.CreateRoomResponse{Room: room}),
		mk(rs, "ListRooms", &tesserav1.ListRoomsRequest{Names: []string{"standup"}}, &tesserav1.ListRoomsResponse{Rooms: []*tesserav1.Room{room}}),
		mk(rs, "DeleteRoom", &tesserav1.DeleteRoomRequest{Room: "standup"}, &tesserav1.DeleteRoomResponse{}),
		mk(rs, "UpdateRoomMetadata", &tesserav1.UpdateRoomMetadataRequest{Room: "standup", Metadata: "m2"}, &tesserav1.UpdateRoomMetadataResponse{Room: room}),
		mk(rs, "ListParticipants", &tesserav1.ListParticipantsRequest{Room: "standup"}, &tesserav1.ListParticipantsResponse{Participants: []*tesserav1.ParticipantInfo{part}}),
		mk(rs, "GetParticipant", &tesserav1.GetParticipantRequest{Room: "standup", Identity: "alice"}, &tesserav1.GetParticipantResponse{Participant: part}),
		mk(rs, "RemoveParticipant", &tesserav1.RemoveParticipantRequest{Room: "standup", Identity: "alice"}, &tesserav1.RemoveParticipantResponse{}),
		mk(rs, "MutePublishedTrack", &tesserav1.MutePublishedTrackRequest{Room: "standup", Identity: "alice", TrackSid: "TR_1", Muted: true},
			&tesserav1.MutePublishedTrackResponse{Track: &tesserav1.TrackInfo{Sid: "TR_1", Muted: true}}),
		mk(rs, "UpdateParticipant", &tesserav1.UpdateParticipantRequest{Room: "standup", Identity: "alice", Name: &name, Metadata: &md,
			Attributes: map[string]string{"k": "v"}}, &tesserav1.UpdateParticipantResponse{Participant: part}),
		mk(rc, "StartRecording", startReq, &tesserav1.StartRecordingResponse{Recording: rec}),
		mk(rc, "StopRecording", &tesserav1.StopRecordingRequest{RecordingId: "REC_1"}, &tesserav1.StopRecordingResponse{Recording: rec}),
		mk(rc, "ListRecordings", &tesserav1.ListRecordingsRequest{Room: "standup", ActiveOnly: true}, &tesserav1.ListRecordingsResponse{Recordings: []*tesserav1.RecordingInfo{rec}}),
		mk(rc, "GetRecording", &tesserav1.GetRecordingRequest{RecordingId: "REC_1"}, &tesserav1.GetRecordingResponse{Recording: rec}),
	}
	writeOrCompare(t, "twirp/cases.json", cases)

	// The Go server must accept every request fixture in both encodings.
	s, rooms, recs := newTestServer(t)
	rooms.rooms["standup"] = &tesserav1.Room{Name: "standup"}
	recs.recs = map[string]*tesserav1.RecordingInfo{"REC_1": rec}
	tok := token(t, FullGrant())
	for _, c := range cases {
		path := "/twirp/" + c.Service + "/" + c.Method
		rooms.rooms["standup"] = &tesserav1.Room{Name: "standup"}
		w := post(s, path, tok, "application/json", c.RequestJSON)
		if w.Code != 200 {
			t.Errorf("%s json: %d %s", path, w.Code, w.Body)
		}
		rooms.rooms["standup"] = &tesserav1.Room{Name: "standup"}
		raw, _ := base64.StdEncoding.DecodeString(c.RequestPB)
		w = post(s, path, tok, "application/protobuf", string(raw))
		if w.Code != 200 {
			t.Errorf("%s protobuf: %d %s", path, w.Code, w.Body)
		}
	}
}
