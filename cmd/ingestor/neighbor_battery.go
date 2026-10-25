package main

import (
	"database/sql"
	"fmt"
	"log"
	"math"
	"regexp"
	"strings"
	"time"
)

// Neighbour battery readings: meshcore/<region>/<observer_id>/battery.
//
// An observer logs in to the repeaters it hears directly, asks each for its
// status (REQ_TYPE_GET_STATUS, whose first field is batt_milli_volts) and
// publishes the answers in one batch:
//
//	{"v":1, "origin":"…", "origin_id":"<observer key>", "timestamp":"…",
//	 "readings":[{"node_id":"<64-hex>", "battery_mv":3240, "snr":7.4,
//	              "rssi":-88, "source":"poll", "reply_hops":0,
//	              "method":"status", "sampled_at":"…",
//	              "via":"<64-hex relay>", "temperature_c":31.5,
//	              "stats":{"uptime_secs":788089, "noise_floor":-116, …}}]}
//
// via, temperature_c and stats are optional. via names the relay a one-hop
// request went through; temperature_c is the repeater's own (chip)
// temperature from a telemetry request; stats is the rest of the status
// reply (MeshCore RepeaterStats), stored in neighbor_status.
//
// Readings are observations, never state: one row per (node, observer,
// sampled_at). 0 mV is kept and means "no battery fitted" (an openHop
// repeater without a power sensor answers 0), never "flat".

const (
	batteryReportVersion     = 1
	batteryMaxReadings       = 64
	batteryMaxMv             = 10000 // same bound as the advert telemetry decoder
	batteryMaxPast           = 2 * time.Hour
	batteryMaxFuture         = 5 * time.Minute
	batteryDefaultMinMinutes = 10
	batteryTimeLayout        = "2006-01-02T15:04:05Z"
	batteryMinTempC          = -60
	batteryMaxTempC          = 150
)

// statusFields are the stats keys stored in neighbor_status, in column
// order, with the bounds a value must fall in to be kept.
var statusFields = []struct {
	Key      string
	Min, Max float64
	Float    bool
}{
	{"uptime_secs", 0, math.MaxUint32, false},
	{"noise_floor", -200, 0, false},
	{"last_rssi", -200, 0, false},
	{"last_snr", -64, 64, true},
	{"tx_queue", 0, 65535, false},
	{"packets_recv", 0, math.MaxUint32, false},
	{"packets_sent", 0, math.MaxUint32, false},
	{"recv_flood", 0, math.MaxUint32, false},
	{"recv_direct", 0, math.MaxUint32, false},
	{"sent_flood", 0, math.MaxUint32, false},
	{"sent_direct", 0, math.MaxUint32, false},
	{"tx_air_secs", 0, math.MaxUint32, false},
	{"rx_air_secs", 0, math.MaxUint32, false},
	{"flood_dups", 0, 65535, false},
	{"direct_dups", 0, 65535, false},
	{"recv_errors", 0, math.MaxUint32, false},
	{"err_events", 0, 65535, false},
}

var batteryMethodRe = regexp.MustCompile(`^[a-z_]{1,16}$`)

// NeighborBatteryReading is one accepted reading, ready to store.
type NeighborBatteryReading struct {
	NodeID     string
	ObserverID string
	SampledAt  string
	BatteryMv  int
	SNR        *float64
	RSSI       *int64
	Method     string
	Source     string
	ReplyHops  int
	ReceivedAt string
	// Via is the relay a one-hop request went through; "" when direct.
	Via string
	// KeyMode is "shared" when the reading was taken under the shared guest
	// key; "" for this observer's own key.
	KeyMode      string
	TemperatureC *float64
	// Stats holds the status-reply fields that were present and in range,
	// keyed as in statusFields; nil when the reading carried none.
	Stats map[string]float64
}

