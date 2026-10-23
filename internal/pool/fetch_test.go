package pool

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchAllHandlesEveryShape(t *testing.T) {
	list := "vless://a@one.example:443?type=ws#🇮🇹\nvless://b@two.example:443?type=ws#🇫🇷\n"
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(list))
	}))
	defer plain.Close()

	encoded := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(base64.StdEncoding.EncodeToString([]byte(list))))
	}))
	defer encoded.Close()

	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body>mirror is down</body></html>"))
	}))
	defer html.Close()

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()

	urls := []string{plain.URL, encoded.URL, html.URL, broken.URL}
	subs := FetchAll(context.Background(), urls, 5*time.Second, plain.Client())

	if got := len(subs[0].Nodes); got != 2 {
		t.Fatalf("plain: %d nodes, err %v", got, subs[0].Err)
	}
	if got := len(subs[1].Nodes); got != 2 {
		t.Fatalf("base64: %d nodes, err %v", got, subs[1].Err)
	}
	if subs[2].Err == nil {
		t.Fatal("an html page must not be accepted as a subscription")
	}
	if subs[3].Err == nil {
		t.Fatal("a 500 must be reported")
	}
}

func TestMergeDeduplicatesAcrossSubscriptions(t *testing.T) {
	a, err := ParseLink("vless://x@one.example:443?type=ws#first")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseLink("vless://x@one.example:443?type=ws#second")
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseLink("vless://y@two.example:443?type=ws#third")
	if err != nil {
		t.Fatal(err)
	}
	subs := []Subscription{
		{Nodes: []Node{a}, Skipped: []string{"junk"}},
		{Nodes: []Node{b, c}},
	}
	nodes, unsupported := Merge(subs, 0)
	if len(nodes) != 2 {
		t.Fatalf("merged to %d nodes", len(nodes))
	}
	if unsupported != 1 {
		t.Fatalf("unsupported %d", unsupported)
	}
	if nodes[0].Label != "first" {
		t.Fatalf("kept the wrong label: %q", nodes[0].Label)
	}

	capped, _ := Merge(subs, 1)
	if len(capped) != 1 {
		t.Fatalf("cap ignored: %d", len(capped))
	}
}
