# operator-go v0.13 Migration Guide

This release moves SparkHistoryServer reconciliation from the legacy
operator-go v0.12 `BaseCluster` stack to the v0.13 `GenericReconciler` and
`RoleGroupHandler` stack. The CR spec remains compatible, but the controller,
status schema, generated resources, and cleanup bookkeeping all change.

Use this procedure for an existing installation. A fresh installation only
needs Kubernetes 1.29 or newer and the normal Helm install flow.

## Before the Upgrade

1. Confirm the Kubernetes server is 1.29 or newer. Vector and oauth2-proxy run
   as restartable init containers (native sidecars), which require Kubernetes
   1.29.
2. Back up the CRD and every SparkHistoryServer CR:

   ```bash
   kubectl get crd sparkhistoryservers.spark.kubedoop.dev -o yaml \
     > sparkhistoryserver-crd.before-v013.yaml
   kubectl get sparkhistoryservers.spark.kubedoop.dev -A -o yaml \
     > sparkhistoryservers.before-v013.yaml
   ```

3. Inventory role-group resource-name lengths:

   ```bash
   kubectl get sparkhistoryservers.spark.kubedoop.dev -A -o json |
     jq -r '.items[] as $cr |
       (($cr.spec.node.roleGroups // {}) | keys[]) as $group |
       [$cr.metadata.namespace, $cr.metadata.name, $group,
        ([$cr.metadata.name, "node", $group] | join("-")),
        (([$cr.metadata.name, "node", $group] | join("-")) | length)] | @tsv'
   ```

   operator-go v0.13 hashes a role-group base name longer than 54 characters.
   The old controller used the natural `<cluster>-node-<group>` name. If a CR
   already owns a natural-name StatefulSet, Service, or ConfigMap above this
   boundary, the new controller deliberately refuses to reconcile it instead
   of creating a duplicate hashed workload. Migrate that group to a shorter
   name during a maintenance window before upgrading. New clusters with long
   names can use the v0.13 hashed name safely.
4. Review every effective `config.cleaner` setting. The old implementation
   normally failed to activate the cleaner because it compared the role name
   with the role-group name. The new implementation honors role and role-group
   precedence and will begin deleting event logs according to Spark's cleaner
   policy. Correct retention settings before the first new reconcile.
5. Plan for one OIDC login interruption if authentication is enabled. The new
   controller uses a pinned oauth2-proxy native sidecar and a generated cookie
   Secret, so existing login sessions are not reusable.
6. Inspect S3 and OIDC TLS verification. This release supports the system
   WebPKI trust store (server.caCert.webPki) only. It rejects
   verification.none and private CAs referenced through
   server.caCert.secretClass before changing workloads, because the shipped
   Spark/oauth2-proxy containers do not yet receive a matching trust store.
7. For MinIO or another path-style S3 endpoint, set
   S3Connection.spec.pathStyle to true explicitly. The v0.12 controller forced
   path-style access regardless of that field. The v0.13 compatibility layer
   keeps path-style access for an owned legacy StatefulSet, but an explicit
   value makes the requirement portable to a fresh install. To move an
   upgraded workload to virtual-host style later, set the
   spark.hadoop.fs.s3a.path.style.access key under the spark-defaults.conf
   configOverrides file to false in a separate rollout.

## Required Upgrade Order

### 1. Apply the CRD explicitly

Helm does not upgrade files from a chart's `crds/` directory. Apply the new
superset schema before starting the new controller:

```bash
kubectl apply \
  -f deploy/helm/spark-k8s-operator/crds/crds.yaml

kubectl explain \
  sparkhistoryservers.status.observedGeneration \
  --api-version=spark.kubedoop.dev/v1alpha1
kubectl explain \
  sparkhistoryservers.status.roleGroups \
  --api-version=spark.kubedoop.dev/v1alpha1
```

For a published OCI chart, render its CRDs with `helm show crds` and pipe that
output to `kubectl apply -f -` before `helm upgrade`.

The new schema intentionally retains the legacy `status.generation`, `name`,
`type`, and `urls` fields. Keeping this superset CRD is also required for a
safe controller rollback.

### 2. Record the live workload identity

