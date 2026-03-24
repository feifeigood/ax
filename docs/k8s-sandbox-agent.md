# Kubernetes Sandbox Agents

AX supports dynamically provisioning secure, isolated agents on Kubernetes via the [Agent Sandbox](https://github.com/kubernetes-sigs/agent-sandbox) feature. When an agent requires a Sandbox Agent, the AX server requests a temporary remote agent container in the cluster, establishes a secure connection locally (using port-forwarding via a proxy service), and cleans up the sandbox claim automatically upon closing.

## Prerequisites
- A running Kubernetes cluster.
- The [Agent Sandbox Controller](https://github.com/kubernetes-sigs/agent-sandbox?tab=readme-ov-file#installation) installed.
- `kubectl` installed and configured locally.

## Setup: Deploying the Router
Before using Sandbox Agents remotely and developing locally, you must deploy the `sandbox-router` into your cluster. This router proxies traffic securely to the isolated gVisor pods (direct port-forwarding to gVisor pods is not supported by Kubernetes due to netstack isolation).

1. Cross-compile the proxy binary via Make and inject it into an Alpine Pod:
```bash
# Build the router for linux and copy it into the cluster
make build-router
kubectl apply -f cmd/sandbox-router/sandbox-router.yaml
kubectl wait --for=condition=Ready pod -l app=sandbox-router --timeout=60s
POD_NAME=$(kubectl get pods -l app=sandbox-router -o jsonpath='{.items[0].metadata.name}')
kubectl cp sandbox-router $POD_NAME:/app/sandbox-router
kubectl exec $POD_NAME -- touch /app/.done
```

2. Expose it as a service:
```bash
kubectl expose deployment sandbox-router --port=8080 --target-port=8080
```

To use a Sandbox Agent, specify it in your `ax.yaml` configuration:

```yaml
registry:
  k8s_sandbox_agents:
    - id: "my-sandbox-agent"
      name: "Sandbox Worker"
      description: "An ephemeral sandbox processor"
      sandbox_template_ref: "your-gke-sandbox-template-name"
      container_port: 8494
```

## End-to-End Example (Uppercase Agent)

AX provides a complete example of a Sandbox Agent in `examples/k8s_sandbox_agent/`. It receives text input via gRPC and returns the same text converted to uppercase.

You can test this agent end-to-end using the `ax` binary, which exercises the full `SandboxAgent` lifecycle (provisioning, port-forwarding, and remote execution).

**1. Build the Agent Image**

From the root of the AX repository:
```bash
docker build -t ax-uppercase:latest -f examples/k8s_sandbox_agent/Dockerfile .
```

**2. Publish Image to Registry**

When deploying to a cluster, you can host the agent container image in **any standard container registry** accessible by your Kubernetes cluster (e.g., Docker Hub, Google Artifact Registry, GitHub Container Registry).
- For production, update the `image` field in `examples/k8s_sandbox_agent/sandbox-template-and-pool.yaml` to your full registry path.

Once the image is available, register the SandboxTemplate:
```bash
kubectl apply -f examples/k8s_sandbox_agent/sandbox-template-and-pool.yaml
```

**3. Configure ax.yaml**
Ensure your `ax.yaml` references this sandbox agent:
```yaml
registry:
  k8s_sandbox_agents:
    - id: "uppercase"
      sandbox_template_ref: "uppercase-agent-template"
      container_port: 8494
      use_router: true
```

**4. Run the Server**
```bash
ax serve --config ax.yaml
```

**5. Run the Agent**
In a separate terminal:
```bash
ax exec --agent "uppercase" --input "hello world"
```

The system will dynamically create a `SandboxClaim`, establish a connection via `kubectl port-forward`, execute the code securely, and return the result.


## Viewing Sandbox Logs
If you want to monitor the internal agent output or see if your gVisor sandbox received the physical gRPC requests:

1. List the active sandbox Pods:
```bash
kubectl get pods -n default
```

2. Fetch the logs for your specific sandbox claim:
```bash
kubectl logs -f ax-claim-uppercase-<hash_id>
```
