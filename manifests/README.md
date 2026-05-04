# AX Deployment on Kubernetes

This directory contains Kubernetes manifests and configurations to deploy
and verify the AX on GKE using Substrate sandboxes.

The target Kubernetes cluster is assumed to have
[SubstrATE](https://github.com/ai-on-gke/SubstrATE) installed.

---

## 🚀 Deploying to Substrate

This option deploys to isolated, warm-standby actors. Workers are live-snapshotted on boot and instantly restored from GCS when a new conversation starts.

### 1. Deploy Sandboxed environment

```bash
kubectl apply -f manifests/ax-deployment.yaml
kubectl apply -f manifests/ax-service.yaml
```

### 2. Retrieve Public Router IP

```bash
kubectl get svc ax-router -n ax
```
*Wait until the `EXTERNAL-IP` transitions from `<pending>` to a public IP (e.g., `34.57.137.14`).*

### 3. Test End-to-End

```bash
ax exec --server=<EXTERNAL-IP>:443 --input="hello"
```
*Envoy will intercept the request and route traffic using the conversation ID.*

---

## 🛠️ Inspection & Diagnostics

Use the **`kubectl-ate`** CLI tool to inspect the live states of
active actors and allocated standby worker pool instances:

```bash
kubectl-ate get actors

kubectl-ate get workers
```
