# Kubernetes deployment

Production-grade deployment for scuttlebot on Kubernetes.

## Architecture

```
                      LoadBalancer
                      :6667 (IRC)
                           │
              ┌────────────▼────────────┐
              │          ergo           │
              │    (single replica)     │
              │    ircd.db on PVC       │
              └────────────┬────────────┘
                           │ ClusterIP :8089
              ┌────────────▼────────────┐
              │       scuttlebot        │
              │  REST API :8080 (CIP)   │
              │  MCP      :8081 (CIP)   │
              └────────────┬────────────┘
                           │
              ┌────────────▼────────────┐
              │        Postgres         │
              │     (external, PaaS)    │
              └─────────────────────────┘
```

**Ergo is single-instance.** HA = fast pod restart with durable PVC, not horizontal scaling. `strategy: Recreate` is required — Ergo cannot run two pods sharing one `ReadWriteOnce` volume.

**Postgres is external.** Use your cloud provider's managed Postgres (RDS, Cloud SQL, etc.). Scuttlebot expects a `postgres-dsn` secret.

## Prerequisites

- A running Kubernetes cluster
- `kubectl` configured
- A Postgres instance reachable from the cluster
- Container images built and pushed (see below)

## Deploying

### 1. Build and push images

```sh
# scuttlebot
docker build -f deploy/docker/Dockerfile -t ghcr.io/conflicthq/scuttlebot:latest .
docker push ghcr.io/conflicthq/scuttlebot:latest

# ergo (custom image with envsubst)
docker build -f deploy/compose/ergo/Dockerfile -t ghcr.io/conflicthq/scuttlebot-ergo:latest deploy/compose/ergo/
docker push ghcr.io/conflicthq/scuttlebot-ergo:latest
```

### 2. Create the secret

```sh
kubectl create secret generic scuttlebot-secrets \
  --from-literal=ergo-api-token=$(openssl rand -hex 32) \
  --from-literal=postgres-dsn='postgres://scuttlebot:PASSWORD@HOST:5432/scuttlebot?sslmode=require'
```

Do **not** commit `scuttlebot-secret.yaml` with real values. The file in this directory is an example template only.

### 3. Apply manifests

```sh
kubectl apply -f deploy/k8s/
```

### 4. Watch rollout

```sh
kubectl rollout status deployment/ergo
kubectl rollout status deployment/scuttlebot
```

### 5. Get the API token

```sh
kubectl logs deployment/scuttlebot | grep "api token"
```

## Customising

| What | How |
|------|-----|
| IRC network name / server name | Edit `scuttlebot-configmap.yaml` |
| Storage class for Ergo PVC | Uncomment `storageClassName` in `ergo-pvc.yaml` |
| Expose REST API externally | Change `scuttlebot-api` service type to `LoadBalancer` or add an Ingress |
| Namespace | Add `namespace:` to all resource metadata |

## Secrets management

The example uses a plain Kubernetes Secret for simplicity. For production, prefer:
- [Sealed Secrets](https://github.com/bitnami-labs/sealed-secrets)
- [External Secrets Operator](https://external-secrets.io/)
- [HashiCorp Vault](https://www.vaultproject.io/)
