// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controller

import (
	"sync"
	"time"

	"github.com/google/gar/internal/config"
)

// HealthMonitor manages the periodic health checking of agents.
type HealthMonitor struct {
	config   config.HealthCheckConfig
	registry *Registry
	ticker   *time.Ticker
	stop     chan struct{}
	wg       sync.WaitGroup
	stopOnce sync.Once
}

// NewHealthMonitor creates a new health monitor.
func NewHealthMonitor(cfg config.HealthCheckConfig, r *Registry) *HealthMonitor {
	return &HealthMonitor{
		config:   cfg,
		registry: r,
		stop:     make(chan struct{}),
	}
}

func (hm *HealthMonitor) Start() {
	interval := hm.config.Interval
	hm.ticker = time.NewTicker(interval)
	hm.wg.Add(1)
	go hm.run()
}

// Stop halts the health check loop.
func (hm *HealthMonitor) Stop() {
	if hm.ticker != nil {
		hm.ticker.Stop()
		hm.stopOnce.Do(func() {
			close(hm.stop)
		})
		hm.wg.Wait()
	}
}

// run is the main loop for health checks.
func (hm *HealthMonitor) run() {
	defer hm.wg.Done()

	for {
		select {
		case <-hm.stop:
			return
		case <-hm.ticker.C:
			hm.performChecks()
		}
	}
}

// performChecks triggers health checks for all agents.
func (hm *HealthMonitor) performChecks() {
	// Get IDs safely
	hm.registry.mu.RLock()
	ids := make([]string, 0, len(hm.registry.agents))
	for id := range hm.registry.agents {
		ids = append(ids, id)
	}
	hm.registry.mu.RUnlock()

	// Run checks in parallel
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(agentID string) {
			defer wg.Done()
			_ = hm.registry.healthCheck(agentID)
		}(id)
	}
	wg.Wait()
}
