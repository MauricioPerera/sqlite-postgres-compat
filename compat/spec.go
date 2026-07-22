// Package compat defines the engine-neutral contract used by the compatibility
// implementation. It deliberately requires explicit targets: a claim of exact
// compatibility has no meaning without source and destination versions.
package compat

import (
	"fmt"
)

type Engine string

const (
	SQLite   Engine = "sqlite"
	Postgres Engine = "postgres"
)

type Version struct {
	Major int `json:"major"`
	Minor int `json:"minor"`
	Patch int `json:"patch"`
}

func (v Version) Valid() bool {
	return v.Major >= 0 && v.Minor >= 0 && v.Patch >= 0
}

func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

type Target struct {
	Engine  Engine  `json:"engine"`
	Version Version `json:"version"`
}

func (t Target) Validate() error {
	if t.Engine != SQLite && t.Engine != Postgres {
		return fmt.Errorf("unsupported engine %q", t.Engine)
	}
	if !t.Version.Valid() {
		return fmt.Errorf("invalid %s version %s", t.Engine, t.Version)
	}
	return nil
}

// Contract is the complete compatibility claim made for one migration or
// synchronization relationship. RequiredFeatures must be audited before data
// movement begins; unknown capabilities are failures, never silent fallbacks.
type Contract struct {
	Source           Target    `json:"source"`
	Destination      Target    `json:"destination"`
	RequiredFeatures []Feature `json:"required_features"`
}

func (c Contract) Validate() error {
	if err := c.Source.Validate(); err != nil {
		return fmt.Errorf("source: %w", err)
	}
	if err := c.Destination.Validate(); err != nil {
		return fmt.Errorf("destination: %w", err)
	}
	if c.Source.Engine == c.Destination.Engine {
		return fmt.Errorf("source and destination must be different engines")
	}
	return nil
}