Capture the StatefulSet UID, selectors, Service endpoints, and ready replicas.
They are the continuity evidence for the upgrade:

```bash
kubectl get sts,svc,endpoints -n <namespace> \
  -l app.kubernetes.io/instance=<cluster> -o yaml \
  > sparkhistoryserver-resources.before-v013.yaml
```

### 3. Upgrade only the controller

Upgrade the chart without otherwise changing the SparkHistoryServer CR when
possible; this leaves the smallest migration surface. If a v0.12 role group
was already removed from the spec, the compatibility extension recognizes its
exact name, owner, and full legacy label fingerprint on every remaining
StatefulSet, ConfigMap, client Service, or metrics Service. It reconstructs
the status cleanup ledger without mutating those objects, then lets the v0.13
cleaner reclaim them in order. It refuses ambiguous or long-name legacy slots
instead of silently leaking or deleting them.

### 4. Wait for every CR to reconcile

```bash
kubectl wait sparkhistoryservers.spark.kubedoop.dev/<cluster> \
  -n <namespace> \
  --for='condition=ReconcileComplete=True' \
  --timeout=10m

kubectl get sparkhistoryservers.spark.kubedoop.dev/<cluster> \
  -n <namespace> \
  -o jsonpath='{.status.observedGeneration}{"\n"}{.status.roleGroups}{"\n"}'
```

Verify that the StatefulSet UID is unchanged, its selector still matches the
Pod template, the client and metrics Services still select the Pods, and the
client Service endpoints remain populated. Verify that any role group removed
before the upgrade no longer has a StatefulSet, ConfigMap, client Service, or
metrics Service.

## Compatibility Preserved by the Controller

- StatefulSet, ConfigMap, client Service, metrics Service, and PDB names remain
  unchanged for existing supported names.
- The legacy StatefulSet selector, `serviceName`, and `OrderedReady` policy are
  retained. No additional headless Service is introduced.
- The client and metrics Service selectors and the role PDB selector continue
  to match the legacy Pod labels. Upgrade acceptance also requires each PDB's
  `status.observedGeneration` to cover its current `metadata.generation`, so a
  stale pre-selector-change status cannot satisfy the health gate.
- Service labels and annotations remain stable apart from the reviewed
  framework `managed-by`, version, and resource-slot labels. In particular,
  the metrics Service keeps `prometheus.io/scrape=true` plus the port `18081`
  and scheme `http` annotations across upgrade and rollback.
- A disabled or removed v0.12 role PDB is deleted only after an exact
  name/owner/legacy-label check. An enabled PDB is adopted and receives the
  v0.13 role-slot marker, so later disablement is handled by normal framework
  cleanup.
- Object metadata carries the v0.13 `managed-by=operator-go` and slot labels so
  lifecycle cleanup can discover reconciled resources. For a group removed
  before upgrade, any remaining legacy StatefulSet, ConfigMap, client Service,
  or metrics Service reconstructs the cleanup ledger across controller
  restarts. Pod identity labels remain compatible with the immutable legacy
  StatefulSet selector.
- The root container security context, Kubernetes service-link behavior, and
  the Vector log-volume size remain compatible with v0.12.
- An adopted workload keeps the v0.12 config and S3 credential mount modes and
  the S3 Secret generic-ephemeral PVC request of 1Mi. Explicit podOverrides
  remain authoritative. Fresh v0.13 workloads use the framework's read-only
  mounts and 10Mi Secret volume default.
- A configured memory limit remains limit-only in the StatefulSet template.
  An explicit `podOverrides` memory request still wins. This avoids an
  unnecessary template rollout and preserves behavior in clusters where an
  admission policy supplies a request different from the limit.
- An upgraded StatefulSet keeps its live ServiceAccount (normally `default`)
  unless `podOverrides` explicitly selects another one. This preserves legacy
  image-pull secrets, RBAC, and admission identity. Fresh v0.13 clusters use
  the framework's per-CR ServiceAccount.
- A non-empty legacy vectorAggregatorConfigMapName remains the default switch
  for Vector. An explicit role or role-group
  config.logging.enableVectorAgent: false has higher precedence and disables
  it.
