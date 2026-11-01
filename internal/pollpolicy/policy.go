// Package pollpolicy is the community polling policy for neighbour battery
// polling: the neighborBattery.polling block of config.json, the effective
// policy derived from it, its version hash, and the plugin version rules.
// cmd/server serves it (/api/guest-polling/policy) and ranks claimants with
// it; cmd/ingestor drops readings and credit by it. Both re-read the block
// while running (Watch), so a pause or a block takes effect without a
// restart.
package pollpolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Defaults for a missing block or a missing (or non-positive) field.
const (
	DefaultDirectMinutes        = 60
	DefaultOneHopMinutes        = 120
	DefaultStandbyGraceMinutes  = 30
	DefaultMaxRequestsPerWindow = 2
	DefaultTakeoverAfterMissed  = 2
)

// SentinelOwner is the owner /owners names when nobody may poll a node, so a
// plugin that only reads owner/mine stands down.
var SentinelOwner = strings.Repeat("0", 64)

var hexKeyRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Config is neighborBattery.polling as written in config.json.
type Config struct {
	DirectMinutes              int            `json:"directMinutes,omitempty"`
	OneHopMinutes              int            `json:"oneHopMinutes,omitempty"`
	StandbyGraceMinutes        int            `json:"standbyGraceMinutes,omitempty"`
	MaxRequestsPerWindow       int            `json:"maxRequestsPerWindow,omitempty"`
	TakeoverAfterMissedWindows int            `json:"takeoverAfterMissedWindows,omitempty"`
	Paused                     bool           `json:"paused,omitempty"`
	PausedReason               string         `json:"pausedReason,omitempty"`
	PausedUntil                string         `json:"pausedUntil,omitempty"`
	ExcludedNodes              []string       `json:"excludedNodes,omitempty"`
	BlockedObservers           []string       `json:"blockedObservers,omitempty"`
	MinPluginVersion           string         `json:"minPluginVersion,omitempty"`
	MinVersionReason           string         `json:"minVersionReason,omitempty"`
	NodeMinutes                map[string]int `json:"nodeMinutes,omitempty"`
}

// Policy is the effective policy at one moment: defaults filled in, keys
// lowercased and checked, and the pause lifted once pausedUntil has passed.
// Its field order is the canonical form the version is hashed from.
type Policy struct {
	DirectMinutes        int            `json:"direct_minutes"`
	OneHopMinutes        int            `json:"one_hop_minutes"`
	StandbyGraceMinutes  int            `json:"standby_grace_minutes"`
	MaxRequestsPerWindow int            `json:"max_requests"`
	TakeoverAfter        int            `json:"takeover_after"`
	Paused               bool           `json:"paused"`
	PausedReason         string         `json:"paused_reason"`
	PausedUntil          string         `json:"paused_until"`
	ExcludedNodes        []string       `json:"excluded_nodes"`
	BlockedObservers     []string       `json:"blocked_observers"`
	MinPluginVersion     string         `json:"min_plugin_version"`
	MinVersionReason     string         `json:"min_version_reason"`
	NodeMinutes          map[string]int `json:"node_minutes"`

	// Version is the first 8 hex of sha256 over the fields above.
	Version string `json:"-"`

	excluded map[string]bool
	blocked  map[string]bool
	min      Semver
	hasMin   bool
}

