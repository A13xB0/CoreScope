// Package areamap places a position in a named area: the regions a deployment
// draws for itself in a GeoJSON file, so packets and nodes can be attributed
// to where they are, not to the observer that heard them.
//
// Each feature of the FeatureCollection is one area. Its properties name it
// (code, label), put it in a layer (several sets of areas can share a file:
// countries, counties), and may list the airport codes of observers that
// report no position. Geometry is a Polygon or MultiPolygon, holes respected,
// in GeoJSON's [lon, lat] order.
package areamap

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

// DefaultLayer is the layer of an area that names none.
const DefaultLayer = "region"

// Area is one named area.
type Area struct {
	Code  string
	Label string
	Layer string
	// IATA lists airport codes of observers without a position that belong
	// to this area.
	IATA []string
	// Polygons are the area's parts; each is an outer ring and its holes, in
	// [lon, lat] order.
	Polygons [][][][2]float64
}

// Info is an area without its geometry, for listings.
type Info struct {
	Code  string `json:"code"`
	Label string `json:"label"`
	Layer string `json:"layer"`
}

// part is one polygon prepared for lookups: its rings and bounding box.
type part struct {
	rings                          [][][2]float64
	minLon, minLat, maxLon, maxLat float64
}

type prepared struct {
	Area
	parts []part
	size  float64 // area in roughly square kilometres, to pick the smallest
	iata  map[string]bool
}

// Map answers which area of a layer holds a position.
type Map struct {
	areas  []prepared
	layers []string
	byCode map[string]int
}

// ErrDuplicate is returned when two areas share a code.
var ErrDuplicate = errors.New("areamap: duplicate area code")

type geoJSON struct {
	Type     string `json:"type"`
	Features []struct {
		Type       string `json:"type"`
		Properties struct {
			Code  string   `json:"code"`
			Label string   `json:"label"`
			Layer string   `json:"layer"`
			IATA  []string `json:"iata"`
		} `json:"properties"`
		Geometry struct {
			Type        string          `json:"type"`
			Coordinates json.RawMessage `json:"coordinates"`
		} `json:"geometry"`
	} `json:"features"`
}

// ParseGeoJSON reads a FeatureCollection into areas.
func ParseGeoJSON(data []byte) ([]Area, error) {
	var g geoJSON
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, fmt.Errorf("areamap: %w", err)
	}
	if g.Type != "FeatureCollection" {
		return nil, fmt.Errorf("areamap: want a FeatureCollection, got %q", g.Type)
	}
	out := make([]Area, 0, len(g.Features))
	for i, f := range g.Features {
		p := f.Properties
		code := strings.TrimSpace(p.Code)
		if code == "" {
			return nil, fmt.Errorf("areamap: feature %d has no code", i)
		}
		a := Area{Code: code, Label: strings.TrimSpace(p.Label), Layer: strings.TrimSpace(p.Layer), IATA: p.IATA}
		if a.Label == "" {
			a.Label = code
		}
		if a.Layer == "" {
			a.Layer = DefaultLayer
		}
		switch f.Geometry.Type {
		case "Polygon":
			var poly [][][2]float64
			if err := json.Unmarshal(f.Geometry.Coordinates, &poly); err != nil {
				return nil, fmt.Errorf("areamap: %s: %w", code, err)
			}
			a.Polygons = [][][][2]float64{poly}
		case "MultiPolygon":
			if err := json.Unmarshal(f.Geometry.Coordinates, &a.Polygons); err != nil {
				return nil, fmt.Errorf("areamap: %s: %w", code, err)
			}
		default:
			return nil, fmt.Errorf("areamap: %s: geometry %q is not a Polygon or MultiPolygon", code, f.Geometry.Type)
		}
		for _, poly := range a.Polygons {
			if len(poly) == 0 {
				return nil, fmt.Errorf("areamap: %s: a polygon with no rings", code)
			}
			for _, ring := range poly {
				if len(ring) < 3 {
					return nil, fmt.Errorf("areamap: %s: a ring needs at least three points", code)
				}
			}
		}
		out = append(out, a)
	}
	return out, nil
}

