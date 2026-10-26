package main

import (
	"strings"
	"time"
)

// The Battery column on the nodes list: each repeater/room row of /api/nodes
// carries a compact {mv, ts, status, stale} for its latest battery reading —
// the newer of its own status reports (observer_metrics) and the readings
// observers took from it (neighbor_battery). Computed with one grouped query
// per source for the whole page, and only when neighborBattery is enabled.
// The /api/nodes response cache (nodesResponseTTL, 15 s) is far shorter than
// the polling interval, so the column needs no invalidation of its own.

// nodeListBatteryWindow bounds how far back the column looks, the same week
// the node page's Battery line reads (/battery?days=7).
const nodeListBatteryWindow = 7 * 24 * time.Hour

// nodeListBatteryChunk keeps each IN list well under SQLite's variable limit
// (each key is bound twice for observer_metrics).
const nodeListBatteryChunk = 4000

type latestBattery struct {
	mv int
	at time.Time
}

// GetLatestBatteries returns the newest reading per node key (lowercase) from
// both sources since the given time. A source that is absent or fails to read
// is skipped: the column is a convenience, never a reason to fail the list.
func (db *DB) GetLatestBatteries(pubkeys []string, since time.Time) map[string]latestBattery {
	out := map[string]latestBattery{}
	keep := func(pk, ts string, mv int) {
		at, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			return
		}
		pk = strings.ToLower(pk)
		if cur, ok := out[pk]; !ok || at.After(cur.at) {
			out[pk] = latestBattery{mv: mv, at: at}
		}
	}
	for start := 0; start < len(pubkeys); start += nodeListBatteryChunk {
		end := start + nodeListBatteryChunk
		if end > len(pubkeys) {
			end = len(pubkeys)
		}
		chunk := pubkeys[start:end]
		// Own status reports. observer_id is matched in both cases so the
		// (observer_id, timestamp) key serves the lookup (as in
		// GetNodeBatteryHistory); SQLite fills battery_mv from the MAX row.
		args := make([]interface{}, 0, 2*len(chunk)+1)
		for _, pk := range chunk {
			args = append(args, strings.ToLower(pk), strings.ToUpper(pk))
		}
		args = append(args, since.UTC().Format(time.RFC3339))
		marks := strings.TrimSuffix(strings.Repeat("?,", 2*len(chunk)), ",")
		if rows, err := db.conn.Query(`
			SELECT observer_id, MAX(timestamp), battery_mv FROM observer_metrics
			WHERE observer_id IN (`+marks+`) AND battery_mv IS NOT NULL AND timestamp >= ?
			GROUP BY observer_id`, args...); err == nil {
			for rows.Next() {
				var id, ts string
				var mv int
				if rows.Scan(&id, &ts, &mv) == nil {
					keep(id, ts, mv)
				}
			}
			rows.Close()
		}
		if !db.neighborBatteryPresent() {
			continue
		}
		args = args[:0]
		for _, pk := range chunk {
			args = append(args, strings.ToLower(pk))
		}
		args = append(args, since.UTC().Format(guestKeyTimeLayout))
		marks = strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		if rows, err := db.conn.Query(`
			SELECT node_id, MAX(sampled_at), battery_mv FROM neighbor_battery
			WHERE node_id IN (`+marks+`) AND sampled_at >= ?
			GROUP BY node_id`, args...); err == nil {
			for rows.Next() {
				var id, ts string
				var mv int
				if rows.Scan(&id, &ts, &mv) == nil {
					keep(id, ts, mv)
				}
			}
			rows.Close()
		}
	}
	return out
}

// enrichNodesBattery sets node["battery"] on the repeater/room rows that have
// a reading; rows without one are left without the field.
func (s *Server) enrichNodesBattery(nodes []map[string]interface{}) {
	if s.db == nil || !s.cfg.NeighborBatteryEnabled() {
		return
	}
	var keys []string
	for _, n := range nodes {
		if role, _ := n["role"].(string); role == "repeater" || role == "room" {
			if pk, ok := n["public_key"].(string); ok && pk != "" {
				keys = append(keys, pk)
			}
		}
	}
	if len(keys) == 0 {
		return
	}
	now := time.Now().UTC()
	latest := s.db.GetLatestBatteries(keys, now.Add(-nodeListBatteryWindow))
	if len(latest) == 0 {
		return
	}
	low, crit := s.cfg.LowBatteryMv(), s.cfg.CriticalBatteryMv()
	hideAfter := s.cfg.NeighborBatteryHideAfterHours()
	for _, n := range nodes {
		pk, _ := n["public_key"].(string)
		b, ok := latest[strings.ToLower(pk)]
		if !ok {
			continue
		}
		if role, _ := n["role"].(string); role != "repeater" && role != "room" {
			continue
		}
		ts := b.at.UTC().Format(guestKeyTimeLayout)
		n["battery"] = map[string]interface{}{
			"mv":     b.mv,
			"ts":     ts,
			"status": batteryStatus(b.mv, low, crit),
			"stale":  isStale(ts, hideAfter, now),
		}
	}
}
