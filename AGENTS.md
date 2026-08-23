<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-04-06 | Updated: 2026-04-06 -->

# spark-k8s-operator

## Purpose
Kubernetes operator for managing Apache Spark History Server deployments. Handles creation, configuration, and lifecycle management of Spark History Server instances, including S3-backed event log storage, OIDC authentication, and Vector log aggregation integration.

## Key Files
| File | Description |
|------|-------------|
| `go.mod` | Go module dependencies (operator-go, controller-runtime, k8s.io/*) |
| `Makefile` | Build, generate, test, and deploy commands |
| `PROJECT` | Kubebuilder project metadata (domain: kubedoop.dev) |
| `Dockerfile` | Container image definition for the operator |

## Subdirectories
| Directory | Purpose |
|-----------|---------|
| `api/v1alpha1/` | CRD type definitions for `SparkHistoryServer` |
| `cmd/` | Operator entry point (`main.go`) |
| `config/` | Kubernetes manifests and kustomize configs (CRDs, RBAC, manager) |
| `internal/controller/historyserver/` | Reconciliation logic for SparkHistoryServer |
| `internal/util/` | Shared utilities |
| `deploy/` | Example deployment manifests |
| `grafana/` | Grafana dashboard definitions |
| `test/` | E2E test suites |

## API / CRDs

### SparkHistoryServer (`spark.kubedoop.dev/v1alpha1`)
The sole CRD managed by this operator.

| Field | Type | Description |
|-------|------|-------------|
| `spec.image` | `ImageSpec` | Container image config (repo, version, pullPolicy) |
| `spec.clusterConfig` | `ClusterConfigSpec` | Cluster-level config: log directory, auth, listener class, Vector |
| `spec.clusterConfig.logFileDirectory.s3` | `S3Spec` | S3 bucket + prefix for Spark event logs |
| `spec.clusterConfig.authentication` | `AuthenticationSpec` | Optional OIDC authentication class |
| `spec.clusterOperation` | `ClusterOperationSpec` | Cluster lifecycle operations (from operator-go commons) |
| `spec.node` | `RoleSpec` | The single `node` role; contains `roleGroups` map |

**Role/RoleGroup model:** `SparkHistoryServer` uses a single role (`node`) with multiple role groups (`spec.node.roleGroups`). Each role group maps to a StatefulSet.

## Internal Controller Structure

`internal/controller/historyserver/` contains:

| File | Purpose |
|------|---------|
| `controller.go` | `SparkHistoryServerReconciler` - main controller, registers with manager |
| `cluster.go` | Cluster-level reconciliation orchestration |
| `node.go` | Role (`node`) reconciliation, delegates to role groups |
| `statefulset.go` | StatefulSet generation for history server pods |
| `configmap.go` | ConfigMap generation (spark-defaults.conf, log4j2, etc.) |
| `service.go` | Service generation for the history server UI |
| `s3.go` | S3 bucket/credential resolution (inline or referenced) |
| `constants.go` | Shared constants |

## For AI Agents

### Working In This Directory
- Standard Kubebuilder operator structure (cliVersion 4.x, layout go.kubebuilder.io/v4)
- Uses `operator-go` GenericReconciler framework for reconciliation
- Go module: `github.com/zncdatadev/spark-k8s-operator`
- Run `make generate` after modifying API types to regenerate DeepCopy methods
- Run `make manifests` to regenerate CRDs and RBAC after API changes
- Run `make fmt && make vet` before committing
- Run `make test` for unit tests

### Testing Requirements
- E2E tests in `test/e2e/`
- Requires a running Kubernetes cluster for E2E tests
- Unit tests: `make test`

### Common Patterns
- Controllers follow operator-go `GenericReconciler` pattern
- All CRDs use `v1alpha1` API version
- S3 credentials can be inline or referenced via `S3BucketSpec` from operator-go
- OIDC support via `AuthenticationClass` reference
- Vector log aggregation via `vectorAggregatorConfigMapName`
- `listenerClass` controls service exposure (cluster-internal / external-unstable / external-stable)

## Dependencies

### Internal
- `../operator-go` - Shared operator framework (`github.com/zncdatadev/operator-go v0.12.x`)

### External
- `sigs.k8s.io/controller-runtime` - Kubernetes controller framework
- `k8s.io/client-go`, `k8s.io/api`, `k8s.io/apimachinery` - Kubernetes client libraries
- Go 1.25+

### AI Worktree Development Mode

**IMPORTANT**: When making code changes, work in a worktree under `.worktree/`, NOT in the main working directory.

#### Workflow
1. Create worktree: `git worktree add .worktree/<branch-name> -b <branch-name>`
2. Work in `.worktree/<branch-name>/` directory
3. Test: `cd .worktree/<branch-name> && make lint && make test`
4. Commit changes in the worktree
5. Push and create PR from the worktree branch
6. Cleanup: `git worktree remove .worktree/<branch-name>`

#### Rules
- NEVER modify files directly in the main working directory
- Each task gets its own worktree with a descriptive branch name
- Run `make generate` if API structs are modified
- Run `make lint && make test` before committing

<!-- MANUAL: Any manually added notes below this line are preserved on regeneration -->
