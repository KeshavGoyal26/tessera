package webhook

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
)

// TestContractFixtures verifies and decodes the Go-generated golden webhooks
// (testdata/contract/webhook), the same files the Java SDK tests consume.
func TestContractFixtures(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/contract/webhook/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Key, Secret        string
		T                  int64
		EventID            string `json:"event_id"`
		JSONBody           string `json:"json_body"`
		JSONSig            string `json:"json_signature"`
		RoomName           string `json:"room_name"`
		ProtobufBodyBase64 string `json:"protobuf_body_base64"`
		ProtobufSignature  string `json:"protobuf_signature"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no fixtures")
	}
	for _, c := range cases {
		now := time.Unix(c.T, 0)
		if err := Verify(c.JSONSig, []byte(c.JSONBody), c.Secret, now, DefaultTolerance); err != nil {
			t.Errorf("%s json: %v", c.EventID, err)
		}
		pb, _ := base64.StdEncoding.DecodeString(c.ProtobufBodyBase64)
		if err := Verify(c.ProtobufSignature, pb, c.Secret, now, DefaultTolerance); err != nil {
			t.Errorf("%s protobuf: %v", c.EventID, err)
		}
		var a, b tesserav1.WebhookEvent
		if err := protojson.Unmarshal([]byte(c.JSONBody), &a); err != nil {
			t.Fatal(err)
		}
		if err := proto.Unmarshal(pb, &b); err != nil {
			t.Fatal(err)
		}
		if !proto.Equal(&a, &b) || a.Id != c.EventID {
			t.Errorf("%s: json and protobuf bodies differ", c.EventID)
		}
		if Verify(c.JSONSig, []byte(c.JSONBody+" "), c.Secret, now, DefaultTolerance) == nil {
			t.Errorf("%s: tampered body accepted", c.EventID)
		}
	}
}
