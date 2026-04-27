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

package sandboxclient

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"

	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// forwardPortToPod executes the low-level SPDY stream connection to forward ports.
func (c *Client) forwardPortToPod(ctx context.Context, cancel context.CancelFunc, podName string, localPort, targetPort int) error {
	req := c.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(c.namespace).
		Name(podName).
		SubResource("portforward")

	transport, upgrader, err := spdy.RoundTripperFor(c.restConfig)
	if err != nil {
		return fmt.Errorf("error creating round tripper: %w", err)
	}

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, req.URL())

	readyChan := make(chan struct{})
	ports := []string{fmt.Sprintf("%d:%d", localPort, targetPort)}

	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer

	pf, err := portforward.New(dialer, ports, ctx.Done(), readyChan, &stdoutBuf, &stderrBuf)
	if err != nil {
		return fmt.Errorf("error creating portforward: %w", err)
	}

	errChan := make(chan error, 1)
	go func() {
		errChan <- pf.ForwardPorts()
	}()

	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()

	select {
	case <-readyChan:
		return nil
	case err := <-errChan:
		cancel()
		return fmt.Errorf("port forwarding failed: %w", err)
	case <-timer.C:
		cancel()
		return fmt.Errorf("timeout waiting for port-forward to be ready")
	}
}