- Legacy regular-container podOverrides for oidc and vector are migrated onto
  their restartable init containers with Kubernetes strategic-merge semantics.
  This includes command/args, env.valueFrom, resources, mounts, ports,
  lifecycle, security context, and probes; the old Vector config mount is
  translated to the v0.13 path. An OIDC port override must still expose 4180
  because the client Service routes to that fixed entrypoint.
- The native Vector container gets numeric uid/gid 1001 when no usable non-root
  pod or container identity is present. The spark-k8s image declares the named
  user `kubedoop`, which kubelet cannot validate against `runAsNonRoot: true`
  without this numeric identity. A non-root pod identity remains authoritative;
  an explicit pod uid 0 is isolated from Vector with container uid 1001 and
  from the pinned oauth2-proxy image with its numeric uid 65532. Container-level
  `podOverrides.securityContext` entries are applied last.
- `image.pullSecretName` is now forwarded to the workload Pod.

The v0.13 reconciler also creates a per-CR ServiceAccount named
`sparkhistoryserver-<cluster>` even when an upgraded legacy workload keeps its
current account. When OIDC is enabled it creates a generated cookie Secret
named `<cluster>-oauth2-cookie`. Both are owned by the CR.

## Intentional Behavior Changes

- Generic status conditions, `observedGeneration`, and per-role-group status
  are now populated. A healthy acceptance snapshot requires both the top-level
  value and every healthy condition's `observedGeneration` to equal the CR's
  current generation. Legacy status fields remain readable for rollback.
- User `configOverrides`, `envOverrides`, `cliOverrides`, and `podOverrides`
  keep precedence over product defaults. In particular, Spark properties that
  were previously overwritten are now honored.
  Overrides of framework-owned log4j2.properties are also preserved without
  post-processing. A vector.yaml override can replace the generated file only
  after Vector has been enabled through a valid vectorAggregatorConfigMapName;
  it is not an alternative source of enablement or aggregator discovery, and
  the flat configOverrides shape is unsuitable for arbitrary nested Vector
  YAML. The Spark CRD's historical cliOverrides contract remains
  unchanged: role-group values are appended to role values, the resulting
  non-empty slice replaces the main command, and a final main-container
  podOverrides command/args wins. If only podOverrides command or args is set,
  the controller first restores the v0.12 command/args split so the untouched
  half keeps its historical meaning.
  operator-go v0.13 normally rejects a main-container volumeMount that replaces
  a framework mount at the same mountPath. For upgrade compatibility, Spark
  retains the v0.12 strategic-merge result when the replacement volume is
  declared: only that conflicting mount patch is replayed after the framework
  build, including readOnly, subPath, and subPathExpr. Named and unnamed main
  container overrides are supported. Non-conflicting mounts and undeclared
  replacements still go through the framework's normal validation.
- `spark-defaults.conf` is rendered deterministically as `key=value`, which is
  equivalent for Java properties and Spark.
- The event-log cleaner now works as declared; review retention before upgrade.
- Fresh v0.13 workloads honor the resolved S3 connection's TLS and region data.
  An adopted v0.12 workload keeps its historical omission of the computed
  region until that property is set explicitly through configOverrides, which
  avoids a framework-only rollout. System-WebPKI verification is supported;
  unsupported insecure/private-CA modes fail during reconciliation instead of
  being silently ignored.
- Without Vector logging, the unused legacy log `emptyDir` and file appender
  are omitted. With Vector enabled, the shared log volume remains 30Mi.
  Upgrade acceptance treats this as a narrow compatibility exception: only the
  FILE appender declaration and its root-logger reference may disappear from
  `log4j2.properties`. The internal `CONSOLE` identifiers may be renamed to the
  v0.13 `console`/`stdout`/`STDOUT` identifiers after reference resolution, but
  the console target remains `SYSTEM_ERR`, its threshold remains `INFO`, and
  its pattern, logger levels, every other config file, and the complete rollback
  ConfigMap must remain unchanged. An omitted target means Log4j2's default
  `SYSTEM_OUT` and is deliberately rejected as a behavior change. When Vector
  enables the FILE appender, its v0.12 default INFO threshold and 10MB rollover
  size are retained as well.
