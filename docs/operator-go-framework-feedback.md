# operator-go v0.13 Framework Feedback

This document captures framework defects exposed by the SparkHistoryServer
Gen 2 to Gen 3 migration. These are upstream issue drafts, not yet filed
issues. Spark carries narrow compatibility workarounds until operator-go offers
the corresponding migration APIs.

## Recognize ErrImageNeverPull as a Pod Failure

Kubelet reports `ErrImageNeverPull` when a container image is absent locally
and `imagePullPolicy` is `Never`. operator-go v0.13.0's stuck-container reason
set in `pkg/reconciler/health.go` does not include this reason, so a Pod can be
permanently unready while the CR retains `Degraded=False` with the message
`No failing pods`.

Add `ErrImageNeverPull` to the framework's stuck-container reasons and cover it
in the health tests. Spark includes the same reason in its legacy-labelled Pod
compatibility extension, but fresh framework-labelled workloads still depend
on the upstream fix.

## Provider Defaults During Workload Adoption

The S3 credentials convenience provider fixes Secret mounts to read-only and
uses the framework-wide 10Mi generic-ephemeral PVC request. Those are good
defaults for fresh workloads, but a framework-only migration may need to keep
the product's earlier mount mode and storage request until users opt into a
rollout. A storage quota can turn even a small request increase into an upgrade
outage after the old Pod and its Pod-owned PVC are removed.

Spark currently decorates only its S3 provider while adopting a legacy
StatefulSet, restoring the v0.12 false mount flag and 1Mi request before normal
podOverrides are merged. The S3 helper should accept provider options for
storage size and mount mode, or the framework should expose an adoption-policy
hook that can supply legacy defaults below user overrides. Fresh workloads must
continue to receive the current framework defaults.

The same adoption-policy seam would help with product configuration computed
from framework resolvers. For example, Spark must suppress the newly resolved
S3 region only while adopting a v0.12 workload, without suppressing a user's
explicit configOverride or the v0.13 default for a fresh workload.

## Migration-Safe Workload Identity and Health Discovery

### Workload Identity Motivation

An upgraded operator may need to retain an existing StatefulSet's immutable
selector and the Pod labels required by it. operator-go v0.13 preserves live
immutable fields, but it generates desired Pod, Service, and PDB selectors from
the new framework identity.

Health discovery independently assumes
`app.kubernetes.io/managed-by=operator-go`. Spark must keep
`managed-by=spark.kubedoop.dev` in the legacy selector and Pod labels, so the
framework misses pod-level failures such as CrashLoopBackOff and
ImagePullBackOff in its Degraded signal. Spark currently supplements that
framework path with a narrow PostReconcile check for legacy-labelled Pods.

### Workload Identity Evidence

- Spark rewrites StatefulSet and Pod identity, client and metrics Service
  selectors, and the role PDB selector in
  `internal/controller/historyserver/handler.go`.
- Spark keeps object metadata managed by `operator-go`, while the immutable
  Pod identity remains managed by `spark.kubedoop.dev`.
- operator-go v0.13 `pkg/reconciler/health.go` hard-codes the new managed-by
  value when listing failing Pods.
- Kafka handles the same identity transition by replacing workloads during a
  maintenance window; Hive moves from legacy product labels to the base v0.13
  handler without an adoption API.

### Workload Identity API

```go
type WorkloadIdentityPolicy interface {
    RoleSelector(clusterName, roleName string) labels.Set
    RoleGroupSelector(clusterName, roleName, roleGroupName string) labels.Set
}

type GenericReconcilerConfig[CR client.Object] struct {
    WorkloadIdentity WorkloadIdentityPolicy
}
```

Resolve identity once and use it for the StatefulSet selector and required Pod
labels, all Services, the role PDB, product-built selectors, and health Pod
discovery. Provide an opt-in legacy adoption policy. Whenever the apply layer
preserves a live selector, it must also ensure every required label is present
with the same value in the desired Pod template.

### Workload Identity Migration Impact

Keep today's v0.13 identity as the default. With opt-in adoption, Spark can
remove its selector and PDB post-processing as well as the duplicated legacy
Pod health extension, while retaining existing workload identity and complete
health reporting. No CRD change is required.

## Migration-Aware Role-Group Resource Names

### Resource Name Motivation

operator-go v0.13 hashes role-group base names longer than 54 characters so a
generated `-headless` Service still fits the DNS label limit. v0.12 used the
natural `<cluster>-<role>-<group>` name. During upgrade this can target a new
hashed slot while the natural-name resources remain, silently creating two
workloads for one role group.

Spark does not retain the generated headless Service, so a 55-character base
name could be valid with its legacy topology.

### Resource Name Evidence

- operator-go v0.13 `pkg/reconciler/generic_reconciler.go` owns the 54-character
  hash rule; v0.12 `pkg/reconciler/info.go` used the natural name.
