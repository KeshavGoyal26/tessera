package e2e

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/client"
	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/hls/hlstest"
	"github.com/KeshavGoyal26/tessera/pkg/recorder"
)

// TestFullSystem (WP5): one server, 3 publishers (one H.264 + Opus camera
// that is recorded to HLS, two VP8 3-layer simulcast + Opus), 5 subscribers
// of which one sits behind a 1.2 Mbit/s policed link. Asserts that every
// subscriber receives every track, that the constrained subscriber is moved
// off the top simulcast layer, and that the recording is valid HLS (ffprobe).
func TestFullSystem(t *testing.T) {
	if testing.Short() {
		t.Skip("full-system test")
	}
	s := startServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	const roomName = "system"

	// Publisher 1: real H.264 + Opus media (recorded).
	cam := connect(t, s, roomName, "pub-h264")
	vtrack, _ := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000,
		SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"}, "video", "s")
	atrack, _ := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "s")
	if _, err := cam.PublishTrack(ctx, vtrack, client.PublishOptions{Name: "cam", Width: 320, Height: 240}); err != nil {
		t.Fatal(err)
	}
	if _, err := cam.PublishTrack(ctx, atrack, client.PublishOptions{Name: "mic"}); err != nil {
		t.Fatal(err)
	}
	_, errFF := exec.LookPath("ffmpeg")
	_, errFP := exec.LookPath("ffprobe")
	recordMedia := errFF == nil && errFP == nil
	stop := make(chan struct{})
	var wg sync.WaitGroup
	t.Cleanup(func() { close(stop); wg.Wait() })
	if !recordMedia {
		// Without ffmpeg there is no real clip to send, but the forwarding
		// assertions below still need media on these two tracks: send
		// synthetic H.264 IDR NAL units and Opus silence frames instead.
		wg.Add(1)
		go func() {
			defer wg.Done()
			vt := time.NewTicker(40 * time.Millisecond)
			at := time.NewTicker(20 * time.Millisecond)
			defer vt.Stop()
			defer at.Stop()
			var vseq, aseq uint16
			idr := append([]byte{0x65, 0x88}, make([]byte, 400)...)
			for {
				select {
				case <-stop:
					return
				case <-vt.C:
					_ = vtrack.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: vseq,
						Timestamp: uint32(vseq) * 3600, SSRC: 1, Marker: true}, Payload: idr})
					vseq++
				case <-at.C:
					_ = atrack.WriteRTP(hlstest.OpusPacket(aseq, uint32(aseq)*960, 2, []byte{0xfc, 0xff, 0xfe}))
					aseq++
				}
			}
		}()
	}
	if recordMedia {
		aus := hlstest.H264(t, 20)
		opus := hlstest.Opus(t, 20)
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := &hlstest.Packetizer{SSRC: 1}
			vt := time.NewTicker(40 * time.Millisecond)
			at := time.NewTicker(20 * time.Millisecond)
			defer vt.Stop()
			defer at.Stop()
			vi, ai := 0, 0
			for vi < len(aus) || ai < len(opus) {
				select {
				case <-stop:
					return
				case <-vt.C:
					if vi < len(aus) {
						for _, pkt := range p.Packetize(aus[vi], uint32(vi*3600)) {
							_ = vtrack.WriteRTP(pkt)
						}
						vi++
					}
				case <-at.C:
					if ai < len(opus) {
						_ = atrack.WriteRTP(hlstest.OpusPacket(uint16(ai), uint32(ai*960), 2, opus[ai]))
						ai++
					}
				}
			}
		}()
	}

	// Publishers 2 and 3: VP8 simulcast (~120k / ~390k / ~880k bit/s) + Opus.
	var simTracks []string
	for i := range 2 {
		c := connect(t, s, roomName, fmt.Sprintf("pub-sim%d", i))
		layers := newSimLayers(t, fmt.Sprintf("cam%d", i))
		for j, pad := range []int{140, 480, 1080} {
			layers[j].pad = pad
		}
		info, err := c.PublishSimulcastTrack(ctx, []webrtc.TrackLocal{layers[0].track, layers[1].track, layers[2].track},
			client.PublishOptions{Name: "cam", Width: 1280, Height: 720})
		if err != nil {
			t.Fatal(err)
		}
		simTracks = append(simTracks, info.Sid)
		a := newSynth(t, "audio", fmt.Sprintf("mic%d", i))
		if _, err := c.PublishTrack(ctx, a.track, client.PublishOptions{Name: "mic"}); err != nil {
			t.Fatal(err)
		}
		a.run()
		t.Cleanup(a.Stop)
		wg.Add(1)
		go func() {
			defer wg.Done()
			tk := time.NewTicker(20 * time.Millisecond)
			defer tk.Stop()
			for {
				select {
				case <-stop:
					return
				case <-tk.C:
					for _, l := range layers {
						l.writeFrame()
					}
				}
			}
		}()
	}

	// Five subscribers; sub0 is bandwidth-capped.
	type subState struct {
		c        client.Client
		received sync.Map // track sid -> *atomic.Int64
	}
	var subs []*subState
	for i := range 5 {
		opts := client.Options{AutoSubscribe: true, SettingEngine: client.LoopbackSettingEngine(), Logger: testLogger()}
		if i == 0 {
			raw, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			pc := &policedConn{UDPConn: raw, last: time.Now()}
			pc.rate.Store(1_200_000)
			mux := ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: pc})
			t.Cleanup(func() { _ = mux.Close() })
			opts.SettingEngine.SetICEUDPMux(mux)
		}
		st := &subState{c: client.New(opts)}
		st.c.SetCallbacks(client.Callbacks{OnTrackSubscribed: func(rt *client.RemoteTrack) {
			cnt := &atomic.Int64{}
			st.received.Store(rt.SID, cnt)
			go func() {
				b := make([]byte, 1500)
				for {
					if _, err := rt.Read(b); err != nil {
						return
					}
					cnt.Add(1)
				}
			}()
		}})
		if err := st.c.Connect(ctx, s.URL, token(t, roomName, fmt.Sprintf("sub%d", i))); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.c.Disconnect(context.Background()) })
		subs = append(subs, st)
	}

	// Every subscriber gets packets on all 6 tracks.
	deadline := time.Now().Add(30 * time.Second)
	for i, st := range subs {
		for {
			n := 0
			st.received.Range(func(_, v any) bool {
				if v.(*atomic.Int64).Load() > 0 {
					n++
				}
				return true
			})
			if n == 6 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("sub%d receives media on %d of 6 tracks", i, n)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	// The capped subscriber is moved off the top layer on at least one simulcast track.
	deadline = time.Now().Add(20 * time.Second)
	for {
		var below int
		for _, p := range s.Manager.Snapshot().Rooms[0].Participants {
			if p.Identity != "sub0" {
				continue
			}
			for _, st := range p.Subscribed {
				for _, sid := range simTracks {
					if st.TrackSID == sid && st.CurrentSpatial >= 0 && st.CurrentSpatial < 2 {
						below++
					}
				}
			}
		}
		if below > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("capped subscriber never downgraded")
		}
		time.Sleep(250 * time.Millisecond)
	}

	if !recordMedia {
		t.Log("ffmpeg not found: recording part skipped")
		return
	}
	// Record pub-h264 to HLS through the real Coordinator + Worker.
	coord := recorder.NewCoordinator(recorder.CoordinatorParams{Config: config.RecordingConfig{WSURL: s.URL, HeartbeatTimeout: time.Minute},
		Rooms: s.Manager, APIKey: apiKey, Secret: apiSecret, Logger: testLogger()})
	defer coord.Close()
	root := t.TempDir()
	wcfg := recorder.DefaultConfig()
	wcfg.WorkerID, wcfg.Capacity, wcfg.Heartbeat = "e2e-worker", 4, 100*time.Millisecond
	wcfg.Storage.LocalRoot = root
	w, err := recorder.NewWorker(wcfg, nil, testLogger(), recorder.WithJobAPI(coord),
		recorder.WithConnector(recorder.ClientConnector{Logger: testLogger(), SettingEngine: client.LoopbackSettingEngine()}))
	if err != nil {
		t.Fatal(err)
	}
	wctx, wcancel := context.WithCancel(ctx)
	defer wcancel()
	go func() { _ = w.Run(wctx) }()
	info, err := coord.StartRecording(ctx, &tesserav1.StartRecordingRequest{Room: roomName,
		Target: &tesserav1.StartRecordingRequest_Participant{Participant: &tesserav1.ParticipantRecordingTarget{Identity: "pub-h264"}}})
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 40*time.Second, "HLS segments", func() bool { return w.Stats().SegmentsWritten >= 4 })
	if _, err := coord.StopRecording(ctx, info.RecordingId); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 20*time.Second, "recording complete", func() bool {
		r, _ := coord.GetRecording(ctx, info.RecordingId)
		return r.Status == tesserav1.RecordingStatus_RECORDING_STATUS_COMPLETE || r.Status == tesserav1.RecordingStatus_RECORDING_STATUS_FAILED
	})
	got, _ := coord.GetRecording(ctx, info.RecordingId)
	if got.Status != tesserav1.RecordingStatus_RECORDING_STATUS_COMPLETE {
		t.Fatalf("recording %v", got)
	}
	master := filepath.Join(root, roomName, info.RecordingId, "master.m3u8")
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_name", "-of", "csv=p=0", master).CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe: %v %s", err, out)
	}
	if !strings.Contains(string(out), "h264") || !strings.Contains(string(out), "opus") {
		t.Fatalf("ffprobe streams: %s", out)
	}
}

func waitUntil(t *testing.T, d time.Duration, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
