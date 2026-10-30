# Muster

Muster is a self-hosted alert grouping and on-call service that sits after Alertmanager. It turns Alertmanager's
notifications into Alert Groups — one problem with a number, a status and an Owner — and shows each Alert Group as one
editable message in Mattermost, in Slack, in Telegram, in any other messenger through a Bridge, or through an outgoing
webhook. People acknowledge, resolve and snooze an Alert Group from the chat or the web UI. One binary and one
PostgreSQL database are a complete installation.

This chart runs Muster on Kubernetes 1.30 or newer. It does not include PostgreSQL: Muster takes an external database,
PostgreSQL 14 or newer.

## Install

Create the Secret first: the chart never generates the master keys, and rendering fails without `existingSecret`.

```sh
kubectl create namespace muster
kubectl -n muster create secret generic muster \
  --from-literal=secret-keys="$(openssl rand -base64 32)" \
  --from-literal=database-password='<the database password>' \
  --from-literal=bootstrap-password="$(openssl rand -hex 16)"
```

Keep a copy of the master key apart from the database backups: Muster refuses to start on a database without the key
that wrote it.

The minimum `values.yaml`:

```yaml
existingSecret: muster

database:
  host: muster-db-rw.muster.svc
  name: muster
  user: muster
  sslmode: require

publicURL: https://muster.example.org

bootstrapAdmin:
  email: admin@example.org
  passwordKey: bootstrap-password
```

`publicURL` is the address people use; Muster stops at start without it. `ingestURL`, the address Alertmanager and the
messengers call, defaults to it. `bootstrapAdmin` creates the first Admin once.

```sh
helm install muster oci://ghcr.io/muster-io/charts/muster --version <version> \
  --namespace muster --create-namespace -f values.yaml
```

Each release signs the chart with cosign, keyless, with the identity of its release workflow, as it signs the image.

## Image registry and Ingress annotations

The image is `<registry>/<repository>:<tag>`: `image.registry` (`ghcr.io`) and `image.repository` (`muster-io/muster`).
`global.imageRegistry` replaces `image.registry` when set, for every image the chart renders. An `image.repository`
that starts with a registry host, such as `nexus.example.org/muster-io/muster`, is used as it is.

`ingress.annotations` applies to both Ingress objects, `muster-app` and `muster-ingest`. `ingress.app.annotations` and
`ingress.ingest.annotations` are merged over a copy of it for their own Ingress, and their keys win. The
`httpRoute.annotations`, `httpRoute.app.annotations` and `httpRoute.ingest.annotations` work the same way.

## Documentation

- [Install with Helm](https://muster-io.github.io/muster/latest/install/helm/): the Secret, the values, the two public
  addresses, Ingress and Gateway API routes, replicas, alert rules and the dashboard.
- [Chart values](https://muster-io.github.io/muster/latest/reference/chart-values/): every value with its type and
  default.
- [Verify a release](https://muster-io.github.io/muster/latest/install/versions/#verify-a-release): check the
  signatures of the chart and the image.
- [Documentation](https://muster-io.github.io/muster/) and the
  [changelog](https://github.com/muster-io/muster/blob/master/CHANGELOG.md).

Muster is licensed under AGPL-3.0-only.
