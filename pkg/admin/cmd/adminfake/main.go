// Command adminfake runs the real admin API server and webhook notifier on top
// of in-memory fakes. It exists for cross-language tests (the Java SDK test
// suite starts it and talks to it); it is not part of the product.
//
//	adminfake -key K -secret S [-webhook URL]
//
// It prints "LISTENING <port>" on stdout once ready and exits on SIGTERM/EOF
// of stdin.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/admin"
	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
	"github.com/KeshavGoyal26/tessera/pkg/webhook"
)

type rooms struct {
	mu   sync.Mutex
	r    map[string]*tesserav1.Room
	n    webhook.Notifier
	recs map[string]*tesserav1.RecordingInfo
}

func (f *rooms) CreateRoom(ctx context.Context, q *tesserav1.CreateRoomRequest) (*tesserav1.Room, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.r[q.Name]; ok {
		return r, nil
	}
	r := &tesserav1.Room{Name: q.Name, Sid: "RM_" + q.Name, Metadata: q.Metadata, MaxParticipants: q.MaxParticipants}
	f.r[q.Name] = r
	f.n.Notify(ctx, &tesserav1.WebhookEvent{Type: tesserav1.WebhookEventType_WEBHOOK_EVENT_TYPE_ROOM_STARTED, Room: r})
	return r, nil
}
func (f *rooms) ListRooms(_ context.Context, names []string) ([]*tesserav1.Room, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*tesserav1.Room
	for _, r := range f.r {
		if len(names) == 0 || has(names, r.Name) {
			out = append(out, r)
		}
	}
	return out, nil
}
func has(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
func (f *rooms) get(name string) (*tesserav1.Room, error) {
	if r, ok := f.r[name]; ok {
		return r, nil
	}
	return nil, errs.New(errs.CodeNotFound, "room %q not found", name)
}
func (f *rooms) DeleteRoom(ctx context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.get(name)
	if err != nil {
		return err
	}
	delete(f.r, name)
	f.n.Notify(ctx, &tesserav1.WebhookEvent{Type: tesserav1.WebhookEventType_WEBHOOK_EVENT_TYPE_ROOM_FINISHED, Room: r})
	return nil
}
func (f *rooms) UpdateRoomMetadata(_ context.Context, name, md string) (*tesserav1.Room, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.get(name)
	if err != nil {
		return nil, err
	}
	r.Metadata = md
	return r, nil
}
func (f *rooms) ListParticipants(_ context.Context, name string) ([]*tesserav1.ParticipantInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.get(name); err != nil {
		return nil, err
	}
	return []*tesserav1.ParticipantInfo{{Identity: "alice", Sid: "PA_alice", Tracks: []*tesserav1.TrackInfo{{Sid: "TR_1"}}}}, nil
}
func (f *rooms) GetParticipant(ctx context.Context, name, id string) (*tesserav1.ParticipantInfo, error) {
	ps, err := f.ListParticipants(ctx, name)
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		if p.Identity == id {
			return p, nil
		}
	}
	return nil, errs.New(errs.CodeNotFound, "participant %q not found", id)
}
func (f *rooms) RemoveParticipant(ctx context.Context, name, id string) error {
	_, err := f.GetParticipant(ctx, name, id)
	return err
}
func (f *rooms) MutePublishedTrack(_ context.Context, _, _, sid string, muted bool) (*tesserav1.TrackInfo, error) {
	return &tesserav1.TrackInfo{Sid: sid, Muted: muted}, nil
}
func (f *rooms) UpdateParticipant(ctx context.Context, q *tesserav1.UpdateParticipantRequest) (*tesserav1.ParticipantInfo, error) {
	p, err := f.GetParticipant(ctx, q.Room, q.Identity)
	if err != nil {
		return nil, err
	}
	p.Metadata = q.GetMetadata()
	return p, nil
}

func (f *rooms) StartRecording(_ context.Context, q *tesserav1.StartRecordingRequest) (*tesserav1.RecordingInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.get(q.Room); err != nil {
		return nil, err
	}
	i := &tesserav1.RecordingInfo{RecordingId: fmt.Sprintf("REC_%d", len(f.recs)+1), Room: q.Room,
		Status: tesserav1.RecordingStatus_RECORDING_STATUS_ACTIVE, Request: q}
	f.recs[i.RecordingId] = i
	return i, nil
}
func (f *rooms) StopRecording(_ context.Context, id string) (*tesserav1.RecordingInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i, ok := f.recs[id]
	if !ok {
		return nil, errs.New(errs.CodeNotFound, "recording %q not found", id)
	}
	i.Status = tesserav1.RecordingStatus_RECORDING_STATUS_COMPLETE
	return i, nil
}
func (f *rooms) ListRecordings(_ context.Context, q *tesserav1.ListRecordingsRequest) ([]*tesserav1.RecordingInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*tesserav1.RecordingInfo
	for _, i := range f.recs {
		if q.Room == "" || q.Room == i.Room {
			out = append(out, i)
		}
	}
	return out, nil
}
func (f *rooms) GetRecording(_ context.Context, id string) (*tesserav1.RecordingInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i, ok := f.recs[id]; ok {
		return i, nil
	}
	return nil, errs.New(errs.CodeNotFound, "recording %q not found", id)
}

func main() {
	key := flag.String("key", "devkey", "API key")
	secret := flag.String("secret", "", "API secret")
	hook := flag.String("webhook", "", "webhook endpoint URL")
	enc := flag.String("webhook-encoding", "json", "webhook encoding: json or protobuf")
	flag.Parse()
	if *secret == "" {
		fmt.Fprintln(os.Stderr, "-secret is required")
		os.Exit(2)
	}
	keys := auth.StaticKeys{*key: *secret}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	var n webhook.Notifier = webhook.Nop{}
	if *hook != "" {
		var err error
		n, err = webhook.NewNotifier(config.WebhookConfig{APIKey: *key, Timeout: 5 * time.Second, QueueSize: 100,
			Endpoints: []config.WebhookEndpoint{{URL: *hook, Encoding: *enc}}}, keys, nil, logger)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	f := &rooms{r: map[string]*tesserav1.Room{}, recs: map[string]*tesserav1.RecordingInfo{}, n: n}
	srv := admin.NewServer(admin.Params{Rooms: f, Recordings: f, Verifier: auth.NewVerifier(keys), Logger: logger})
	mux := http.NewServeMux()
	mux.Handle(admin.PathPrefix, srv)
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	go func() { _ = http.Serve(ln, mux) }()
	fmt.Printf("LISTENING %d\n", ln.Addr().(*net.TCPAddr).Port)
	_, _ = io.Copy(io.Discard, os.Stdin) // exit when the parent closes stdin
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = n.Close(ctx)
}
