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

import "errors"

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

// Map answers which area of a layer holds a position.
type Map struct{}

// ErrDuplicate is returned when two areas share a code.
var ErrDuplicate = errors.New("areamap: duplicate area code")

// ParseGeoJSON reads a FeatureCollection into areas.
func ParseGeoJSON(data []byte) ([]Area, error) { return nil, nil }

// New builds a map. Codes must be unique across every layer.
func New(areas []Area) (*Map, error) { return &Map{}, nil }

// Layers lists the layers, in the order their first area appeared.
func (m *Map) Layers() []string { return nil }

// Areas lists one layer's areas, or every area when layer is "".
func (m *Map) Areas(layer string) []Info { return nil }

// Locate returns the code of the smallest area of layer containing the
// position. 0,0 is an unset GPS, never a place.
func (m *Map) Locate(layer string, lat, lon float64) (string, bool) { return "", false }

// LocateIATA returns the area of layer whose IATA list holds code.
func (m *Map) LocateIATA(layer, code string) (string, bool) { return "", false }

// Contains reports whether the area with code holds the position.
func (m *Map) Contains(code string, lat, lon float64) bool { return false }
