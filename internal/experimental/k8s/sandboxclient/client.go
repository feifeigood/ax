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
	"log"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	sandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	agentsclientset "sigs.k8s.io/agent-sandbox/clients/k8s/clientset/versioned"
	extclientset "sigs.k8s.io/agent-sandbox/clients/k8s/extensions/clientset/versioned"
	extensionsv1alpha1 "sigs.k8s.io/agent-sandbox/extensions/api/v1alpha1"
)

const routerLabelSelector = "app=sandbox-router"

// Client implements a high-level client for managing Sandbox resources in Kubernetes.
//
// It holds three typed clientsets — one per API group:
//
//   - agentsClient: for Sandbox resources (agents.x-k8s.io/v1alpha1).
//     Used to Watch/Get sandboxes and check the Ready condition.
//
//   - extClient: for SandboxClaim, SandboxTemplate, SandboxWarmPool
//     (extensions.agents.x-k8s.io/v1alpha1). Used to Create/Delete claims.
//
//   - clientset: for core Kubernetes resources (pods, services).
//     Needed for pod discovery during port-forward setup and for the
//     CoreV1().RESTClient() that SPDY port-forward tunneling requires.
type Client struct {
	agentsClient agentsclientset.Interface // Handles Sandbox resources (agents.x-k8s.io/v1alpha1)
	extClient    extclientset.Interface    // Handles SandboxClaim, SandboxTemplate, SandboxWarmPool (extensions.agents.x-k8s.io/v1alpha1)
	clientset    kubernetes.Interface      // Handles Core Kubernetes resources (Pods, Services, REST client)
	restConfig   *rest.Config
	namespace    string
}

// NewClient creates a new Sandbox client using the local kubeconfig or cluster environment.
func NewClient(namespace string) (*Client, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig := clientcmd.RecommendedHomeFile
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
		}
	}

	agentsClient, err := agentsclientset.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create agents clientset: %w", err)
	}

	extClient, err := extclientset.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create extensions clientset: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes clientset: %w", err)
	}

	return &Client{
		agentsClient: agentsClient,
		extClient:    extClient,
		clientset:    clientset,
		restConfig:   config,
		namespace:    namespace,
	}, nil
}

// CreateClaim provisions an ephemeral sandbox execution environment by creating a SandboxClaim.
func (c *Client) CreateClaim(ctx context.Context, claimName, templateRef string) error {
	claim := &extensionsv1alpha1.SandboxClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: claimName,
		},
		Spec: extensionsv1alpha1.SandboxClaimSpec{
			TemplateRef: extensionsv1alpha1.SandboxTemplateRef{
				Name: templateRef,
			},
		},
	}

	_, err := c.extClient.ExtensionsV1alpha1().SandboxClaims(c.namespace).
		Create(ctx, claim, metav1.CreateOptions{})
	return err
}

// ResolveSandboxName watches the SandboxClaim until its status contains the resolved
// Sandbox name (populated by the controller after a warm-pool pod is assigned).
func (c *Client) ResolveSandboxName(ctx context.Context, claimName string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	watcher, err := c.extClient.ExtensionsV1alpha1().SandboxClaims(c.namespace).
		Watch(ctx, metav1.ListOptions{
			FieldSelector: fmt.Sprintf("metadata.name=%s", claimName),
		})
	if err != nil {
		return "", fmt.Errorf("failed to watch SandboxClaim %s: %w", claimName, err)
	}
	defer watcher.Stop()

	for event := range watcher.ResultChan() {
		if event.Type == watch.Error {
			continue
		}
		claim, ok := event.Object.(*extensionsv1alpha1.SandboxClaim)
		if !ok {
			continue
		}
		// In the current schema, the Sandbox name is identical to the claim name,
		// and we wait for the claim to be Ready to ensure it's bound.
		for _, cond := range claim.Status.Conditions {
			if string(cond.Type) == "Ready" && string(cond.Status) == string(metav1.ConditionTrue) {
				return claimName, nil
			}
		}
	}
	return "", fmt.Errorf("timeout waiting for sandbox readiness on claim %s", claimName)
}

// ResolveSandboxStatus watches the Sandbox until its status is fully populated,
// returning the label selector and service name needed for port-forwarding.
func (c *Client) ResolveSandboxStatus(ctx context.Context, sandboxName string, timeout time.Duration) (selector, svc string, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	watcher, err := c.agentsClient.AgentsV1alpha1().Sandboxes(c.namespace).
		Watch(ctx, metav1.ListOptions{
			FieldSelector: fmt.Sprintf("metadata.name=%s", sandboxName),
		})
	if err != nil {
		return "", "", fmt.Errorf("failed to watch Sandbox %s: %w", sandboxName, err)
	}
	defer watcher.Stop()

	for event := range watcher.ResultChan() {
		if event.Type == watch.Error {
			continue
		}
		sandbox, ok := event.Object.(*sandboxv1alpha1.Sandbox)
		if !ok {
			continue
		}
		if sandbox.Status.LabelSelector != "" && sandbox.Status.Service != "" {
			return sandbox.Status.LabelSelector, sandbox.Status.Service, nil
		}
	}
	return "", "", fmt.Errorf("timeout waiting for sandbox status for %s", sandboxName)
}