- Spark's `validateCompatibleResourceName` checks owned natural-name
  StatefulSets, Services, and ConfigMaps and aborts instead of duplicating an
  existing workload. New long-name clusters continue to use the v0.13 hash.
- The framework recomputes names in validation, health, status, and cleanup,
  so changing only objects returned by a downstream handler is incomplete.

### Resource Name API

```go
type RoleGroupNameResolution struct {
    CanonicalName string
    EffectiveName string
    Source        NameSource
}

type RoleGroupNameResolver[CR client.Object] interface {
    Resolve(
        ctx context.Context,
        reader client.Reader,
        cr CR,
        roleName string,
        roleGroupName string,
    ) (RoleGroupNameResolution, error)
}
```

Offer the current canonical resolver and an `AdoptOwnedLegacyNaturalName`
resolver. The adoption resolver must verify controller owner UID and GVK. The
effective result must be threaded through build contexts, fixed-slot
validation, health, the status ledger, cleanup, and reclaim logic.

### Resource Name Migration Impact

Default output remains unchanged. An opt-in resolver lets upgraded operators
adopt natural-name resources while fresh installs use canonical hashed names.
The framework should report adopted legacy names through an event or condition.

## Explicit Memory Request Policy

### Memory Policy Motivation

In operator-go v0.12, `MemoryResource.Limit` produced a StatefulSet Pod
template with a memory limit and no explicit request. v0.13 copies the limit
into the request. This changes the persisted template and may cause an
unnecessary rollout. It can also change admitted Pod resources where a
LimitRange or another admission component previously supplied a different
request.

### Memory Policy Evidence

- Spark's `applyLegacyMemoryRequest` restores limit-only output while retaining
  an explicit request from `podOverrides`.
- Hive inherited the same v0.12 limit-only builder behavior and therefore has
  the same migration delta.
- Kafka's legacy custom builder already used request equal to limit, so neither
  behavior is a safe universal compatibility default.

### Memory Policy API

```go
type MemoryResource struct {
    Limit   *resource.Quantity `json:"limit,omitempty"`
    Request *resource.Quantity `json:"request,omitempty"`
}

type MemoryRequestPolicy string

const (
    MemoryRequestFromLimit    MemoryRequestPolicy = "FromLimit"
    MemoryRequestExplicitOnly MemoryRequestPolicy = "ExplicitOnly"
)
```

Expose the policy through a role declaration or reconciler configuration. An
explicit `memory.request` and final `podOverrides` must retain precedence. Keep
`FromLimit` as the v0.13 default; Spark and Hive can select `ExplicitOnly`,
while Kafka stays on `FromLimit`.

### Memory Policy Migration Impact

The policy can be added without changing existing output. Adding
`memory.request` to the commons CRD requires pointer semantics without a
structural default, generated code and manifests, examples, and the operator-go
documentation changelog.

## Adopt Existing Workload ServiceAccounts

### ServiceAccount Motivation

The legacy Spark StatefulSet did not choose a ServiceAccount explicitly and
therefore ran as `default`. GenericReconciler creates and binds a derived
per-CR ServiceAccount. Changing this field during a framework-only migration
can drop imagePullSecrets, RBAC bindings, or admission policy associated with
the live account and can leave the replacement Pod unable to start.

### ServiceAccount Evidence

- Spark's `preserveExistingServiceAccount` adopts the account from an owned
  live StatefulSet when the current `podOverrides` does not explicitly select
  another account.
- Every operator migrating to the v0.13 GenericReconciler receives the new
  per-CR identity, so this is not Spark-specific.
- Creating the per-CR ServiceAccount is harmless by itself; changing the Pod
  identity is the compatibility boundary.

### ServiceAccount API

```go
type WorkloadServiceAccountPolicy string

const (
    PerClusterServiceAccount WorkloadServiceAccountPolicy = "PerCluster"
    AdoptLiveServiceAccount WorkloadServiceAccountPolicy = "AdoptLive"
)
```

An adoption policy should read the ServiceAccount from an owned live workload
and use it as the base value. A current explicit pod override must still win.
The resolved value belongs in `RoleGroupBuildContext` so builders, validation,
status, and events report one answer. The framework should emit a migration
event when it adopts an account different from the derived per-CR account.

### ServiceAccount Migration Impact

Keep per-CR ServiceAccounts as the default for fresh v0.13 workloads. Opt-in
adoption lets a migrated operator preserve security identity, then move to the
per-CR account in a separate, documented rollout after copying the necessary
imagePullSecrets and permissions.

## Adopt and Reclaim Legacy Fixed Slots

### Fixed-Slot Motivation

The v0.13 role PDB cleaner intentionally deletes only objects carrying its
pdb.kubedoop.dev/role slot label. A v0.12 role PDB has the same canonical name
and controller owner but no slot label. If a CR disables the PDB before its
first v0.13 adoption pass, the generic cleaner leaves the legacy PDB in place
forever, where it can continue constraining eviction.

