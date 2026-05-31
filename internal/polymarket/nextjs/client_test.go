package nextjs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const htmlWithBuildID = `<!doctype html><html><head><script id="__NEXT_DATA__" type="application/json">
{"buildId":"abc123","page":"/event/[slug]"}
</script></head><body>ok</body></html>`

func TestResolveBuildID_ParsesAndCaches(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Write([]byte(htmlWithBuildID))
	}))
	defer srv.Close()
	c := New(srv.URL, 2*time.Second, 10*time.Minute)
	id, err := c.ResolveBuildID(context.Background(), "demo-event")
	if err != nil || id != "abc123" {
		t.Fatalf("expected abc123, got %q err=%v", id, err)
	}
	_, _ = c.ResolveBuildID(context.Background(), "demo-event")
	if atomic.LoadInt64(&hits) != 1 {
		t.Fatalf("expected cache hit; got %d html fetches", hits)
	}
}

func TestFetchEvent_RefreshesBuildIDOn404(t *testing.T) {
	var htmlHits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/event/"):
			atomic.AddInt64(&htmlHits, 1)
			if atomic.LoadInt64(&htmlHits) == 1 {
				w.Write([]byte(strings.Replace(htmlWithBuildID, "abc123", "stale", 1)))
			} else {
				w.Write([]byte(strings.Replace(htmlWithBuildID, "abc123", "fresh", 1)))
			}
		case strings.HasPrefix(r.URL.Path, "/_next/data/stale/"):
			w.WriteHeader(http.StatusNotFound)
		case strings.HasPrefix(r.URL.Path, "/_next/data/fresh/"):
			w.Write([]byte(`{"pageProps":{"dehydratedState":{"queries":[]}}}`))
		default:
			t.Fatalf("unexpected path: %v", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, 2*time.Second, 10*time.Minute)
	p, err := c.FetchEvent(context.Background(), "s")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if p == nil {
		t.Fatalf("expected payload")
	}
	if atomic.LoadInt64(&htmlHits) < 2 {
		t.Fatalf("expected 2+ HTML hits, got %d", htmlHits)
	}
}

func TestFetchEvent_MalformedDoesNotPanic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/event/") {
			w.Write([]byte(htmlWithBuildID))
			return
		}
		w.Write([]byte(`{not json`))
	}))
	defer srv.Close()
	c := New(srv.URL, 2*time.Second, 10*time.Minute)
	_, err := c.FetchEvent(context.Background(), "x")
	if err == nil {
		t.Fatalf("expected parse error")
	}
}

func TestParse_AnnotationsExtracted(t *testing.T) {
	payload := []byte(`{
	  "pageProps":{
	    "dehydratedState":{
	      "queries":[
	        {"queryKey":["annotations","event","x"],"state":{"data":[
	          {"title":"Big news","body":"Macron resigned","url":"https://news/1","timestamp":1700000000}
	        ]}},
	        {"queryKey":["/api/event/slug","x"],"state":{"data":{
	          "slug":"x","title":"Event Title","news":[
	            {"title":"From event.news","summary":"context","url":"https://news/2","createdAt":"2024-01-01T00:00:00Z"}
	          ],
	          "timeline":[
	            {"title":"Timeline item","date":"2024-02-01","url":"https://news/3"}
	          ]
	        }}}
	      ]
	    }
	  }
	}`)
	ep, err := parseEventPayload(payload, "x")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ep.Title != "Event Title" {
		t.Fatalf("expected Event Title, got %q", ep.Title)
	}
	if len(ep.News) < 3 {
		t.Fatalf("expected 3+ news items, got %d", len(ep.News))
	}
	sources := map[string]bool{}
	for _, n := range ep.News {
		sources[n.Source] = true
	}
	for _, want := range []string{"annotations", "event.news", "timeline"} {
		if !sources[want] {
			t.Fatalf("missing source %q in extracted items: %v", want, sources)
		}
	}
}

func TestParse_EmptyDehydratedState(t *testing.T) {
	payload := []byte(`{"pageProps":{"dehydratedState":{"queries":[]}}}`)
	ep, err := parseEventPayload(payload, "x")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ep.News) != 0 {
		t.Fatalf("expected no news for empty state")
	}
}