// parseBatteryReport validates a /battery message and returns the readings
// to store. A message-level problem rejects the whole batch (err != nil);
// a bad reading is skipped and named in skipped, so one malformed entry
// doesn't cost the others.
func parseBatteryReport(observerID string, msg map[string]interface{}, now time.Time) (out []NeighborBatteryReading, skipped []string, err error) {
	observerID = strings.ToLower(strings.TrimSpace(observerID))
	if !targetPubkeyRe.MatchString(observerID) {
		return nil, nil, fmt.Errorf("observer id is not a 64-hex key")
	}
	if v, ok := toFloat64(msg["v"]); !ok || v != batteryReportVersion {
		return nil, nil, fmt.Errorf("unsupported version %v", msg["v"])
	}
	// The broker binds origin_id to the authenticated key; a mismatch means
	// the topic and the payload disagree about who is speaking.
	if origin, ok := msg["origin_id"].(string); ok && origin != "" && strings.ToLower(origin) != observerID {
		return nil, nil, fmt.Errorf("origin_id does not match the topic")
	}
	raw, _ := msg["readings"].([]interface{})
	if len(raw) == 0 || len(raw) > batteryMaxReadings {
		return nil, nil, fmt.Errorf("readings: want 1-%d, got %d", batteryMaxReadings, len(raw))
	}

	receivedAt := now.UTC().Format(batteryTimeLayout)
	seen := make(map[string]bool, len(raw))
	for i, item := range raw {
		r, ok := item.(map[string]interface{})
		if !ok {
			skipped = append(skipped, fmt.Sprintf("#%d not an object", i))
			continue
		}
		reading, why := parseBatteryReading(observerID, r, now)
		if why != "" {
			skipped = append(skipped, fmt.Sprintf("#%d %s", i, why))
			continue
		}
		if seen[reading.NodeID] {
			skipped = append(skipped, fmt.Sprintf("#%d duplicate node", i))
			continue
		}
		seen[reading.NodeID] = true
		reading.ReceivedAt = receivedAt
		out = append(out, reading)
	}
	return out, skipped, nil
}

func parseBatteryReading(observerID string, r map[string]interface{}, now time.Time) (NeighborBatteryReading, string) {
	var z NeighborBatteryReading
	node, _ := r["node_id"].(string)
	node = strings.ToLower(strings.TrimSpace(node))
	if !targetPubkeyRe.MatchString(node) {
		return z, "bad node_id"
	}
	if node == observerID {
		return z, "self-report"
	}
	mv, ok := toFloat64(r["battery_mv"])
	if !ok || mv != math.Trunc(mv) || mv < 0 || mv > batteryMaxMv {
		return z, "bad battery_mv"
	}
	ts, _ := r["sampled_at"].(string)
	sampled, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return z, "bad sampled_at"
	}
	if sampled.Before(now.Add(-batteryMaxPast)) || sampled.After(now.Add(batteryMaxFuture)) {
		return z, "sampled_at outside the accepted window"
	}
	source := "poll"
	if s, ok := r["source"].(string); ok && s != "" {
		if s != "poll" && s != "reused" {
			return z, "bad source"
		}
		source = s
	}
	hops := 0
	if h, ok := toFloat64(r["reply_hops"]); ok {
		if h != math.Trunc(h) || h < 0 || h > 64 {
			return z, "bad reply_hops"
		}
		hops = int(h)
	}
	method := "status"
	if m, ok := r["method"].(string); ok && m != "" {
		if !batteryMethodRe.MatchString(m) {
			return z, "bad method"
		}
		method = m
	}
	out := NeighborBatteryReading{
		NodeID:     node,
		ObserverID: observerID,
		SampledAt:  sampled.UTC().Format(batteryTimeLayout),
		BatteryMv:  int(mv),
		Method:     method,
		Source:     source,
		ReplyHops:  hops,
	}
	if snr, ok := toFloat64(r["snr"]); ok && snr >= -64 && snr <= 64 {
		out.SNR = &snr
	}
	if rssi, ok := toFloat64(r["rssi"]); ok && rssi >= -200 && rssi <= 0 {
		v := int64(rssi)
		out.RSSI = &v
	}
	if via, ok := r["via"].(string); ok && via != "" {
		via = strings.ToLower(strings.TrimSpace(via))
		if !targetPubkeyRe.MatchString(via) || via == node {
			return z, "bad via"
		}
		out.Via = via
	}
	if k, ok := r["key"].(string); ok && k == "shared" {
		out.KeyMode = "shared"
	}
	if t, ok := toFloat64(r["temperature_c"]); ok && t >= batteryMinTempC && t <= batteryMaxTempC {
		out.TemperatureC = &t
	}
	if raw, ok := r["stats"].(map[string]interface{}); ok {
		out.Stats = parseStatusStats(raw)
	}
	return out, ""
}