// WaitForSandboxReady watches the Sandbox until the Ready condition is True.
func (c *Client) WaitForSandboxReady(ctx context.Context, sandboxName string) error {
	watcher, err := c.agentsClient.AgentsV1alpha1().Sandboxes(c.namespace).
		Watch(ctx, metav1.ListOptions{
			FieldSelector: fmt.Sprintf("metadata.name=%s", sandboxName),
		})
	if err != nil {
		return fmt.Errorf("failed to watch Sandbox %s: %w", sandboxName, err)
	}
	defer watcher.Stop()

	for event := range watcher.ResultChan() {
		sandbox, ok := event.Object.(*sandboxv1alpha1.Sandbox)
		if !ok {
			continue
		}
		for _, cond := range sandbox.Status.Conditions {
			if string(cond.Type) == string(sandboxv1alpha1.SandboxConditionReady) && string(cond.Status) == string(metav1.ConditionTrue) {
				return nil
			}
		}
	}
	return fmt.Errorf("timeout waiting for Sandbox %s to be ready", sandboxName)
}

// findRunningPod finds a Running pod matching the given label selector.
func (c *Client) findRunningPod(ctx context.Context, selector string) (string, error) {
	pods, err := c.clientset.CoreV1().Pods(c.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
		FieldSelector: "status.phase=Running",
	})
	if err != nil {
		return "", fmt.Errorf("failed to list pods with selector %s: %w", selector, err)
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no running pod found with selector %s", selector)
	}
	return pods.Items[0].Name, nil
}

// PortForward establishes a local connection to the remote sandbox pod over the given port.
// It returns a cleanup function the caller MUST execute when finished.
func (c *Client) PortForward(ctx context.Context, claimName, selector string, localPort, targetPort int) (cancelFunc func(), err error) {
	ctx, cancel := context.WithCancel(ctx)

	// 1. Resolve sandbox name from the claim
	sandboxName, err := c.ResolveSandboxName(ctx, claimName, 2*time.Minute)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to resolve sandbox for claim %s: %w", claimName, err)
	}

	// 2. Wait for Sandbox to be ready
	waitCtx, waitCancel := context.WithTimeout(ctx, 2*time.Minute)
	defer waitCancel()

	if err := c.WaitForSandboxReady(waitCtx, sandboxName); err != nil {
		cancel()
		return nil, fmt.Errorf("failed waiting for Sandbox %s to be ready: %w", sandboxName, err)
	}

	// 3. Resolve sandbox status to get the label selector if not provided
	if selector == "" {
		resolvedSelector, _, err := c.ResolveSandboxStatus(ctx, sandboxName, 30*time.Second)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("failed to resolve sandbox status for %s: %w", sandboxName, err)
		}
		selector = resolvedSelector
	}

	// 4. Find the specific running pod assigned to the claim
	podName, err := c.findRunningPod(ctx, selector)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to find running pod for %s: %w", claimName, err)
	}

	// 5. Establish SPDY port-forward (no kubectl subprocess)
	if err := c.forwardPortToPod(ctx, cancel, podName, localPort, targetPort); err != nil {
		cancel()
		return nil, err
	}

	return cancel, nil
}

// PortForwardRouter establishes a local connection to the Sandbox Router service.
func (c *Client) PortForwardRouter(ctx context.Context, localPort, targetPort int) (cancelFunc func(), err error) {
	ctx, cancel := context.WithCancel(ctx)

	routerPodName, err := c.waitForRouterPod(ctx, 2*time.Minute)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed waiting for sandbox-router to be ready: %w", err)
	}

	if err := c.forwardPortToPod(ctx, cancel, routerPodName, localPort, targetPort); err != nil {
		cancel()
		return nil, err
	}

	return cancel, nil
}

// waitForPod watches for a Ready pod matching the given selector.
func (c *Client) waitForPod(ctx context.Context, selector string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Check if already ready
	pods, err := c.clientset.CoreV1().Pods(c.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return "", fmt.Errorf("failed to list pods with selector %s: %w", selector, err)
	}
	for _, pod := range pods.Items {
		if isPodReady(&pod) {
			return pod.Name, nil
		}
	}

	// Watch until one becomes Ready
	watcher, err := c.clientset.CoreV1().Pods(c.namespace).Watch(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return "", fmt.Errorf("failed to watch pods with selector %s: %w", selector, err)
	}
	defer watcher.Stop()

	for event := range watcher.ResultChan() {
		pod, ok := event.Object.(*corev1.Pod)
		if !ok {
			continue
		}
		if isPodReady(pod) {
			return pod.Name, nil
		}
	}
	return "", fmt.Errorf("timeout waiting for pod with selector %s to be ready", selector)
}

// waitForRouterPod watches for a Ready sandbox-router pod.
func (c *Client) waitForRouterPod(ctx context.Context, timeout time.Duration) (string, error) {
	return c.waitForPod(ctx, routerLabelSelector, timeout)
}

func isPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// DeleteClaim deletes the SandboxClaim, releasing the sandbox back to the warm pool.
func (c *Client) DeleteClaim(ctx context.Context, claimName string) error {
	return c.extClient.ExtensionsV1alpha1().SandboxClaims(c.namespace).
		Delete(ctx, claimName, metav1.DeleteOptions{})
}

// WaitForSandbox wraps the name resolution, selector resolution, and readiness wait into a single call for callers.
func (c *Client) WaitForSandbox(ctx context.Context, claimName string) (string, error) {
	sandboxName, err := c.ResolveSandboxName(ctx, claimName, 2*time.Minute)
	if err != nil {
		return "", fmt.Errorf("failed to resolve sandbox name: %w", err)
	}

	selector, _, err := c.ResolveSandboxStatus(ctx, sandboxName, 30*time.Second)
	if err != nil {
		return "", fmt.Errorf("failed to resolve sandbox status: %w", err)
	}

	if err := c.WaitForSandboxReady(ctx, sandboxName); err != nil {
		log.Printf("Warning: failed waiting for Sandbox %s to be ready: %v", sandboxName, err)
	}

	return selector, nil
}
