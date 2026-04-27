# ATE Agents

ATE agents are experimental, they can be removed or significantly
changed any time.

## Setup

To install the manifests on your Kubernetes cluster, run:

```bash
export PROJECT_ID="YOUR_PROJECT_ID"
export GCE_REGION="us-central1"
export BUCKET_NAME="ate-agent-snapshots"

gcloud storage buckets create --project=${PROJECT_ID} --location=${GCE_REGION} "gs://${BUCKET_NAME}" --uniform-bucket-level-access

kubectl apply -f manifest.yaml
```

## Run AX with ATE

```bash
kubectl port-forward -n ate-system service/api '35601:443'

make install-ate && \
  ax --config-file ./internal/examples/ate_agent/ax.yaml --input "say hello world"
```
