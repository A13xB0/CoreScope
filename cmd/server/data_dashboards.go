package main

import (
	"net/http"

	"github.com/meshcore-analyzer/dataindex"
)

type DashboardVariable struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Default string `json:"default,omitempty"`
}

type DashboardQuery struct {
	Window  string             `json:"window,omitempty"`
	Filters []dataindex.Filter `json:"filters,omitempty"`
}

type DashboardPanel struct {
	ID    string         `json:"id"`
	Title string         `json:"title"`
	Type  string         `json:"type"`
	Query DashboardQuery `json:"query"`
}

type Dashboard struct {
	Schema int              `json:"schema"`
	ID     string           `json:"id"`
	Title  string           `json:"title"`
	Panels []DashboardPanel `json:"panels"`
	Source string           `json:"source"`
}

type DataDashboards struct {
	Default    string      `json:"default"`
	Home       string      `json:"home,omitempty"`
	Dashboards []Dashboard `json:"dashboards"`
}

func LoadDataDashboards(cfg *Config, configDir string) (*DataDashboards, error) {
	return &DataDashboards{}, nil
}

func validateDashboard(d Dashboard) error { return nil }

func (s *Server) handleDataDashboards(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{})
}