// Effective derives the policy in force at now. A nil Config gives the
// defaults. Invalid keys are dropped; an unparsable pausedUntil counts as no
// end (the pause holds until the operator lifts it).
func (c *Config) Effective(now time.Time) *Policy {
	var src Config
	if c != nil {
		src = *c
	}
	p := &Policy{
		DirectMinutes:        positiveOr(src.DirectMinutes, DefaultDirectMinutes),
		OneHopMinutes:        positiveOr(src.OneHopMinutes, DefaultOneHopMinutes),
		StandbyGraceMinutes:  positiveOr(src.StandbyGraceMinutes, DefaultStandbyGraceMinutes),
		MaxRequestsPerWindow: positiveOr(src.MaxRequestsPerWindow, DefaultMaxRequestsPerWindow),
		TakeoverAfter:        positiveOr(src.TakeoverAfterMissedWindows, DefaultTakeoverAfterMissed),
		ExcludedNodes:        normKeys(src.ExcludedNodes),
		BlockedObservers:     normKeys(src.BlockedObservers),
		NodeMinutes:          map[string]int{},
	}
	if src.Paused {
		p.Paused = true
		p.PausedReason = strings.TrimSpace(src.PausedReason)
		if u := strings.TrimSpace(src.PausedUntil); u != "" {
			if t, err := time.Parse(time.RFC3339, u); err == nil {
				if !now.Before(t) {
					p.Paused, p.PausedReason = false, ""
				} else {
					p.PausedUntil = t.UTC().Format(time.RFC3339)
				}
			}
		}
	}
	if v, ok := ParseVersion(strings.TrimSpace(src.MinPluginVersion)); ok {
		p.MinPluginVersion = v.String()
		p.MinVersionReason = strings.TrimSpace(src.MinVersionReason)
		p.min, p.hasMin = v, true
	}
	for k, m := range src.NodeMinutes {
		k = strings.ToLower(strings.TrimSpace(k))
		if hexKeyRe.MatchString(k) && m > 0 {
			p.NodeMinutes[k] = m
		}
	}
	p.excluded = toSet(p.ExcludedNodes)
	p.blocked = toSet(p.BlockedObservers)
	b, _ := json.Marshal(p)
	sum := sha256.Sum256(b)
	p.Version = hex.EncodeToString(sum[:])[:8]
	return p
}

func positiveOr(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

func normKeys(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, k := range in {
		k = strings.ToLower(strings.TrimSpace(k))
		if hexKeyRe.MatchString(k) && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func toSet(keys []string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// IsExcluded reports whether node (any case) is on excludedNodes.
func (p *Policy) IsExcluded(node string) bool {
	return p.excluded[strings.ToLower(node)]
}

// IsBlocked reports whether observer (any case) is on blockedObservers.
func (p *Policy) IsBlocked(observer string) bool {
	return p.blocked[strings.ToLower(observer)]
}

// HasMinVersion reports whether a minimum plugin version is set.
func (p *Policy) HasMinVersion() bool { return p.hasMin }

// VersionOK reports whether a plugin version string ("0.8.0",
// "openhop.neighbour_battery/0.8.0" or "") meets the minimum. An unparsable
// or empty version counts as 0.0.0. Always true without a minimum.
func (p *Policy) VersionOK(version string) bool {
	if !p.hasMin {
		return true
	}
	v, _ := ParsePluginVersion(version)
	return v.Compare(p.min) >= 0
}

// WindowMinutes is the polling window for node when its poller is hops away
// (0 = direct): nodeMinutes[node] if set, else directMinutes or oneHopMinutes.
func (p *Policy) WindowMinutes(node string, hops int) int {
	if m, ok := p.NodeMinutes[strings.ToLower(node)]; ok {
		return m
	}
	if hops == 0 {
		return p.DirectMinutes
	}
	return p.OneHopMinutes
}

// Window is WindowMinutes as a duration.
func (p *Policy) Window(node string, hops int) time.Duration {
	return time.Duration(p.WindowMinutes(node, hops)) * time.Minute
}

// Grace is the standby grace as a duration.
func (p *Policy) Grace() time.Duration {
	return time.Duration(p.StandbyGraceMinutes) * time.Minute
}

// Semver is a parsed x.y.z version.
type Semver [3]int

func (v Semver) String() string {
	return strconv.Itoa(v[0]) + "." + strconv.Itoa(v[1]) + "." + strconv.Itoa(v[2])
}

// Compare returns -1, 0 or 1.
func (v Semver) Compare(o Semver) int {
	for i := 0; i < 3; i++ {
		if v[i] != o[i] {
			if v[i] < o[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// ParseVersion parses "x.y.z" (a leading "v", missing trailing components
// and a pre-release/build suffix are tolerated: "v0.8" is 0.8.0, "0.8.1-rc1"
// is 0.8.1). ok is false for anything else.
func ParseVersion(s string) (Semver, bool) {
	var v Semver
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(s, "-+ "); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return v, false
	}
	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return v, false
	}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return Semver{}, false
		}
		v[i] = n
	}
	return v, true
}

// ParsePluginVersion reads the version from a plugin identifier
// ("openhop.neighbour_battery/0.8.0") or a bare version ("0.8.0"). ok is
// false (and the version 0.0.0) when none can be read.
func ParsePluginVersion(s string) (Semver, bool) {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	return ParseVersion(s)
}

// PluginVersionString is ParsePluginVersion as "x.y.z", or "" when none can
// be read.
func PluginVersionString(s string) string {
	if v, ok := ParsePluginVersion(s); ok {
		return v.String()
	}
	return ""
}
