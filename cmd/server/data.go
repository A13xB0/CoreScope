package main

import (
	"context"
	"net/http"
	"time"

	"github.com/meshcore-analyzer/dataindex"
)

type DataConfig struct {
	Enabled  bool   `json:"enabled"`
	Days     int    `json:"days,omitempty"`
	Layer    string `json:"layer,omitempty"`
	HomeArea string `json:"homeArea,omitempty"`
}

func (c *Config) DataEnabled() bool { return false }
func (c *Config) DataDays() int     { return 0 }

type dataService struct {
	now         func() time.Time
	refreshSpan time.Duration
	ix          *dataindex.Index
}

func newDataService(db *DB, cfg *Config) (*dataService, error) {
	return &dataService{ix: dataindex.New(dataindex.Options{})}, nil
}
func (d *dataService) fill(ctx context.Context) error    { return nil }
func (d *dataService) refresh(ctx context.Context) error { return nil }
func (d *dataService) index() *dataindex.Index           { return d.ix }
func (d *dataService) packets(window string, from, to int64, fs []dataindex.Filter) dataindex.PacketResult {
	return dataindex.PacketResult{}
}
func (d *dataService) cubeJSON(window string) ([]byte, error)              { return []byte("{}"), nil }
func (s *Server) handleDataConfig(w http.ResponseWriter, r *http.Request)  {}
func (s *Server) handleDataSummary(w http.ResponseWriter, r *http.Request) {}
func (s *Server) handleDataCube(w http.ResponseWriter, r *http.Request)    {}
func (s *Server) handleDataPackets(w http.ResponseWriter, r *http.Request) {}