- Without OIDC, the client Service no longer exposes the unused port 4180.
- With OIDC, oauth2-proxy becomes a pinned restartable init container, reads a
  stable random cookie from a Secret, derives the issuer scheme from the
  AuthenticationClass TLS settings, and no longer enables unrestricted
  redirect domains by default. The unauthenticated Spark UI port is removed
  from the client Service, so oauth2-proxy cannot be bypassed through that
  Service. On an adopted v0.12 workload, a podOverrides container named `oidc`
  is migrated onto that native sidecar. A separate legacy regular container
  already named `oauth2-proxy` is ambiguous and is rejected with a validation
  error, even when an `oidc` override is also present; rename or migrate that
  custom container before upgrading. Fresh v0.13 workloads may patch the native
  `oauth2-proxy` name directly. A configured authentication block without oidc
  is rejected.
- The historical metrics mapping `18081 -> targetPort http` is retained only
  while adopting an unauthenticated v0.12 workload. Fresh v0.13 workloads and
  OIDC-enabled upgrades target the JMX `metrics` port, so Prometheus receives
  metrics and the secondary Service cannot bypass oauth2-proxy.

## Legacy Workload Health Compatibility

The v0.13 framework discovers failing Pods using
`app.kubernetes.io/managed-by=operator-go`, while an existing StatefulSet must
keep the legacy value in its immutable selector and Pod labels. A product
PostReconcile extension therefore checks only those legacy-labelled Pods and
applies the framework's same Unschedulable, CrashLoopBackOff, and image or
container failure reasons to the Degraded condition. This includes
`ErrImageNeverPull`, which kubelet reports when an unavailable image has
`imagePullPolicy: Never`; operator-go v0.13.0 does not yet include that reason
in its built-in health check. Fresh v0.13 workloads
continue to use the framework health path. This narrow duplication can be
removed when operator-go accepts an adopted workload identity and recognizes
`ErrImageNeverPull`; the desired upstream changes are recorded in
[operator-go v0.13 framework feedback](operator-go-framework-feedback.md).

## Rollback

1. Stop CR edits and roll back only the controller/chart.
2. Do not downgrade the CRD; retain the v0.13 superset schema.
3. Confirm the StatefulSet UID and legacy selector are unchanged and its Pods
   become ready under the old controller.
4. Recheck S3 event retention. Logs already removed after the cleaner became
   active cannot be restored by controller rollback.

The v0.13-created ServiceAccount and OIDC cookie Secret are ignored by the old
controller and can remain after rollback. Their owner reference removes them
when the SparkHistoryServer CR is eventually deleted. An OIDC rollback causes
one additional login-session reset.

## Automated Upgrade and Rollback Acceptance

The repository includes a destructive, long-running acceptance target for the
same-cluster migration contract:

```bash
make framework-upgrade-e2e PRODUCT_VERSION=3.5.5
```

The target refuses to reuse an existing cluster. Before any long-running work,
its small workspace bootstrap requires a clean candidate, copies the runner to
a new evidence directory, records its SHA-256, and `exec`s that frozen copy
exactly once. Frozen-runner mode is guarded by the bootstrap environment,
expected path, and hash, so it cannot recurse into another bootstrap. The
runner then checks out the pinned operator-go v0.12.6 baseline and candidate in
temporary detached worktrees and proves that its own bytes exactly match the
candidate HEAD before any image build or cluster mutation. It builds uniquely
tagged old and new controller images and freezes the worker, comparator, test
fixtures, v0.13 CRD, kustomize binary, operator dependency charts, and the
official Vector Helm chart at exactly `0.43.0` in the run evidence directory.
The existing logging-suite Vector values and aggregator discovery ConfigMap
are frozen and hashed with those inputs. All frozen-input hashes are checked
again immediately before the worker runs. The local Vector chart archive is
installed in the disposable Spark namespace and its StatefulSet must be ready
before the SparkHistoryServer fixture is created.

The run performs a controller-only v0.12 -> v0.13 -> v0.12 transition without
recreating the SparkHistoryServer StatefulSet. It explicitly applies the new
CRD before starting v0.13 and asserts:

- the StatefulSet UID, selector, `serviceName`, and `OrderedReady` policy stay
  unchanged;
