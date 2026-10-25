package main

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
)

// The rest of a polled repeater's status reply (neighbor_status, written by
// the ingestor beside each neighbor_battery row), its chip temperature, and
// the rule for hiding readings nobody has refreshed.

const defaultHideAfterHours = 24

// NeighborBatteryServerConfig is the server's part of the neighborBattery
// block. HideAfterHours: a Battery or Temperature line whose latest reading
// is older than this is hidden; 0 never hides. Unset means 24.
type NeighborBatteryServerConfig struct {
	HideAfterHours *int `json:"hideAfterHours,omitempty"`
}

// NeighborBatteryHideAfterHours returns neighborBattery.hideAfterHours, or 24.
func (c *Config) NeighborBatteryHideAfterHours() int {
	if c.NeighborBattery != nil && c.NeighborBattery.HideAfterHours != nil && *c.NeighborBattery.HideAfterHours >= 0 {
		return *c.NeighborBattery.HideAfterHours
	}
	return defaultHideAfterHours
}

// isStale reports whether ts is older than hideAfter hours (0 never is).
// An unreadable time counts as stale: nothing can say it is recent.
func isStale(ts string, hideAfter int, now time.Time) bool {
	if hideAfter <= 0 {
		return false
	}
	at, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return true
	}
	return now.Sub(at) > time.Duration(hideAfter)*time.Hour
}

func (db *DB) columnPresent(flag interface {
	Load() bool
	Store(bool)
}, query string, args ...interface{}) bool {
	if flag.Load() {
		return true
	}
	var n int
	if err := db.conn.QueryRow(query, args...).Scan(&n); err != nil || n == 0 {
		return false
	}
	flag.Store(true)
	return true
}

// neighborBatteryV2Present reports whether neighbor_battery has the
// temperature_c and via columns; re-probed until it does.
func (db *DB) neighborBatteryV2Present() bool {
	return db.columnPresent(&db.neighborBatteryV2,
		`SELECT COUNT(*) FROM pragma_table_info('neighbor_battery') WHERE name IN ('temperature_c', 'via') HAVING COUNT(*) = 2`)
}

