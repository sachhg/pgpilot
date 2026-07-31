# Deploying pgpilot on Kubernetes

These manifests run pgpilot in front of an **existing** Postgres primary and its
replicas. pgpilot does not manage the databases; it proxies to them.

## Build and push the image

```sh
docker build -t <registry>/pgpilot:0.1.0 .
docker push <registry>/pgpilot:0.1.0
```

Then set that image in [`deployment.yaml`](deployment.yaml).

## Configure

pgpilot reads a single JSON config, and that config carries user passwords, so
the whole file is a **Secret** (not a ConfigMap), mounted read-only at
`/etc/pgpilot/pgpilot.json`. Edit [`secret.yaml`](secret.yaml):

- point `primary` and `replicas` at your Postgres Services;
- replace the `CHANGE_ME` password;
- adjust `fencing.mode` (`strict` for read-your-writes) and `routing.policy`.

Manage this Secret with your real secret tooling (sealed-secrets, SOPS, an
external secrets operator) — do not commit credentials.

## Apply

```sh
kubectl apply -f k8s/namespace.yaml
kubectl apply -f k8s/secret.yaml
kubectl apply -f k8s/deployment.yaml
kubectl apply -f k8s/service.yaml
```

Clients then connect to `pgpilot.pgpilot.svc.cluster.local:5432`.

## What the manifests set

- **Deployment** — two replicas; the config Secret mounted read-only; a
  liveness probe on the TCP listener and a readiness probe on `/metrics`;
  a locked-down security context (non-root, read-only root filesystem, all
  capabilities dropped, `RuntimeDefault` seccomp); and CPU/memory
  requests and limits.
- **Service** — `5432` for clients (to the proxy's `6432`) and `9090` for
  metrics.

## Metrics

The pod template carries `prometheus.io/scrape` annotations, which annotation-based
Prometheus configurations pick up automatically. With the Prometheus Operator,
add a `ServiceMonitor` selecting `app.kubernetes.io/name: pgpilot` on the
`metrics` port instead. Import the dashboard from
[`grafana/pgpilot-dashboard.json`](../grafana/pgpilot-dashboard.json).

## Notes

- Reloading the replica set: `SIGHUP` makes pgpilot re-read its config. Update the
  Secret and send the signal (e.g. `kubectl exec` a `kill -HUP 1`), or roll the
  Deployment.
- pgpilot refuses TLS today, so clients must permit a cleartext connection
  (`sslmode=prefer` falls back). Terminate TLS at a sidecar or mesh if required.
