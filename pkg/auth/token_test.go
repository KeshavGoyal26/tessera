package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
)

const (
	testKey    = "APItest"
	testSecret = "0123456789abcdef0123456789abcdef"
)

func TestRoundTrip(t *testing.T) {
	tok, err := NewAccessToken(testKey, testSecret).
		SetIdentity("alice").SetName("Alice").SetMetadata(`{"a":1}`).
		SetGrant(&RoomGrant{Room: "r1", RoomJoin: true, CanPublish: Bool(false), CanPublishSources: []string{"camera"}}).
		SetValidFor(time.Minute).ToJWT()
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewVerifier(StaticKeys{testKey: testSecret}).Verify(tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.Identity() != "alice" || c.APIKey() != testKey || c.Grants.Room != "r1" || !c.Grants.RoomJoin {
		t.Fatalf("claims: %+v", c)
	}
	p := c.Grants.ToPermission()
	if p.CanPublish || !p.CanSubscribe || !p.CanPublishData {
		t.Fatalf("permission defaults: %+v", p)
	}
	if len(p.CanPublishSources) != 1 || p.CanPublishSources[0] != tesserav1.TrackSource_TRACK_SOURCE_CAMERA {
		t.Fatalf("sources: %v", p.CanPublishSources)
	}
}

func TestVerifyRejects(t *testing.T) {
	v := NewVerifier(StaticKeys{testKey: testSecret})
	good, _ := NewAccessToken(testKey, testSecret).SetIdentity("a").ToJWT()
	expired := NewAccessToken(testKey, testSecret).SetIdentity("a")
	expired.now = func() time.Time { return time.Now().Add(-48 * time.Hour) }
	expiredTok, _ := expired.ToJWT()
	otherKey, _ := NewAccessToken("other", testSecret).ToJWT()
	parts := strings.Split(good, ".")
	noneAlg := "eyJhbGciOiJub25lIn0." + parts[1] + "."

	for name, tok := range map[string]string{
		"garbage":       "abc",
		"tampered":      parts[0] + "." + parts[1] + "x." + parts[2],
		"bad signature": parts[0] + "." + parts[1] + "." + strings.Repeat("A", len(parts[2])),
		"expired":       expiredTok,
		"unknown key":   otherKey,
		"alg none":      noneAlg,
	} {
		if _, err := v.Verify(tok); !errors.Is(err, errs.ErrUnauthenticated) {
			t.Errorf("%s: want unauthenticated, got %v", name, err)
		}
	}
}

func TestJoinRequiresIdentity(t *testing.T) {
	_, err := NewAccessToken(testKey, testSecret).SetGrant(&RoomGrant{RoomJoin: true, Room: "r"}).ToJWT()
	if !errors.Is(err, errs.ErrInvalidArgument) {
		t.Fatalf("got %v", err)
	}
}

func TestClaimNamesContract(t *testing.T) {
	tok, _ := NewAccessToken(testKey, testSecret).SetIdentity("bob").
		SetGrant(&RoomGrant{Room: "r", RoomJoin: true, RoomRecord: true, CanSubscribe: Bool(true), Hidden: true}).ToJWT()
	payload := strings.Split(tok, ".")[1]
	for _, name := range []string{`"iss"`, `"sub"`, `"exp"`, `"nbf"`, `"grants"`, `"room_join"`, `"room_record"`, `"can_subscribe"`, `"hidden"`} {
		if !strings.Contains(decode(t, payload), name) {
			t.Errorf("claim %s missing", name)
		}
	}
}
