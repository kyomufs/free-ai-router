package pool

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"free-ai-router/internal/provider"
)

// standIn answers Classify the way the real provider does for the two verdicts the
// pool cares about, so the plumbing can be tested without the network.
type standIn struct {
	url string
}

func (s standIn) ID() provider.ID { return "standin" }
func (s standIn) Models(context.Context) ([]provider.Model, error) {
	return []provider.Model{{ID: "m", Free: true}}, nil
}

func (s standIn) Build(provider.Request) (provider.Prepared, error) {
	return provider.Prepared{
		URL:     s.url + "/completion",
		Headers: http.Header{"content-type": []string{"application/json"}},
		Body:    []byte(`{"model":"m","stream":true}`),
	}, nil
}

func (s standIn) Classify(resp *http.Response, body []byte) provider.Failure {
	f := provider.Failure{Status: resp.StatusCode, Body: body}
	if strings.Contains(string(body), "abusive traffic") {
		f.AddressRejected = true
	}
	if strings.Contains(string(body), "insufficient_quota") {
		f.KeyRejected = true
	}
	return f
}

func judgeServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/completion", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	})
	mux.HandleFunc("/trace", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ip=203.0.113.7\nwarp=off\n"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestProberAcceptsAndRecordsExitIP(t *testing.T) {
	srv := judgeServer(t, http.StatusOK, "data: {\"choices\":[]}\n\n")
	p := Prober{
		Provider: standIn{url: srv.URL},
		Model:    provider.Model{ID: "m"},
		TraceURL: srv.URL + "/trace",
		Timeout:  2 * time.Second,
	}
	v := p.Against(context.Background(), srv.Client().Transport)
	if !v.Accepted || v.AddressRefused {
		t.Fatalf("verdict %+v", v)
	}
	if v.ExitIP != "203.0.113.7" {
		t.Fatalf("exit ip %q", v.ExitIP)
	}
	if v.RTT <= 0 {
		t.Fatal("rtt not measured")
	}
}

func TestProberFlagsAddressRefusal(t *testing.T) {
	srv := judgeServer(t, http.StatusForbidden, `{"error":{"message":"we have detected abusive traffic"}}`)
	p := Prober{Provider: standIn{url: srv.URL}, Model: provider.Model{ID: "m"}, TraceURL: srv.URL + "/trace", Timeout: 2 * time.Second}
	v := p.Against(context.Background(), srv.Client().Transport)
	if !v.AddressRefused || v.Accepted {
		t.Fatalf("verdict %+v", v)
	}
}

// TestProberJudgesAQuotaRefusalByWhoOwnsTheQuota: with a key the quota sits on the
// credential and the gateway rotates keys, so the node stays. Without one the free
// lane hands quota out per exit address, and a node that answers "limit reached"
// would pass exactly that refusal on to clients.
func TestProberJudgesAQuotaRefusalByWhoOwnsTheQuota(t *testing.T) {
	srv := judgeServer(t, http.StatusTooManyRequests, `{"error":{"code":"insufficient_quota"}}`)
	base := Prober{Provider: standIn{url: srv.URL}, Model: provider.Model{ID: "m"}, TraceURL: srv.URL + "/trace", Timeout: 2 * time.Second}

	keyed := base
	keyed.Key = func() string { return "sk-test" }
	if v := keyed.Against(context.Background(), srv.Client().Transport); !v.Accepted || v.AddressRefused {
		t.Fatalf("a quota on the key is not an address problem: %+v", v)
	}

	keyless := base
	v := keyless.Against(context.Background(), srv.Client().Transport)
	if v.Accepted || v.AddressRefused {
		t.Fatalf("a spent free lane must not be installed: %+v", v)
	}
	if v.Reason == "" {
		t.Fatal("a rejected verdict must say why")
	}
}

func TestProberReportsABrokenTunnel(t *testing.T) {
	p := Prober{Provider: standIn{url: "http://127.0.0.1:1"}, Model: provider.Model{ID: "m"}, Timeout: 500 * time.Millisecond}
	v := p.Against(context.Background(), &http.Transport{})
	if v.Accepted || v.AddressRefused {
		t.Fatalf("verdict %+v", v)
	}
	if !strings.Contains(v.Reason, "tunnel") {
		t.Fatalf("reason %q", v.Reason)
	}
}
