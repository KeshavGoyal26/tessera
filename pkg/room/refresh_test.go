package room

import (
	"context"
	"testing"
	"time"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/signal"
)

type captureConn struct{ out []*tesserav1.SignalResponse }

func (c *captureConn) ReadRequest(ctx context.Context) (*tesserav1.SignalRequest, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (c *captureConn) WriteResponse(m *tesserav1.SignalResponse) error {
	c.out = append(c.out, m)
	return nil
}
func (c *captureConn) Close(int, string) error { return nil }
func (c *captureConn) RemoteAddr() string      { return "test" }

// The server re-signs the participant's token with a fresh lifetime, so a client
// can reconnect after the token it joined with has expired.
func TestRefreshTokenIsValidAndLater(t *testing.T) {
	keys := auth.StaticKeys{"k": "secret-secret-secret-secret-0000"}
	mi, err := NewManager(Params{Config: config.Default(), Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	defer mi.Close()
	m := mi.(*manager)
	tok := auth.NewAccessToken("k", keys["k"]).SetIdentity("alice").SetValidFor(10 * time.Minute).
		SetGrant(&auth.RoomGrant{RoomJoin: true, Room: "r"})
	jwt, err := tok.ToJWT()
	if err != nil {
		t.Fatal(err)
	}
	claims, err := auth.NewVerifier(keys).Verify(jwt)
	if err != nil {
		t.Fatal(err)
	}
	r, err := m.getOrCreateRoom("r", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	conn := &captureConn{}
	p := newParticipant(r, claims, signal.ConnectParams{}, conn)
	p.joined.Store(true)
	p.refreshToken()
	if len(conn.out) != 1 {
		t.Fatalf("sent %d messages", len(conn.out))
	}
	fresh := conn.out[0].GetRefreshToken()
	got, err := auth.NewVerifier(keys).Verify(fresh)
	if err != nil {
		t.Fatalf("refreshed token invalid: %v", err)
	}
	if got.Identity() != "alice" || got.Grants.Room != "r" || got.Expiry < claims.Expiry {
		t.Fatalf("refreshed claims %+v", got)
	}
	if ttl := got.Expiry - got.NotBefore; ttl != 600 {
		t.Fatalf("refreshed lifetime %d s, want 600", ttl)
	}
	if d := p.refreshInterval(); d != 5*time.Minute {
		t.Fatalf("refresh every %v", d)
	}
}