// parseStatusStats keeps the known fields that are numbers in range; the
// rest are dropped one by one, so an odd field never costs the reading.
func parseStatusStats(raw map[string]interface{}) map[string]float64 {
	out := map[string]float64{}
	for _, f := range statusFields {
		v, ok := toFloat64(raw[f.Key])
		if !ok || v < f.Min || v > f.Max || (!f.Float && v != math.Trunc(v)) {
			continue
		}
		out[f.Key] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// handleBatteryReport ingests a /battery message from an observer.
func handleBatteryReport(store *Store, cfg *Config, tag, observerID string, msg map[string]interface{}) {
	readings, skipped, err := parseBatteryReport(observerID, msg, time.Now().UTC())
	if err != nil {
		log.Printf("MQTT [%s] battery from %.8s rejected: %v", tag, observerID, err)
		return
	}
	if len(skipped) > 0 {
		log.Printf("MQTT [%s] battery from %.8s: skipped %s", tag, observerID, strings.Join(skipped, ", "))
	}
	minGap := time.Duration(cfg.NeighborBatteryMinIntervalMinutes()) * time.Minute
	stored := 0
	for i := range readings {
		ok, err := store.InsertNeighborBattery(&readings[i], minGap)
		if err != nil {
			log.Printf("MQTT [%s] battery insert for %.8s: %v", tag, readings[i].NodeID, err)
			continue
		}
		if ok {
			stored++
		}
	}
	log.Printf("MQTT [%s] battery from %.8s: %d reading(s), %d stored", tag, observerID, len(readings), stored)
}

// InsertNeighborBattery stores one reading. It is skipped (false, nil) when
// the same observer already has a reading for the node within minGap of this
// one, so a chatty or replaying publisher can't flood the table.
func (s *Store) InsertNeighborBattery(r *NeighborBatteryReading, minGap time.Duration) (bool, error) {
	if minGap > 0 {
		t, err := time.Parse(batteryTimeLayout, r.SampledAt)
		if err != nil {
			return false, err
		}
		var one int
		err = s.db.QueryRow(`
			SELECT 1 FROM neighbor_battery
			WHERE node_id = ? AND observer_id = ? AND sampled_at > ? AND sampled_at < ?
			LIMIT 1`,
			r.NodeID, r.ObserverID,
			t.Add(-minGap).Format(batteryTimeLayout), t.Add(minGap).Format(batteryTimeLayout)).Scan(&one)
		if err == nil {
			return false, nil
		}
	}
	var via interface{}
	if r.Via != "" {
		via = r.Via
	}
	var keyMode interface{}
	if r.KeyMode != "" {
		keyMode = r.KeyMode
	}
	res, err := s.db.Exec(`
		INSERT INTO neighbor_battery
			(node_id, observer_id, sampled_at, battery_mv, snr, rssi, method, source, reply_hops, received_at,
			 temperature_c, via, key_mode)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(node_id, observer_id, sampled_at) DO NOTHING`,
		r.NodeID, r.ObserverID, r.SampledAt, r.BatteryMv, r.SNR, r.RSSI, r.Method, r.Source, r.ReplyHops, r.ReceivedAt,
		r.TemperatureC, via, keyMode)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 && r.Stats != nil {
		if err := s.insertNeighborStatus(r); err != nil {
			return true, err
		}
	}
	return n > 0, nil
}

// insertNeighborStatus stores a reading's status stats beside its battery row.
func (s *Store) insertNeighborStatus(r *NeighborBatteryReading) error {
	cols := []string{"node_id", "observer_id", "sampled_at"}
	args := []interface{}{r.NodeID, r.ObserverID, r.SampledAt}
	for _, f := range statusFields {
		cols = append(cols, f.Key)
		if v, ok := r.Stats[f.Key]; ok {
			if f.Float {
				args = append(args, v)
			} else {
				args = append(args, int64(v))
			}
		} else {
			args = append(args, nil)
		}
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",")
	_, err := s.db.Exec(`INSERT INTO neighbor_status (`+strings.Join(cols, ", ")+`) VALUES (`+marks+`)
		ON CONFLICT(node_id, observer_id, sampled_at) DO NOTHING`, args...)
	return err
}

// migrateNeighborBatteryV2 adds temperature_c and via to a neighbor_battery
// table created before them. CREATE TABLE IF NOT EXISTS leaves an existing
// table as it was, so the columns are added here.
func migrateNeighborBatteryV2(db *sql.DB) error {
	for _, col := range []struct{ name, typ string }{{"temperature_c", "REAL"}, {"via", "TEXT"}, {"key_mode", "TEXT"}} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('neighbor_battery') WHERE name = ?`, col.name).Scan(&n); err != nil {
			return fmt.Errorf("neighbor_battery columns: %w", err)
		}
		if n > 0 {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE neighbor_battery ADD COLUMN ` + col.name + ` ` + col.typ); err != nil {
			return fmt.Errorf("adding neighbor_battery.%s: %w", col.name, err)
		}
		log.Printf("[migration] neighbor_battery.%s added", col.name)
	}
	return nil
}

// PruneOldNeighborBattery deletes readings older than days (by sampled_at).
// 0 disables.
func (s *Store) PruneOldNeighborBattery(days int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format(batteryTimeLayout)
	res, err := s.db.Exec(`DELETE FROM neighbor_battery WHERE sampled_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune neighbor_battery: %w", err)
	}
	if _, err := s.db.Exec(`DELETE FROM neighbor_status WHERE sampled_at < ?`, cutoff); err != nil {
		return 0, fmt.Errorf("prune neighbor_status: %w", err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		log.Printf("[prune] deleted %d neighbor_battery older than %d days", n, days)
	}
	return n, nil
}
