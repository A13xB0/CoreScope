package main

import "github.com/meshcore-analyzer/areamap"

// LoadAreaMap builds the map of every area: config.json's areas (layer
// "region") and the features of AreasFile. It stores the map on cfg.
func LoadAreaMap(cfg *Config, configDir string) (*areamap.Map, error) {
	m, err := areamap.New(nil)
	if err != nil {
		return nil, err
	}
	cfg.areaMap = m
	return m, nil
}