Spark carries a narrow cluster pre-reconcile extension that deletes only the
exact role name with the matching owner UID and complete v0.12 label
fingerprint. A framework migration API should instead support safe legacy-slot
fingerprints as part of reclaim, just as the role-group PDB cleaner already
recognizes an earlier framework fingerprint.

The same blind spot affects role groups removed from the spec before the first
v0.13 pass: their v0.12 StatefulSet and ConfigMap still carry the legacy
managed-by value, so live orphan discovery cannot see them and no new status
ledger entry exists. Spark reconstructs that ledger at PreReconcile from exact
owned natural-name StatefulSet, ConfigMap, client Service, and metrics Service
fingerprints, without mutating them, so every intermediate cleanup state is
restart-safe. Long names that resolve to a different v0.13 slot fail closed.

### Fixed-Slot API Direction

A fixed slot should accept ordered adoption fingerprints:

    type LegacySlotFingerprint func(client.Object) bool

    type FixedSlotPolicy struct {
        CurrentLabel string
        Legacy       []LegacySlotFingerprint
    }

The framework must still require the expected owner UID and derived resource
name before adopting or deleting a legacy match.

## Migrate Regular Containers to Native Sidecars

### Sidecar Migration Motivation

Moving a built-in sidecar from PodSpec.containers to restartable
PodSpec.initContainers changes the strategic-merge target. Existing CRs can
carry full Container overrides: command, args, env valueFrom, lifecycle,
volumeMounts, ports, security context, and partial probes. SidecarConfig
represents only a subset and several providers use different merge rules, so a
field-by-field downstream adapter either drops valid settings or produces an
invalid Pod.

Spark currently removes recognized legacy sidecar entries before the regular
container merge and applies the full Container strategic patch to the injected
native sidecar afterwards. It also translates the Vector config mount and
restores the v0.12 readiness-probe baseline for partial overrides.

There is a second, fresh-cluster failure at this boundary. The framework's
default sidecar security context sets `runAsNonRoot: true` but deliberately
relies on a pod-level numeric identity. A product preserving a legacy root main
container can validly configure no pod security context at all. Vector then
runs the product image whose OCI metadata says `USER kubedoop`; kubelet cannot
prove that a named user is non-root and leaves the Pod in
`CreateContainerConfigError`. Spark fills uid/gid 1001 on the native Vector
container unless a usable non-root pod/container identity already exists,
before replaying the user's raw container override.

An explicit pod-level uid 0 exposes the same assumption for every native
sidecar because container-level `runAsNonRoot: true` wins over the inherited
root uid. Spark isolates Vector with uid 1001 and the pinned oauth2-proxy v7.8.2
image with its actual OCI uid 65532 in that case. The framework's sidecar
security comment currently describes oauth2-proxy as uid 2000, so image
identity should come from provider-owned metadata rather than documentation or
one global sidecar default.

### Sidecar Migration API Direction

The framework should offer a named container migration map and apply existing
raw PodTemplate patches at the final native-sidecar boundary:

    type SidecarMigration struct {
        LegacyContainerName string
        NativeSidecarName   string
        NormalizePatch      func(*corev1.Container) error
    }

This keeps raw field presence and Kubernetes strategic-merge behavior inside
the framework, before typed decoding loses the distinction between omitted and
explicit zero values.

The built-in sidecar path also needs product defaults below `podOverrides`, for
example a `RoleDeclaration.SidecarDefaults` map keyed by provider name. Its
security context should be merged before the PodTemplate patch and before any
legacy-container migration patch. That lets a product-image provider declare a
known numeric image identity without overriding either a pod-level user choice
or a final container-specific override.

## Product Logging Migration Defaults

### Logging Default Motivation

The v0.12 Log4j2 generator always sent console events to `SYSTEM_ERR`, applied
INFO thresholds to the console and FILE appenders, and rolled FILE output at
10MB. The v0.13 renderer defaults to `SYSTEM_OUT`, omits appender thresholds,
and uses a 5MB FILE rollover. Those defaults are reasonable for a new product,
but changing them during a framework-only upgrade can alter stream-aware log
collection, DEBUG filtering, and retained log volume.

Spark currently supplies INFO through `ConfigDefaults` and narrowly patches
only framework-generated Log4j2 content to retain `SYSTEM_ERR` and the 10MB
rollover. A user-provided `log4j2.properties` is never patched.

### Logging Default API Direction

`ContainerLogging` should expose product defaults for the console target and
rolling-file bounds alongside its existing pattern and file-name fields. The
renderer can keep its current defaults when these fields are unset:

    type ContainerLogging struct {
        ConsoleTarget string
        MaxFileSize   string
        MaxHistory    int
    }

This would remove downstream text post-processing while retaining normal
role/role-group logging-level precedence.
