package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

const nbRelay = "4128a6aa112233445566778899aabbccddeeff00112233445566778899001100"

// addNeighborBatteryV2 adds the columns a newer ingestor creates.
func addNeighborBatteryV2(t *testing.T, db *DB) {
	t.Helper()
	for _, q := range []string{
		`ALTER TABLE neighbor_battery ADD COLUMN temperature_c REAL`,
		`ALTER TABLE neighbor_battery ADD COLUMN via TEXT`,
	} {
		if _, err := db.conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func createNeighborStatusTable(t *testing.T, db *DB) {
	t.Helper()
	if _, err := db.conn.Exec(`CREATE TABLE neighbor_status (
		node_id TEXT NOT NULL, observer_id TEXT NOT NULL, sampled_at TEXT NOT NULL,
		uptime_secs INTEGER, noise_floor INTEGER, last_rssi INTEGER, last_snr REAL, tx_queue INTEGER,
		packets_recv INTEGER, packets_sent INTEGER, recv_flood INTEGER, recv_direct INTEGER,
		sent_flood INTEGER, sent_direct INTEGER, tx_air_secs INTEGER, rx_air_secs INTEGER,
		flood_dups INTEGER, direct_dups INTEGER, recv_errors INTEGER, err_events INTEGER,
		PRIMARY KEY (node_id, observer_id, sampled_at)) WITHOUT ROWID`); err != nil {
		t.Fatal(err)
	}
}

func insertStatus(t *testing.T, db *DB, observer string, at time.Time, uptime, recvFlood, floodDups, txAir int) {
	t.Helper()
	if _, err := db.conn.Exec(`INSERT INTO neighbor_status (node_id, observer_id, sampled_at, uptime_secs, noise_floor,
		recv_flood, flood_dups, tx_air_secs) VALUES (?,?,?,?,?,?,?,?)`,
		nbNode, observer, at.UTC().Format("2006-01-02T15:04:05Z"), uptime, -116, recvFlood, floodDups, txAir); err != nil {
		t.Fatal(err)
	}
}

func getNodeJSON(t *testing.T, path string, db *DB) map[string]interface{} {
	t.Helper()
	w := httptest.NewRecorder()
	batteryRouter(t, db).ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	if w.Code != 200 {
		t.Fatalf("%s: status %d: %s", path, w.Code, w.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestNodeBatteryTemperatureAndVia(t *testing.T) {
	db := setupTestDB(t)
	now := seedNeighborNode(t, db)
	addNeighborBatteryV2(t, db)
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, last_seen, first_seen) VALUES (?, 'sco-edi-garvald', 'repeater', ?, ?)`,
		nbRelay, now.Format(time.RFC3339), now.Format(time.RFC3339))
	insertNeighborBattery(t, db, nbObsA, now.Add(-2*time.Hour), 3900, 7.5, 1)
	ts := now.Add(-time.Hour).UTC().Format("2006-01-02T15:04:05Z")
	if _, err := db.conn.Exec(`INSERT INTO neighbor_battery VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		nbNode, nbObsA, ts, 3950, 7.0, nil, "status", "poll", 1, ts, 16.2, nbRelay); err != nil {
		t.Fatal(err)
	}

	body := getNodeJSON(t, "/api/nodes/"+nbNode+"/battery?days=7", db)
	temp := body["temperature"].(map[string]interface{})
	if temp["celsius"].(float64) != 16.2 || temp["source"] != "neighbor" || temp["observer_name"] != "Corstorphine OBS" || temp["stale"] != false {
		t.Errorf("temperature = %v", temp)
	}
	sel := body["selected"].(map[string]interface{})
	if sel["via"] != nbRelay || sel["via_name"] != "sco-edi-garvald" {
		t.Errorf("selected via = %v %v", sel["via"], sel["via_name"])
	}
	if body["stale"] != false || body["hide_after_hours"].(float64) != 24 {
		t.Errorf("stale=%v hide_after_hours=%v", body["stale"], body["hide_after_hours"])
	}
}

// Without the newer columns (an ingestor not yet upgraded) the endpoint
// still works and reports no temperature.
func TestNodeBatteryWithoutV2Columns(t *testing.T) {
	db := setupTestDB(t)
	now := seedNeighborNode(t, db)
	insertNeighborBattery(t, db, nbObsA, now.Add(-time.Hour), 3900, 7.5, 0)
	body := getNodeJSON(t, "/api/nodes/"+nbNode+"/battery?days=7", db)
	if body["temperature"] != nil {
		t.Errorf("temperature = %v, want null", body["temperature"])
	}
	if len(body["samples"].([]interface{})) != 1 {
		t.Errorf("samples missing")
	}
}

func TestNodeBatteryAdvertTemperatureFallbackAndStale(t *testing.T) {
	db := setupTestDB(t)
	now := seedNeighborNode(t, db)
	addNeighborBatteryV2(t, db)
	old := now.Add(-30 * time.Hour)
	db.conn.Exec(`UPDATE nodes SET temperature_c = 21.5, last_seen = ? WHERE public_key = ?`, old.Format(time.RFC3339), nbNode)
	insertNeighborBattery(t, db, nbObsA, now.Add(-26*time.Hour), 3900, 7.5, 0)

	body := getNodeJSON(t, "/api/nodes/"+nbNode+"/battery?days=7", db)
	temp := body["temperature"].(map[string]interface{})
	if temp["celsius"].(float64) != 21.5 || temp["source"] != "advert" || temp["stale"] != true {
		t.Errorf("temperature = %v", temp)
	}
	if body["stale"] != true {
		t.Errorf("a 26 h old battery reading must be stale with the 24 h default")
	}
}

func TestHideAfterHoursConfig(t *testing.T) {
	zero, six := 0, 6
	if (&Config{}).NeighborBatteryHideAfterHours() != 24 {
		t.Error("default should be 24")
	}
	if (&Config{NeighborBattery: &NeighborBatteryServerConfig{HideAfterHours: &six}}).NeighborBatteryHideAfterHours() != 6 {
		t.Error("6 not honoured")
	}
	if (&Config{NeighborBattery: &NeighborBatteryServerConfig{HideAfterHours: &zero}}).NeighborBatteryHideAfterHours() != 0 {
		t.Error("0 (never hide) not honoured")
	}
	now := time.Now().UTC()
	if !isStale(now.Add(-25*time.Hour).Format(time.RFC3339), 24, now) || isStale(now.Add(-23*time.Hour).Format(time.RFC3339), 24, now) {
		t.Error("24 h boundary")
	}
	if isStale(now.Add(-1000*time.Hour).Format(time.RFC3339), 0, now) {
		t.Error("0 must never hide")
	}
}

func TestNodeStatusHistoryRatesAndReboot(t *testing.T) {
	db := setupTestDB(t)
	now := seedNeighborNode(t, db)
	createNeighborStatusTable(t, db)
	t0 := now.Add(-3 * time.Hour)
	insertStatus(t, db, nbObsA, t0, 10000, 1000, 700, 50)
	insertStatus(t, db, nbObsA, t0.Add(time.Hour), 13600, 1400, 1000, 80)
	insertStatus(t, db, nbObsA, t0.Add(2*time.Hour), 600, 30, 20, 2) // rebooted
	insertStatus(t, db, nbObsB, t0, 9000, 900, 600, 40)

	body := getNodeJSON(t, "/api/nodes/"+nbNode+"/status-history?days=7", db)
	series := body["series"].([]interface{})
	if len(series) != 2 {
		t.Fatalf("series = %d, want 2", len(series))
	}
	a := series[0].(map[string]interface{})
	if a["observer_id"] != nbObsA || a["observer_name"] != "Corstorphine OBS" {
		t.Fatalf("most samples first: %v", a["observer_id"])
	}
	samples := a["samples"].([]interface{})
	s1 := samples[1].(map[string]interface{})
	ph := s1["per_hour"].(map[string]interface{})
	if ph["recv_flood"].(float64) != 400 || ph["tx_air_secs"].(float64) != 30 {
		t.Errorf("per_hour = %v", ph)
	}
	if s1["dup_rate"].(float64) != 0.75 {
		t.Errorf("dup_rate = %v, want 0.75", s1["dup_rate"])
	}
	s2 := samples[2].(map[string]interface{})
	if s2["reboot"] != true || s2["per_hour"] != nil {
		t.Errorf("reboot sample = %v", s2)
	}
	vals := s1["values"].(map[string]interface{})
	if vals["noise_floor"].(float64) != -116 || vals["last_rssi"] != nil {
		t.Errorf("values = %v", vals)
	}
}

func TestNodeStatusHistoryEmptyAndUnknown(t *testing.T) {
	db := setupTestDB(t)
	seedNeighborNode(t, db)
	body := getNodeJSON(t, "/api/nodes/"+nbNode+"/status-history", db)
	if len(body["series"].([]interface{})) != 0 {
		t.Errorf("want no series without the table")
	}
	w := httptest.NewRecorder()
	batteryRouter(t, db).ServeHTTP(w, httptest.NewRequest("GET", "/api/nodes/"+nbRelay+"/status-history", nil))
	if w.Code != 404 {
		t.Errorf("unknown node: %d", w.Code)
	}
}
