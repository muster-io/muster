# Install with Helm

The Helm chart runs Muster on Kubernetes 1.30 or newer. It deploys Muster and its Services, and optionally an Ingress or
Gateway API routes, a PodDisruptionBudget, a ServiceMonitor, a NetworkPolicy, alert rules and a dashboard. It does not
include PostgreSQL: Muster takes an [external database](database.md). For a first try on one machine, use the
[Quick start](../quick-start.md) with Docker Compose instead.

```mermaid
flowchart LR
  browser["Browser"] -- "muster.example.org" --> app["app Service, 8080"]
  am["Alertmanager"] -- "ingest.example.org" --> ingest["ingest Service, 8081"]
  app --> pod["Muster pods"]
  ingest --> pod
  pod --> pg["PostgreSQL"]
  prom["Prometheus"] -- "/metrics" --> internal["internal Service, 8082"]
  internal --> pod
```

## Get the chart

Each release publishes the chart as an OCI artifact in the registry of the image:

```sh
helm install muster oci://ghcr.io/muster-io/charts/muster --version <version> \
  --namespace muster --create-namespace -f values.yaml
```

`<version>` is a Muster release without the `v`, such as `0.1.0` for the release `v0.1.0`. The chart's `appVersion` is
the release's tag, and `image.tag` defaults to it, so each chart runs the image of its own release.

