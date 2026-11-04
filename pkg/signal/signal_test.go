package signal

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
)

func TestParseConnectParams(t *testing.T) {
	q, _ := url.ParseQuery("protocol=1&auto_subscribe=0&sdk=js&version=0.1.0&reconnect=1&sid=PA_x&format=json")
	p, err := ParseConnectParams(q, "1.2.3.4:5")
	if err != nil {
		t.Fatal(err)
	}
	if p.Protocol != 1 || p.AutoSubscribe || !p.Reconnect || p.ParticipantSID != "PA_x" || !p.JSON {
		t.Fatalf("%+v", p)
	}
	if p.Client.Sdk != tesserav1.ClientInfo_SDK_JS || p.Client.Address != "1.2.3.4:5" {
		t.Fatalf("%+v", p.Client)
	}
}

func TestParseConnectParamsErrors(t *testing.T) {
	for _, raw := range []string{"", "protocol=x", "protocol=99", "protocol=1&reconnect=1"} {
		q, _ := url.ParseQuery(raw)
		if _, err := ParseConnectParams(q, ""); err == nil {
			t.Errorf("%q: expected error", raw)
		}
	}
	q, _ := url.ParseQuery("protocol=99")
	if _, err := ParseConnectParams(q, ""); !errors.Is(err, errs.ErrFailedPrecondition) {
		t.Fatalf("unsupported protocol must be failed_precondition, got %v", err)
	}
}

func TestUnsupportedProtocolIs426(t *testing.T) {
	s := NewServer(config.Default().Signal, nil, nil, nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/rtc?protocol=99", nil))
	if rec.Code != http.StatusUpgradeRequired {
		t.Fatalf("status %d", rec.Code)
	}
}
