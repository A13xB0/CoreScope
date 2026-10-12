package main

// Health notifications and the Atom feed. Both are read from the same
// status the page shows; nothing here transmits on the mesh.

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

// netWatchNotifier posts incidents as they open and close. The first status
// it sees only primes it, so a restart does not repost everything open.
type netWatchNotifier struct {
	cfg    NetWatchNotify
	client *http.Client

	mu     sync.Mutex
	primed bool
	open   map[string]NetWatchIncident // node -> the incident posted as open
	feed   bool
}

func (n *netWatchNotifier) update(ctx context.Context, st NetWatchResponse) {
	if !st.Ready {
		return
	}
	n.mu.Lock()
	now := map[string]NetWatchIncident{}
	for _, i := range st.Open {
		now[i.Node] = i
	}
	if !n.primed {
		n.primed, n.open, n.feed = true, now, st.Feed.Down
		n.mu.Unlock()
		return
	}
	var msgs []string
	if st.Feed.Down != n.feed {
		if st.Feed.Down {
			msgs = append(msgs, "The packet feed has stopped: every observer went quiet at once. Nothing is judged until it is back.")
		} else {
			msgs = append(msgs, "The packet feed is back.")
		}
		n.feed = st.Feed.Down
	}
	for pk, i := range now {
		if _, was := n.open[pk]; !was {
			msgs = append(msgs, n.opened(i))
		}
	}
	for pk, i := range n.open {
		if _, still := now[pk]; !still {
			msgs = append(msgs, n.closed(i, st.Recent))
		}
	}
	n.open = now
	n.mu.Unlock()
	for _, m := range msgs {
		n.post(ctx, m)
	}
}

func (n *netWatchNotifier) link() string {
	if n.cfg.BaseURL == "" {
		return ""
	}
	return " " + strings.TrimRight(n.cfg.BaseURL, "/") + "/#/health"
}

func (n *netWatchNotifier) opened(i NetWatchIncident) string {
	what := map[string]string{"not-relaying": "stopped relaying, though it is still heard", "slowed": "slowed right down",
		"unheard": "has not been heard for longer than usual"}[i.Kind]
	if what == "" {
		what = "went quiet"
	}
	if i.Tier != "" {
		what += " (" + i.Tier + " repeater)"
	}
	win := i.WindowH
	if win == 0 {
		win = 6
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s (since %s UTC). Usually %d relays in %d h, now %d", i.Name, what, shortHour(i.Start), i.Usual, win, i.Now)
	if i.Chance > 0 {
		fmt.Fprintf(&b, "; a quiet spell like this happens by chance about %s", oneIn(i.Chance))
	}
	b.WriteString(".")
	if len(i.Affects) > 0 {
		fmt.Fprintf(&b, " Affects: %s.", strings.Join(i.Affects, "; "))
	}
	b.WriteString(n.link())
	return b.String()
}

func (n *netWatchNotifier) closed(i NetWatchIncident, recent []NetWatchIncident) string {
	for _, r := range recent {
		if r.Node == i.Node && r.End != nil {
			return fmt.Sprintf("%s is back (%s UTC).%s", i.Name, shortHour(*r.End), n.link())
		}
	}
	return fmt.Sprintf("%s is back.%s", i.Name, n.link())
}

func (n *netWatchNotifier) post(ctx context.Context, text string) {
	var body any
	switch n.cfg.Format {
	case "slack":
		body = map[string]string{"text": text}
	case "json":
		body = map[string]string{"source": "corescope-health", "text": text}
	default:
		body = map[string]string{"content": text}
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.cfg.Webhook, bytes.NewReader(b))
	if err != nil {
		log.Printf("[netwatch] notify: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		log.Printf("[netwatch] notify: %v", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("[netwatch] notify: webhook answered %s", resp.Status)
	}
}

func shortHour(iso string) string {
	if t, err := time.Parse(time.RFC3339, iso); err == nil {
		return t.UTC().Format("15:04 2 Jan")
	}
	return iso
}

// oneIn says a small probability as "1 in 10 thousand" and the like.
func oneIn(p float64) string {
	if p <= 0 {
		return "never"
	}
	e := math.Floor(-math.Log10(p))
	switch {
	case e < 3:
		return fmt.Sprintf("1 in %.0f", 1/p)
	case e < 6:
		return fmt.Sprintf("1 in %.0f thousand", math.Pow(10, e-3))
	case e < 9:
		return fmt.Sprintf("1 in %.0f million", math.Pow(10, e-6))
	}
	return fmt.Sprintf("less than 1 in 10^%.0f", e)
}

type atomFeed struct {
	XMLName xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	Title   string      `xml:"title"`
	ID      string      `xml:"id"`
	Updated string      `xml:"updated"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	Title   string `xml:"title"`
	ID      string `xml:"id"`
	Updated string `xml:"updated"`
	Summary string `xml:"summary"`
}

// handleNetWatchFeed is GET /api/netwatch/feed.atom: the timeline as Atom.
func (s *Server) handleNetWatchFeed(w http.ResponseWriter, r *http.Request) {
	nw := s.netWatchOr404(w)
	if nw == nil {
		return
	}
	st := nw.response()
	f := atomFeed{Title: "Mesh health", ID: "urn:corescope:netwatch", Updated: st.Hour}
	for _, e := range st.Events {
		f.Entries = append(f.Entries, atomEntry{Title: e.Text, ID: "urn:corescope:netwatch:" + e.Kind + ":" + e.Hour + ":" + e.Node + ":" + e.Where,
			Updated: e.Hour, Summary: e.Text + " (" + shortHour(e.Hour) + " UTC)"})
	}
	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Encode(f)
}