- all three baseline Pods are covered by the role PDB; after removal of the
  disposable group, both surviving Pods remain healthy and covered through
  upgrade and rollback (`3 -> 2 -> 2`);
- the `default` group explicitly sets `logging.enableVectorAgent: false`.
  v0.12 demonstrably ignores that role-group switch while the cluster-level
  aggregator is configured; v0.13 removes only that group's Vector container,
  `vector.yaml`, 30Mi log volume, and FILE appender, and rollback restores the
  exact legacy state;
- a persistent `vector` group keeps Vector enabled. Its StatefulSet, ConfigMap,
  client Service, metrics Service, and Endpoints UIDs remain stable. v0.13
  moves its agent from a regular container to a restartable native init
  container while retaining the 30Mi shared log volume and the full delivery
  pipeline in `vector.yaml`. The only allowed pipeline addition is Vector's
  `internal_metrics` source and `prometheus_exporter` sink. The pinned v0.12.6
  renderer left Go-template escaping visible as doubled VRL delimiters
  (`{{`/`}}`, including `event = {{}}`), whereas v0.13 emits the equivalent
  single delimiters. Acceptance normalizes that syntax only inside VRL
  `source: |` blocks and only outside quoted strings. It also ignores redundant
  quotes around plain YAML scalars and spacing around the same VRL operator;
  quoted contents, VRL tokens, pipeline fields, addresses, and every other
  configuration value still compare strictly;
- in every phase, the worker appends a phase-unique, valid Log4j2 XML event to
  `upgrade-node-vector-0` and accepts success only after the aggregator emits a
  JSON event with that exact marker and the expected namespace, cluster, role,
  role group, container, file, level, and empty error list. Aggregator logs are
  fetched from a timestamp recorded before each write, so neither an earlier
  phase nor the default group's Spark application traffic can satisfy the
  check;
- every Vector-enabled phase keeps the FILE appender target at
  `/kubedoop/log/node/spark.log4j2.xml`, its threshold at `INFO`, and rollover
  size at `10MB`; the complete Vector ConfigMap and regular-container shape are
  restored on rollback;
- each client and metrics Service UID, ClusterIP, selector, Prometheus metadata,
  and expected ports remain compatible, and each client Endpoints object stays
  populated and targets the captured Pod;
- each surviving role group's generic-ephemeral S3 credential PVC remains
  Bound with a 1Mi request/capacity, the Secret storage class and Filesystem
  mode, and an owner UID matching the phase's Pod. PVC UIDs may change when
  their owning Pods roll; the removed role group's Pod-owned PVC must be
  reclaimed with that group;
- an intentionally unavailable `imagePullPolicy: Never` image reaches
  `Degraded=True` with reason `PodFailure`. After the evidence is captured, the
  worker first waits for the restored StatefulSet template, then deletes only
  the still-faulted Pod because OrderedReady StatefulSets cannot automatically
  roll back an already-unready ordinal;
- v0.13 records `status.roleGroups.node=["default","vector"]` after the
  removed group is reclaimed, and that ledger remains present after the v0.12
  controller rollback;
- Spark applications submitted before upgrade, after upgrade, and after
  rollback all remain visible through the History Server API.

Evidence is written below `target/framework-upgrade-evidence/run.*`, including
resource snapshots, application IDs, operator logs, compact contract diffs,
the bootstrap and complete frozen-input hashes (including the Vector chart and
logging fixtures), the rendered Vector release manifest, per-phase marker and
start-time records, the corresponding raw aggregator log windows, the exact
matched JSON events, and image/build metadata. The comparator's Vector
self-tests cover the real v0.12 doubled-brace form and v0.13 single-brace form,
while negative cases prove that braces in quoted data, braces outside VRL
source blocks, changed VRL conditions, changed aggregator addresses, and
incomplete rollback are rejected. The runner and delivery validators have
positive and deliberate-tamper/stale-marker negative self-tests; the worker
additionally proves that tampered resource evidence is rejected. The cluster
is retained for inspection after either success or failure. Delete only that
dedicated cluster when finished:

```bash
make cleanup-framework-upgrade-e2e
```