// neighborStatusPresent reports whether neighbor_status exists.
func (db *DB) neighborStatusPresent() bool {
	return db.columnPresent(&db.neighborStatusTable,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'neighbor_status'`)
}

// latestTemperature is the newest temperature for a node: an observer's
// telemetry reading if there is one in the window, else the one in the
// node's own adverts. nil when neither exists.
func latestTemperature(samples []NodeBatterySample, node map[string]interface{}, hideAfter int, now time.Time) map[string]interface{} {
	for i := len(samples) - 1; i >= 0; i-- {
		s := samples[i]
		if s.TemperatureC == nil {
			continue
		}
		return map[string]interface{}{
			"celsius":       *s.TemperatureC,
			"timestamp":     s.Timestamp,
			"source":        batterySourceNeighbor,
			"observer_id":   s.ObserverID,
			"observer_name": s.ObserverName,
			"stale":         isStale(s.Timestamp, hideAfter, now),
		}
	}
	t, ok := node["temperature_c"].(float64)
	if !ok {
		return nil
	}
	ts, _ := node["last_seen"].(string)
	return map[string]interface{}{
		"celsius":   t,
		"timestamp": ts,
		"source":    "advert",
		"stale":     isStale(ts, hideAfter, now),
	}
}

// statusColumns are neighbor_status's stat columns, in the order scanned.
var statusColumns = []string{
	"uptime_secs", "noise_floor", "last_rssi", "last_snr", "tx_queue", "packets_recv", "packets_sent",
	"recv_flood", "recv_direct", "sent_flood", "sent_direct", "tx_air_secs", "rx_air_secs",
	"flood_dups", "direct_dups", "recv_errors", "err_events",
}

// rateColumns are the cumulative counters turned into per-hour rates.
var rateColumns = []string{
	"recv_flood", "recv_direct", "sent_flood", "sent_direct", "tx_air_secs", "rx_air_secs",
	"flood_dups", "direct_dups", "recv_errors",
}

// StatusSample is one status reply as stored, plus what changed since the
// previous one from the same observer.
type StatusSample struct {
	Timestamp string              `json:"timestamp"`
	Values    map[string]*float64 `json:"values"`
	// PerHour: each counter's change per hour since the previous sample;
	// absent after a reboot or when a counter is missing.
	PerHour map[string]float64 `json:"per_hour,omitempty"`
	// DupRate: share of flood packets received that were duplicates, from
	// the same interval.
	DupRate *float64 `json:"dup_rate,omitempty"`
	// Reboot: uptime went down since the previous sample.
	Reboot bool `json:"reboot,omitempty"`
}

// StatusSeries is one observer's status samples for a node.
type StatusSeries struct {
	ObserverID   string         `json:"observer_id"`
	ObserverName string         `json:"observer_name,omitempty"`
	Samples      []StatusSample `json:"samples"`
	// Stale: this observer's newest sample is older than
	// neighborBattery.hideAfterHours. The UI hides a stale series' charts.
	Stale bool `json:"stale"`
}

// GetNeighborStatusHistory returns a node's status samples since the given
// time, one series per observer, most samples first.
func (db *DB) GetNeighborStatusHistory(pubkey, since string) ([]StatusSeries, error) {
	if pubkey == "" || !db.neighborStatusPresent() {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339, since); err == nil {
		since = t.UTC().Format("2006-01-02T15:04:05Z")
	}
	rows, err := db.conn.Query(`
		SELECT observer_id, sampled_at, `+strings.Join(statusColumns, ", ")+`
		FROM neighbor_status
		WHERE node_id = ? AND sampled_at >= ?
		ORDER BY observer_id, sampled_at ASC`, strings.ToLower(pubkey), since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []StatusSeries
	observers := map[string]string{}
	for rows.Next() {
		var obs, ts string
		vals := make([]*float64, len(statusColumns))
		dest := []interface{}{&obs, &ts}
		for i := range vals {
			dest = append(dest, &vals[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].ObserverID != obs {
			out = append(out, StatusSeries{ObserverID: obs})
			observers[obs] = ""
		}
		s := StatusSample{Timestamp: ts, Values: map[string]*float64{}}
		for i, c := range statusColumns {
			s.Values[c] = vals[i]
		}
		series := &out[len(out)-1]
		series.Samples = append(series.Samples, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	db.fillObserverNames(observers)
	for i := range out {
		out[i].ObserverName = observers[out[i].ObserverID]
		addStatusRates(out[i].Samples)
	}
	// Most samples first, so a client wanting one series takes the first.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && len(out[j].Samples) > len(out[j-1].Samples); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// addStatusRates fills PerHour, DupRate and Reboot from consecutive samples.
// A counter that went down means the repeater restarted (its counters start
// again from zero), so that interval gets no rates.
func addStatusRates(samples []StatusSample) {
	for i := 1; i < len(samples); i++ {
		prev, cur := &samples[i-1], &samples[i]
		t0, e0 := time.Parse(time.RFC3339, prev.Timestamp)
		t1, e1 := time.Parse(time.RFC3339, cur.Timestamp)
		if e0 != nil || e1 != nil || !t1.After(t0) {
			continue
		}
		if u0, u1 := prev.Values["uptime_secs"], cur.Values["uptime_secs"]; u0 != nil && u1 != nil && *u1 < *u0 {
			cur.Reboot = true
			continue
		}
		hours := t1.Sub(t0).Hours()
		rates := map[string]float64{}
		for _, c := range rateColumns {
			a, b := prev.Values[c], cur.Values[c]
			if a == nil || b == nil || *b < *a {
				continue
			}
			rates[c] = (*b - *a) / hours
		}
		if len(rates) > 0 {
			cur.PerHour = rates
		}
		f0, f1 := prev.Values["recv_flood"], cur.Values["recv_flood"]
		d0, d1 := prev.Values["flood_dups"], cur.Values["flood_dups"]
		if f0 != nil && f1 != nil && d0 != nil && d1 != nil && *f1 > *f0 && *d1 >= *d0 {
			r := (*d1 - *d0) / (*f1 - *f0)
			if r > 1 {
				r = 1
			}
			cur.DupRate = &r
		}
	}
}

// handleNodeStatusHistory serves GET /api/nodes/{pubkey}/status-history?days=N:
// the status replies observers published for a node, per observer, with
// hourly rates and reboots worked out. series is empty when none exist.
func (s *Server) handleNodeStatusHistory(w http.ResponseWriter, r *http.Request) {
	pubkey := mux.Vars(r)["pubkey"]
	node, err := s.db.GetNodeByPubkey(pubkey)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if node == nil {
		writeError(w, 404, "node not found")
		return
	}
	days := 7
	if d, _ := strconv.Atoi(r.URL.Query().Get("days")); d > 0 && d <= 365 {
		days = d
	}
	since := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339)
	series, err := s.db.GetNeighborStatusHistory(pubkey, since)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if series == nil {
		series = []StatusSeries{}
	}
	hideAfter := s.cfg.NeighborBatteryHideAfterHours()
	now := time.Now().UTC()
	for i := range series {
		if n := len(series[i].Samples); n > 0 {
			series[i].Stale = isStale(series[i].Samples[n-1].Timestamp, hideAfter, now)
		}
	}
	writeJSON(w, map[string]interface{}{
		"public_key":       strings.ToLower(pubkey),
		"days":             days,
		"series":           series,
		"hide_after_hours": hideAfter,
	})
}
