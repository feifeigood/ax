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

// Package sandboxclient provides a high-level Go client for interacting with
// the Kubernetes Agent Sandbox controllers, mirroring the official Python client.
package sandboxclient

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	SandboxGVR = schema.GroupVersionResource{
		Group:    "agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxes",
	}
	SandboxClaimGVR = schema.GroupVersionResource{
		Group:    "extensions.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxclaims",
	}
)

// Client represents a high-level client for managing Sandbox resources in Kubernetes.
type Client struct {
	dynClient dynamic.Interface
	namespace string
}

// NewClient creates a new Sandbox client using the local kubeconfig or cluster environment.
func NewClient(namespace string) (*Client, error) {
	// 1. Try in-cluster config first
	config, err := rest.InClusterConfig()
	if err != nil {
		// 2. Fall back to local kubeconfig
		kubeconfig := clientcmd.NewDefaultClientConfigLoadingRules().GetDefaultFilename()
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("failed to load kubernetes configuration: %w", err)
		}
	}

	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	return &Client{
		dynClient: dynClient,
		namespace: namespace,
	}, nil
}

// CreateClaim provisions an ephemeral sandbox execution environment.
func (c *Client) CreateClaim(ctx context.Context, claimName, templateRef string) error {
	claim := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "extensions.agents.x-k8s.io/v1alpha1",
			"kind":       "SandboxClaim",
			"metadata": map[string]interface{}{
				"name":      claimName,
				"namespace": c.namespace,
			},
			"spec": map[string]interface{}{
				"sandboxTemplateRef": map[string]interface{}{
					"name": templateRef,
				},
			},
		},
	}

	_, err := c.dynClient.Resource(SandboxClaimGVR).Namespace(c.namespace).Create(ctx, claim, metav1.CreateOptions{})
	return err
}

// DeleteClaim destroys an ephemeral sandbox execution environment.
func (c *Client) DeleteClaim(ctx context.Context, claimName string) error {
	return c.dynClient.Resource(SandboxClaimGVR).Namespace(c.namespace).Delete(ctx, claimName, metav1.DeleteOptions{})
}

// WaitForSandbox waits for the Sandbox resource to be ready and returns the pod selector.
func (c *Client) WaitForSandbox(ctx context.Context, claimName string) (selector string, err error) {
	for i := 0; i < 300; i++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(1 * time.Second):
			s, err := c.dynClient.Resource(SandboxGVR).Namespace(c.namespace).Get(ctx, claimName, metav1.GetOptions{})
			if err != nil {
				continue
			}

			sel, ok, _ := unstructured.NestedString(s.Object, "status", "selector")
			if ok && sel != "" {
				selector = sel
			}

			svc, found, _ := unstructured.NestedString(s.Object, "status", "service")
			if found && svc != "" {
				return selector, nil // Return once the service status is actively populated
			}
		}
	}
	return "", fmt.Errorf("timeout waiting for sandbox service IP for claim %s", claimName)
}

// WaitForSandboxReady waits for the Sandbox to become Ready.
func (c *Client) WaitForSandboxReady(ctx context.Context, claimName string) error {
	waitCmd := exec.CommandContext(ctx, "kubectl", "wait", "--for=condition=Ready", "--namespace", c.namespace, "sandbox.agents.x-k8s.io/"+claimName, "--timeout=120s")
	if err := waitCmd.Run(); err != nil {
		return fmt.Errorf("failed waiting for sandbox %s to be ready: %w", claimName, err)
	}
	return nil
}

// PortForward establishes a local connection to the remote sandbox pod over the given port.
// It returns a cleanup function the caller MUST execute when finished, closing the port-forward pipeline.
func (c *Client) PortForward(ctx context.Context, claimName, selector string, localPort, targetPort int) (cancelFunc func(), err error) {
	pfCtx, pfCancel := context.WithCancel(context.Background())

	// Wait for Sandbox to be ready first
	waitCmd := exec.CommandContext(pfCtx, "kubectl", "wait", "--for=condition=Ready", "--namespace", c.namespace, "sandbox.agents.x-k8s.io/"+claimName, "--timeout=120s")
	if err := waitCmd.Run(); err != nil {
		pfCancel()
		return nil, fmt.Errorf("failed waiting for sandbox %s to be ready: %w", claimName, err)
	}

	target := "svc/" + claimName
	if selector != "" {
		// Find the backing pod specifically to correctly link the port pipe with WarmPools
		findPodCmd := exec.CommandContext(pfCtx, "kubectl", "get", "pods", "--namespace", c.namespace, "-l", selector, "--field-selector=status.phase=Running", "-o", "name")
		out, err := findPodCmd.Output()
		if err == nil {
			podNames := strings.Split(strings.TrimSpace(string(out)), "\n")
			if len(podNames) > 0 && podNames[0] != "" {
				target = podNames[0]
			}
		}
	}

	cmd := exec.CommandContext(pfCtx, "kubectl", "port-forward", "--namespace", c.namespace, target, fmt.Sprintf("%d:%d", localPort, targetPort))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		pfCancel()
		return nil, fmt.Errorf("failed to start port-forward: %w", err)
	}

	// Give port-forwarding a moment to establish
	time.Sleep(10 * time.Second)

	return pfCancel, nil
}

// PortForwardRouter establishes a local connection to the Sandbox Router service.
// It returns a cleanup function the caller MUST execute when finished.
func (c *Client) PortForwardRouter(ctx context.Context, localPort, targetPort int) (cancelFunc func(), err error) {
	pfCtx, pfCancel := context.WithCancel(context.Background())

	// Wait for Router service to be ready
	waitCmd := exec.CommandContext(pfCtx, "kubectl", "wait", "--for=condition=Ready", "--namespace", c.namespace, "pod", "-l", "app=sandbox-router", "--timeout=120s")
	if err := waitCmd.Run(); err != nil {
		pfCancel()
		return nil, fmt.Errorf("failed waiting for sandbox-router to be ready: %w", err)
	}

	cmd := exec.CommandContext(pfCtx, "kubectl", "port-forward", "--namespace", c.namespace, "svc/sandbox-router-svc", fmt.Sprintf("%d:%d", localPort, targetPort))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		pfCancel()
		return nil, fmt.Errorf("failed to start router port-forward: %w", err)
	}

	// Give port-forwarding a moment to establish
	time.Sleep(2 * time.Second)

	return pfCancel, nil
}
