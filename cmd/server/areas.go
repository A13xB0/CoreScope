package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/meshcore-analyzer/areamap"
)

// LoadAreaMap builds the map of every area: config.json's areas (layer
// "region") and the features of AreasFile. It stores the map on cfg, where
// the store and the handlers find it. A code defined twice, a missing file
// or a file that is not GeoJSON is an error: the caller refuses to start
// rather than run with areas silently missing.
func LoadAreaMap(cfg *Config, configDir string) (*areamap.Map, error) {
	var areas []areamap.Area
	keys := make([]string, 0, len(cfg.Areas))
	for k := range cfg.Areas {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		e := cfg.Areas[k]
		if e.Label == "" {
			continue // "_comment" and other non-areas
		}
		var ring [][2]float64
		switch {
		case len(e.Polygon) >= 3:
			// config.json polygons are [lat, lon]; GeoJSON rings are [lon, lat].
			for _, p := range e.Polygon {
				ring = append(ring, [2]float64{p[1], p[0]})
			}
		case e.LatMin != nil && e.LatMax != nil && e.LonMin != nil && e.LonMax != nil:
			ring = [][2]float64{{*e.LonMin, *e.LatMin}, {*e.LonMax, *e.LatMin}, {*e.LonMax, *e.LatMax}, {*e.LonMin, *e.LatMax}}
		default:
			continue // listed by name only, with nothing to locate against
		}
		areas = append(areas, areamap.Area{Code: k, Label: e.Label, Layer: areamap.DefaultLayer, Polygons: [][][][2]float64{{ring}}})
	}
	if cfg.AreasFile != "" {
		path := cfg.AreasFile
		if !filepath.IsAbs(path) {
			path = filepath.Join(configDir, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("areasFile: %w", err)
		}
		fromFile, err := areamap.ParseGeoJSON(data)
		if err != nil {
			return nil, fmt.Errorf("areasFile %s: %w", path, err)
		}
		areas = append(areas, fromFile...)
	}
	m, err := areamap.New(areas)
	if err != nil {
		return nil, err
	}
	cfg.areaMap = m
	return m, nil
}

// fileArea reports whether code names an area that only the areas file
// defines, so area= resolves it by position instead of config.json's entry.
func (c *Config) fileArea(code string) bool {
	if c == nil || c.areaMap == nil {
		return false
	}
	if _, inConfig := c.Areas[code]; inConfig {
		return false
	}
	return c.areaMap.Has(code)
}

// areaGeoTolerance is how far, in degrees (about 450 m north-south), a
// drawn outline may stray from the file's: too little to see at regional
// zoom. On a 247 KB, 13,700-point country file it gives 10,400 points and
// 62 KB gzipped, built in under 3 ms.
const areaGeoTolerance = 0.004

// areaGeoCache holds each area map's GeoJSON per layer, built on first
// request. Area maps never change after startup, so nothing is evicted.
var areaGeoCache sync.Map // areaGeoKey -> []byte

type areaGeoKey struct {
	m     *areamap.Map
	layer string
}

// GET /api/config/areas/geojson[?layer=]
//
// Every area's shape for drawing, from config.json and the areas file alike,
// simplified to areaGeoTolerance. layer picks one layer; none means all.
func (s *Server) handleConfigAreasGeoJSON(w http.ResponseWriter, r *http.Request) {
	m := s.cfg.areaMap
	if m == nil {
		writeJSON(w, areamap.FeatureCollection{Type: "FeatureCollection", Features: []areamap.Feature{}})
		return
	}
	key := areaGeoKey{m, r.URL.Query().Get("layer")}
	body, ok := areaGeoCache.Load(key)
	if !ok {
		b, err := json.Marshal(m.FeatureCollection(key.layer, areaGeoTolerance))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		body, _ = areaGeoCache.LoadOrStore(key, b)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(body.([]byte))
}