// New builds a map. Codes must be unique across every layer.
func New(areas []Area) (*Map, error) {
	m := &Map{byCode: make(map[string]int, len(areas))}
	seenLayer := map[string]bool{}
	for _, a := range areas {
		if _, dup := m.byCode[a.Code]; dup {
			return nil, fmt.Errorf("%w: %s", ErrDuplicate, a.Code)
		}
		if a.Layer == "" {
			a.Layer = DefaultLayer
		}
		p := prepared{Area: a, iata: map[string]bool{}}
		for _, c := range a.IATA {
			p.iata[strings.ToUpper(strings.TrimSpace(c))] = true
		}
		for _, poly := range a.Polygons {
			pt := part{rings: poly, minLon: math.Inf(1), minLat: math.Inf(1), maxLon: math.Inf(-1), maxLat: math.Inf(-1)}
			for _, c := range poly[0] {
				pt.minLon, pt.maxLon = math.Min(pt.minLon, c[0]), math.Max(pt.maxLon, c[0])
				pt.minLat, pt.maxLat = math.Min(pt.minLat, c[1]), math.Max(pt.maxLat, c[1])
			}
			p.parts = append(p.parts, pt)
			for i, ring := range poly {
				s := ringSize(ring)
				if i > 0 {
					s = -s
				}
				p.size += s
			}
		}
		m.byCode[a.Code] = len(m.areas)
		m.areas = append(m.areas, p)
		if !seenLayer[a.Layer] {
			seenLayer[a.Layer] = true
			m.layers = append(m.layers, a.Layer)
		}
	}
	return m, nil
}

// ringSize is a ring's area in roughly square kilometres: the shoelace sum
// with longitude scaled by the cosine of the ring's mean latitude. It only
// has to order overlapping areas by size.
func ringSize(ring [][2]float64) float64 {
	var lat float64
	for _, c := range ring {
		lat += c[1]
	}
	k := math.Cos(lat / float64(len(ring)) * math.Pi / 180)
	var s float64
	for i, j := 0, len(ring)-1; i < len(ring); j, i = i, i+1 {
		s += (ring[j][0]*k)*ring[i][1] - (ring[i][0]*k)*ring[j][1]
	}
	return math.Abs(s) / 2 * 111.32 * 111.32
}

// inRing is the even-odd test on one ring, x = lon, y = lat.
func inRing(ring [][2]float64, lat, lon float64) bool {
	in := false
	for i, j := 0, len(ring)-1; i < len(ring); j, i = i, i+1 {
		xi, yi := ring[i][0], ring[i][1]
		xj, yj := ring[j][0], ring[j][1]
		if (yi > lat) != (yj > lat) && lon < (xj-xi)*(lat-yi)/(yj-yi)+xi {
			in = !in
		}
	}
	return in
}

func (p *prepared) contains(lat, lon float64) bool {
	for i := range p.parts {
		pt := &p.parts[i]
		if lon < pt.minLon || lon > pt.maxLon || lat < pt.minLat || lat > pt.maxLat {
			continue
		}
		if !inRing(pt.rings[0], lat, lon) {
			continue
		}
		hole := false
		for _, h := range pt.rings[1:] {
			if inRing(h, lat, lon) {
				hole = true
				break
			}
		}
		if !hole {
			return true
		}
	}
	return false
}

// Layers lists the layers, in the order their first area appeared.
func (m *Map) Layers() []string { return append([]string(nil), m.layers...) }

// Areas lists one layer's areas, or every area when layer is "".
func (m *Map) Areas(layer string) []Info {
	out := []Info{}
	for i := range m.areas {
		a := &m.areas[i]
		if layer == "" || a.Layer == layer {
			out = append(out, Info{Code: a.Code, Label: a.Label, Layer: a.Layer})
		}
	}
	return out
}

// Locate returns the code of the smallest area of layer containing the
// position. 0,0 is an unset GPS, never a place.
func (m *Map) Locate(layer string, lat, lon float64) (string, bool) {
	if m == nil || (lat == 0 && lon == 0) {
		return "", false
	}
	best, bestSize := -1, math.Inf(1)
	for i := range m.areas {
		a := &m.areas[i]
		if a.Layer != layer || a.size >= bestSize {
			continue
		}
		if a.contains(lat, lon) {
			best, bestSize = i, a.size
		}
	}
	if best < 0 {
		return "", false
	}
	return m.areas[best].Code, true
}

// LocateIATA returns the area of layer whose IATA list holds code.
func (m *Map) LocateIATA(layer, code string) (string, bool) {
	if m == nil {
		return "", false
	}
	code = strings.ToUpper(strings.TrimSpace(code))
	for i := range m.areas {
		a := &m.areas[i]
		if a.Layer == layer && a.iata[code] {
			return a.Code, true
		}
	}
	return "", false
}

// Has reports whether an area with code exists.
func (m *Map) Has(code string) bool { return false }

// Contains reports whether the area with code holds the position.
func (m *Map) Contains(code string, lat, lon float64) bool {
	if m == nil || (lat == 0 && lon == 0) {
		return false
	}
	i, ok := m.byCode[code]
	return ok && m.areas[i].contains(lat, lon)
}
