package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFromOptionsDefault(t *testing.T) {
	s, err := FromOptions(context.Background(), nil, nil)
	if err != nil || s != nil {
		t.Fatalf("nil options must return default: %v %v", s, err)
	}
}

func TestLocal(t *testing.T) {
	root := t.TempDir()
	s, err := NewLocal(root, WithBaseURL("http://cdn/rec/"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	loc, err := s.Put(ctx, "r1/video/index.m3u8", strings.NewReader("v1"), 2, ContentTypePlaylist)
	if err != nil || loc != filepath.Join(root, "r1/video/index.m3u8") {
		t.Fatalf("%q %v", loc, err)
	}
	if _, err := s.Put(ctx, "r1/video/index.m3u8", strings.NewReader("v2"), 2, ContentTypePlaylist); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(loc)
	if string(b) != "v2" {
		t.Fatalf("overwrite: %q", b)
	}
	ents, _ := os.ReadDir(filepath.Dir(loc))
	if len(ents) != 1 {
		t.Fatalf("temp files left behind: %v", ents)
	}
	if s.URL("r1/video/index.m3u8") != "http://cdn/rec/r1/video/index.m3u8" {
		t.Fatal(s.URL("r1/video/index.m3u8"))
	}
	for _, bad := range []string{"../x", "a/../../x", ""} {
		if _, err := s.Put(ctx, bad, strings.NewReader("x"), 1, ""); err == nil {
			t.Fatalf("key %q accepted", bad)
		}
	}
	if err := s.Delete(ctx, "r1/video/index.m3u8"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "r1/video/index.m3u8"); err != nil {
		t.Fatal("delete must be idempotent")
	}
	if err := s.Check(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRetry(t *testing.T) {
	n := 0
	err := Retry(context.Background(), 4, time.Millisecond, func() error {
		n++
		if n < 3 {
			return errors.New("boom")
		}
		return nil
	})
	if err != nil || n != 3 {
		t.Fatalf("%v %d", err, n)
	}
	n = 0
	err = Retry(context.Background(), 2, time.Millisecond, func() error { n++; return errors.New("boom") })
	if err == nil || n != 2 {
		t.Fatalf("%v %d", err, n)
	}
	n = 0
	err = Retry(context.Background(), 5, time.Millisecond, func() error { n++; return permanent{errors.New("no")} })
	if err == nil || n != 1 {
		t.Fatalf("permanent errors must not retry: %d", n)
	}
}

// fakeS3 implements the subset of S3 used by Put: single PUT, multipart, HEAD bucket.
type fakeS3 struct {
	mu        sync.Mutex
	objects   map[string][]byte
	uploads   map[string]map[int][]byte
	failFirst atomic.Int32
	multipart atomic.Int32
	ctype     map[string]string
}

func newFakeS3() *fakeS3 {
	return &fakeS3{objects: map[string][]byte{}, uploads: map[string]map[int][]byte{}, ctype: map[string]string{}}
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	q := r.URL.Query()
	switch {
	case r.Method == http.MethodHead && !strings.Contains(strings.TrimSuffix(path, "/"), "/"), r.Method == http.MethodGet && q.Has("location"):
		w.WriteHeader(200)
	case r.Method == http.MethodPost && q.Has("uploads"):
		f.multipart.Add(1)
		id := fmt.Sprintf("up%d", f.multipart.Load())
		f.mu.Lock()
		f.uploads[id] = map[int][]byte{}
		f.mu.Unlock()
		_, _ = fmt.Fprintf(w, `<InitiateMultipartUploadResult><Bucket>b</Bucket><Key>k</Key><UploadId>%s</UploadId></InitiateMultipartUploadResult>`, id)
	case r.Method == http.MethodPut && q.Get("uploadId") != "":
		var n int
		_, _ = fmt.Sscan(q.Get("partNumber"), &n)
		b := readBody(r)
		f.mu.Lock()
		f.uploads[q.Get("uploadId")][n] = b
		f.mu.Unlock()
		w.Header().Set("ETag", fmt.Sprintf(`"etag%d"`, n))
	case r.Method == http.MethodPost && q.Get("uploadId") != "":
		var req struct {
			Parts []struct{ PartNumber int } `xml:"Part"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = xml.Unmarshal(b, &req)
		f.mu.Lock()
		var all []byte
		for _, p := range req.Parts {
			all = append(all, f.uploads[q.Get("uploadId")][p.PartNumber]...)
		}
		f.objects[path] = all
		f.mu.Unlock()
		_, _ = fmt.Fprint(w, `<CompleteMultipartUploadResult><Location>x</Location><Bucket>b</Bucket><Key>k</Key><ETag>"done"</ETag></CompleteMultipartUploadResult>`)
	case r.Method == http.MethodPut:
		if f.failFirst.Add(-1) >= 0 {
			w.WriteHeader(503)
			return
		}
		b := readBody(r)
		f.mu.Lock()
		f.objects[path] = b
		f.ctype[path] = r.Header.Get("Content-Type")
		f.mu.Unlock()
		w.Header().Set("ETag", `"e"`)
	case r.Method == http.MethodDelete:
		f.mu.Lock()
		delete(f.objects, path)
		f.mu.Unlock()
		w.WriteHeader(204)
	default:
		w.WriteHeader(404)
	}
}

func TestS3FakeServer(t *testing.T) {
	fake := newFakeS3()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	s, err := NewS3FromConfig(context.Background(), S3Config{
		Bucket: "rec", Endpoint: srv.URL, Region: "us-east-1", AccessKey: "a", Secret: "s", Prefix: "/pre/",
		ForcePathStyle: true, PartSize: 5 << 20, Backoff: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Check(ctx); err != nil {
		t.Fatal(err)
	}
	// Small object, first attempt fails with 503 (retried).
	fake.failFirst.Store(1)
	loc, err := s.Put(ctx, "r/seg_00001.m4s", strings.NewReader("hello"), 5, ContentTypeSegment)
	if err != nil || loc != "s3://rec/pre/r/seg_00001.m4s" {
		t.Fatalf("%q %v", loc, err)
	}
	if string(fake.objects["rec/pre/r/seg_00001.m4s"]) != "hello" || fake.ctype["rec/pre/r/seg_00001.m4s"] != ContentTypeSegment {
		t.Fatalf("%v %v", fake.objects, fake.ctype)
	}
	// Large object uses multipart; non-seekable reader.
	big := make([]byte, 12<<20)
	rand.Read(big)
	if _, err := s.Put(ctx, "r/big.m4s", io.MultiReader(bytes.NewReader(big)), -1, ContentTypeSegment); err != nil {
		t.Fatal(err)
	}
	if fake.multipart.Load() == 0 {
		t.Fatal("expected multipart upload")
	}
	if !bytes.Equal(fake.objects["rec/pre/r/big.m4s"], big) {
		t.Fatal("multipart content mismatch")
	}
	if got := s.URL("r/index.m3u8"); got != srv.URL+"/rec/pre/r/index.m3u8" {
		t.Fatal(got)
	}
	if err := s.Delete(ctx, "r/seg_00001.m4s"); err != nil {
		t.Fatal(err)
	}
}

// readBody decodes aws-chunked streaming bodies used by minio-go over plain HTTP.
func readBody(r *http.Request) []byte {
	raw, _ := io.ReadAll(r.Body)
	if !strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING-") {
		return raw
	}
	var out []byte
	for len(raw) > 0 {
		i := bytes.Index(raw, []byte("\r\n"))
		if i < 0 {
			break
		}
		var n int
		_, _ = fmt.Sscanf(strings.SplitN(string(raw[:i]), ";", 2)[0], "%x", &n)
		raw = raw[i+2:]
		if n == 0 || n > len(raw) {
			break
		}
		out = append(out, raw[:n]...)
		raw = raw[n:]
		raw = bytes.TrimPrefix(raw, []byte("\r\n"))
	}
	return out
}
