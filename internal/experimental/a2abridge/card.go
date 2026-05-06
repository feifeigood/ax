// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package a2abridge

import (
	"fmt"
	"net/url"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// OverrideCardHosts rewrites the host:port portion of every URL in
// card.SupportedInterfaces[] to match the host:port of addr.
// Used for A2A agents whose AgentCard typically declares localhost URLs
// that must be redirected to the actual worker address discovered at
// connect time.
func OverrideCardHosts(card *a2a.AgentCard, addr string) error {
	if card == nil {
		return nil
	}
	addrURL, err := url.Parse(addr)
	if err != nil {
		return fmt.Errorf("OverrideCardHosts: parse addr %q: %w", addr, err)
	}
	if addrURL.Host == "" {
		return fmt.Errorf("OverrideCardHosts: addr %q has no host", addr)
	}
	for i, iface := range card.SupportedInterfaces {
		if iface == nil {
			continue
		}
		u, err := url.Parse(iface.URL)
		if err != nil {
			return fmt.Errorf("OverrideCardHosts: parse SupportedInterfaces[%d].URL %q: %w", i, iface.URL, err)
		}
		u.Host = addrURL.Host
		iface.URL = u.String()
	}
	return nil
}