The release signs the chart with cosign, keyless, as it signs the image; [Verify a release](versions.md#verify-a-release)
shows how to check the signature before you install. Artifact Hub lists the released versions of the chart, each
with its changes and the image it runs, pinned by digest: [Muster on Artifact Hub](https://artifacthub.io/packages/search?repo=muster).

## Create the Secret

The chart never generates the master keys. Create a Secret yourself and name it in `existingSecret`; rendering fails
without it. The Secret holds, under the key names that the values `masterKeysKey`, `database.passwordKey` and
`bootstrapAdmin.passwordKey` name:

`secret-keys`
:   the default of `masterKeysKey`

    Holds the master key of the Keyring, as `openssl rand -base64 32` prints it.

`database-password`
:   the default of `database.passwordKey`

    Holds the password of the database user, unless `database.existingSecret` names another Secret for it.

`bootstrap-password`
:   no default; set `bootstrapAdmin.passwordKey`

    Holds the password of the first Admin.

```sh
kubectl create namespace muster
kubectl -n muster create secret generic muster \
  --from-literal=secret-keys="$(openssl rand -base64 32)" \
  --from-literal=database-password='<the database password>' \
  --from-literal=bootstrap-password="$(openssl rand -hex 16)"
```

The pods mount the Secret as files and read the values through `MUSTER_SECRET_KEYS_FILE`,
`MUSTER_DATABASE_PASSWORD_FILE` and `MUSTER_BOOTSTRAP_ADMIN_PASSWORD_FILE`, so no secret appears in the pod's
environment, except the whole database URL when you name its key in `database.urlKey`.

When another operator keeps the database credentials in a Secret of its own, name that Secret in
`database.existingSecret` instead of copying the password: `database.passwordKey` and `database.urlKey` then name keys
of that Secret, and the master keys and the password of the first Admin stay in `existingSecret`. For the Secret
`muster-db-app` that CloudNativePG generates:

```yaml
existingSecret: muster
database:
  existingSecret: muster-db-app
  urlKey: uri
```

[The database](database.md#cloudnativepg) shows the form with `sslmode`.

!!! warning "Keep a copy of the master key"
    Muster refuses to start on a database without the key that wrote it, and a restored database is useless without it.
    Keep a copy **apart from the database backups**: see [Backup and restore](../operations/backup-and-restore.md).

A tool that manages Secrets, such as External Secrets or Sealed Secrets, works the same way: it only has to produce a
Secret of that name with those keys.

### Ship the Secret's source in the release

With GitOps tools such as Argo CD or Flux, the object that produces the Secret can be part of the same
release: `extraObjects` takes a list of extra manifests, and the chart renders each as a document of its own. An item
is a map or a string that holds a YAML manifest; both go through `tpl`, so a manifest can name `.Values` and `.Release`
and keeps the key names in one place. An `ExternalSecret` of External Secrets Operator that fills the Secret from a
secret store:

```yaml
existingSecret: muster

bootstrapAdmin:
  email: admin@example.org
  passwordKey: bootstrap-password

extraObjects:
  - apiVersion: external-secrets.io/v1
    kind: ExternalSecret
    metadata:
      name: '{{ .Values.existingSecret }}'
    spec:
      refreshInterval: 1h
      secretStoreRef: { kind: ClusterSecretStore, name: vault }
      target: { name: '{{ .Values.existingSecret }}' }
      data:
        - secretKey: '{{ .Values.masterKeysKey }}'
          remoteRef: { key: muster/keys, property: secret-keys }
        - secretKey: '{{ .Values.bootstrapAdmin.passwordKey }}'
          remoteRef: { key: muster/keys, property: bootstrap-password }
        - secretKey: '{{ .Values.database.passwordKey }}'
          remoteRef: { key: muster/database, property: password }
```

Quote a value that is only a template, as above, so that the item stays valid YAML. `bootstrapAdmin.passwordKey` has
no default, so set it before naming it. With `database.existingSecret`, the database password comes from that Secret
instead, and its line is left out.

`tpl` renders every `{{` of an item, so one meant for another tool, such as the template of an `ExternalSecret`, is
escaped as `{{ "{{ .password }}" }}`; unescaped, it renders empty. With `database.urlKey`, a key named
`{{ .Values.database.urlKey }}` takes the place of the `passwordKey` line, for example a database URL that External
Secrets Operator builds from the password:

```yaml
database:
  urlKey: database-url

extraObjects:
  - apiVersion: external-secrets.io/v1
    kind: ExternalSecret
    metadata:
      name: '{{ .Values.existingSecret }}'
    spec:
      refreshInterval: 1h
      secretStoreRef: { kind: ClusterSecretStore, name: vault }
      target:
        name: '{{ .Values.existingSecret }}'
        template:
          mergePolicy: Merge
          data:
            '{{ .Values.database.urlKey }}': 'postgres://muster:{{ "{{ .password }}" }}@muster-db-rw.muster.svc:5432/muster?sslmode=require'
      data:
        - secretKey: '{{ .Values.masterKeysKey }}'
          remoteRef: { key: muster/keys, property: secret-keys }
        - secretKey: '{{ .Values.bootstrapAdmin.passwordKey }}'
          remoteRef: { key: muster/keys, property: bootstrap-password }
        - secretKey: password
          remoteRef: { key: muster/database, property: password }
```

On the first install the pods wait in `Init:0/1`, with `FailedMount` events, until External Secrets Operator has
created the Secret; this is normal, and they start once it exists. A `SealedSecret` works the same way; a string item
suits it, since the encrypted data is pasted as it is:

```yaml
extraObjects:
  - |
    apiVersion: bitnami.com/v1alpha1
    kind: SealedSecret
    metadata:
      name: {{ .Values.existingSecret }}
    spec:
      encryptedData:
        secret-keys: <the output of kubeseal>
        database-password: <the output of kubeseal>
        bootstrap-password: <the output of kubeseal>
```

## Values

A short `values.yaml` for an installation with an Ingress and an external database:

```yaml
existingSecret: muster

database:
  host: muster-db-rw.muster.svc
  name: muster
  user: muster
  sslmode: require

publicURL: https://muster.example.org
ingestURL: https://ingest.example.org

bootstrapAdmin:
  email: admin@example.org
  passwordKey: bootstrap-password

replicas: 2
podDisruptionBudget:
  enabled: true

ingress:
  enabled: true
  className: nginx
  app:
    hosts:
      - host: muster.example.org
    tls:
      - secretName: muster-tls
        hosts: [muster.example.org]
  ingest:
    hosts:
      - host: ingest.example.org
    tls:
      - secretName: muster-ingest-tls
        hosts: [ingest.example.org]
```

`publicURL` is required: the chart renders without it, but Muster stops at start. The notes that `helm install` prints
warn about a missing `publicURL` or a missing database. `bootstrapAdmin` creates the first Admin once; after the first
Admin exists, Muster ignores it. Everything else about the installation — Connections, Integrations, Routes, people —
is configuration in the database and changes in the web UI and the API, never in the chart.

The full list of values, with types and defaults, is the [chart values reference](../reference/chart-values.md). The
chart rejects a value it does not know. Environment variables that have no value of their own go into `extraEnv`; the
[environment reference](../reference/environment.md) lists them.

The chart sets `MUSTER_MIGRATE_ON_START` off and runs `muster migrate` in an init container instead, under a database
lock, so that the pods of a rollout do not migrate at once. The pods run as a non-root user with a read-only root file
system. The readiness probe checks only the database, and a startup probe gives a slow start up to five minutes.

## The two public addresses

Muster has two addresses that other systems use, and the chart takes both as values:

`publicURL`
:   environment variable: `MUSTER_PUBLIC_URL`

    Used for people: the web UI, links in messages, the OIDC redirect.

`ingestURL`
:   environment variable: `MUSTER_INGEST_URL`

    Used for machines: Alertmanager, Heartbeats, Mattermost button callbacks, the Telegram webhook, and the button
    presses of Slack Connections in the HTTP event mode.

`ingestURL` defaults to `publicURL`. Muster never derives either from an Ingress or a route: set them to the addresses
that people and machines really use, or the links it builds will be wrong.

Muster serves ingestion on its own listener (`8081`) apart from the web UI and the API (`8080`), and the chart gives
each a Service. A separate address for ingestion lets you expose the two differently: the web UI only inside the company
network, and ingestion to Alertmanagers in other networks, or the other way round, with different TLS settings, rate
limits and firewall rules on each. It also keeps a burst of Snapshots away from the web UI's address. When you have no
such need, give both the same host name and send the paths `/api/v1/ingest`, `/api/v1/heartbeat` and
`/api/v1/callbacks/` (with everything below them) to the ingest Service and everything else to the app Service, as
[Connect Alertmanager](../integrations/alertmanager.md#behind-a-reverse-proxy) describes.

The third listener, `8082`, serves `/health/live`, `/health/ready` and `/metrics`. It has a Service and no Ingress or
route; do not expose it outside the cluster.

## Ingress

`ingress.enabled` creates two Ingress objects, one for the app Service and one for the ingest Service. List the hosts of
each under `ingress.app.hosts` and `ingress.ingest.hosts`, with their `tls` blocks, and set `ingress.className` and
`ingress.annotations` for your controller. A host takes an optional `path` (`/` by default) and `pathType` (`Prefix`).
Annotations apply to both Ingress objects. Set the body size limit of your controller to at least 16 MB for the ingest
host, because Muster accepts Snapshots up to that size.

## Gateway API

`httpRoute.enabled` creates two `HTTPRoute` objects (`gateway.networking.k8s.io/v1`) instead of Ingress objects, one
for each Service. Name the Gateway in `httpRoute.parentRefs` and the host names in `httpRoute.app.hostnames` and
`httpRoute.ingest.hostnames`:

```yaml
httpRoute:
  enabled: true
  parentRefs:
    - name: public
      namespace: gateway
  app:
    hostnames: [muster.example.org]
  ingest:
    hostnames: [ingest.example.org]
```

The chart does not create the Gateway and its certificates. The Gateway API resources must be installed in the cluster
and the Gateway must accept routes from the namespace of Muster. Use `ingress` or `httpRoute`, not both.

## Replicas and the PodDisruptionBudget

`replicas` is 1 by default. All replicas are active: each serves the web UI and ingestion and works on the queues, and a
Leader among them does the few jobs that must run only once. With more than one replica:

- Set `podDisruptionBudget.enabled` to `true`. The budget allows one unavailable pod (`maxUnavailable`), so a node drain
  never takes all replicas down at once.
- Spread the pods over nodes with `topologySpreadConstraints` or `affinity`.
- If the main database connection goes through PgBouncer in transaction mode, set `database.sessionHost` (and
  `sessionPort`) to a session-capable endpoint. See [the database](database.md#pgbouncer).
- Keep the clocks of all nodes in sync with NTP; Muster compares them with the database's clock and logs a warning on a
  skew, and the chart rule `MusterClockSkew` raises an alert.

Rolling updates run between adjacent minor versions only, and the init container of each new pod applies the database
migrations before the pod starts. Take a backup first.

## Watch Muster

`serviceMonitor.enabled` makes the Prometheus Operator scrape `/metrics` of the internal Service.
`alerting.prometheusRule.enabled` or `alerting.vmRule.enabled` renders the alert rules, and `dashboard.configMap.enabled`
or `dashboard.grafanaDashboard.enabled` the Grafana dashboard. The rules and the dashboard are reference material; the
[Runbooks](../operations/runbooks/index.md) explain each alert, and the [metrics reference](../reference/metrics.md)
lists what Muster exports.
