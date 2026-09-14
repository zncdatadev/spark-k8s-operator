#!/usr/bin/env bash
# Freeze every input, then run the destructive framework upgrade acceptance test.
set -euo pipefail

verify_frozen_runner_hash() {
    local directory=$1
    (cd "$directory" && sha256sum --check --status bootstrap-runner.sha256)
}

self_test_frozen_runner_hash() {
    local test_base test_dir
    test_base=${TMPDIR:-/tmp}
    test_dir=$(mktemp -d "$test_base/spark-framework-runner-self-test.XXXXXX")
    cleanup_runner_self_test() {
        if [[ -d "$test_dir" && "$test_dir" == "$test_base"/spark-framework-runner-self-test.* ]]; then
            rm -rf -- "$test_dir"
        fi
    }
    trap cleanup_runner_self_test EXIT
    cp "$0" "$test_dir/run-framework-upgrade.sh"
    cp "$0" "$test_dir/candidate-run-framework-upgrade.sh"
    (
        cd "$test_dir"
        sha256sum run-framework-upgrade.sh > bootstrap-runner.sha256
    )
    verify_frozen_runner_hash "$test_dir"
    cmp -s \
        "$test_dir/run-framework-upgrade.sh" \
        "$test_dir/candidate-run-framework-upgrade.sh"
    printf '\n# deliberate self-test tamper\n' >> "$test_dir/run-framework-upgrade.sh"
    if verify_frozen_runner_hash "$test_dir"; then
        echo "Frozen-runner hash self-test accepted modified bytes." >&2
        return 1
    fi
    if cmp -s \
        "$test_dir/run-framework-upgrade.sh" \
        "$test_dir/candidate-run-framework-upgrade.sh"; then
        echo "Frozen-runner candidate comparison accepted modified bytes." >&2
        return 1
    fi
    printf '%s\n' "PASS: frozen-runner hash and candidate-byte guards"
    cleanup_runner_self_test
    trap - EXIT
}

if [[ "${1:-}" == --self-test ]]; then
    self_test_frozen_runner_hash
    exit 0
fi

if [[ "${UPGRADE_FROZEN_RUNNER:-false}" != true ]]; then
    bootstrap_root=$(cd "$(dirname "$0")/.." && pwd)
    for executable in cmp git mktemp readlink sha256sum; do
        command -v "$executable" >/dev/null 2>&1 || {
            echo "Required bootstrap command is not available: $executable" >&2
            exit 1
        }
    done
    if [[ -n "$(git -C "$bootstrap_root" status --porcelain --untracked-files=all)" ]]; then
        echo "Framework upgrade acceptance requires a clean committed candidate HEAD." >&2
        git -C "$bootstrap_root" status --short --untracked-files=all >&2
        exit 1
    fi
    bootstrap_evidence_base=${UPGRADE_EVIDENCE_DIR:-$bootstrap_root/target/framework-upgrade-evidence}
    mkdir -p "$bootstrap_evidence_base"
    bootstrap_evidence_base=$(cd "$bootstrap_evidence_base" && pwd)
    bootstrap_run_dir=$(mktemp -d "$bootstrap_evidence_base/run.XXXXXX")
    cp "$bootstrap_root/hack/run-framework-upgrade.sh" \
        "$bootstrap_run_dir/run-framework-upgrade.sh"
    chmod +x "$bootstrap_run_dir/run-framework-upgrade.sh"
    (
        cd "$bootstrap_run_dir"
        sha256sum run-framework-upgrade.sh > bootstrap-runner.sha256
    )
    exec env \
        UPGRADE_FROZEN_RUNNER=true \
        UPGRADE_SOURCE_ROOT="$bootstrap_root" \
        UPGRADE_RUN_DIR="$bootstrap_run_dir" \
        bash "$bootstrap_run_dir/run-framework-upgrade.sh"
fi

: "${UPGRADE_SOURCE_ROOT:?Frozen runner is missing the candidate source root}"
: "${UPGRADE_RUN_DIR:?Frozen runner is missing its evidence directory}"
root=$(cd "$UPGRADE_SOURCE_ROOT" && pwd)
run_dir=$(cd "$UPGRADE_RUN_DIR" && pwd)
runner_path=$(readlink -f "$0")
if [[ "$runner_path" != "$run_dir/run-framework-upgrade.sh" ]]; then
    echo "Refusing frozen-runner mode from unexpected script: $runner_path" >&2
    exit 1
fi
if ! verify_frozen_runner_hash "$run_dir"; then
    echo "Frozen runner failed its bootstrap hash check." >&2
    exit 1
fi

baseline_ref=${UPGRADE_BASELINE_REF:-42f080c1cb6466ce3441c47284994991e881b7c4}
cluster=${UPGRADE_CLUSTER:-framework-upgrade-spark-k8s-operator}
k8s_version=${UPGRADE_K8S_VERSION:-1.35.0}
product_version=${PRODUCT_VERSION:-3.5.5}
evidence_base=${UPGRADE_EVIDENCE_DIR:-$root/target/framework-upgrade-evidence}
dependency_chart_version=${DEPENDENCY_CHART_VERSION:-0.0.0-dev}
operator_depends=${OPERATOR_DEPENDS:-commons-operator listener-operator secret-operator}
vector_chart_version=0.43.0
vector_chart_repo=https://helm.vector.dev
kind=${KIND:-kind}
helm=${HELM:-helm}
container_tool=${CONTAINER_TOOL:-docker}
kustomize=${KUSTOMIZE:-$root/bin/kustomize}

require_command() {
    local executable=$1
    if [[ "$executable" == */* ]]; then
        [[ -x "$executable" ]] || {
            echo "Required executable is not available: $executable" >&2
            exit 1
        }
    else
        command -v "$executable" >/dev/null 2>&1 || {
            echo "Required command is not available: $executable" >&2
            exit 1
        }
    fi
}

module_version_at_ref() {
    local ref=$1
    git -C "$root" show "${ref}:go.mod" |
        awk '$1 == "github.com/zncdatadev/operator-go" && !found { version = $2; found = 1 } END { print version }'
}

has_operator_go_replace_at_ref() {
    local ref=$1
    git -C "$root" show "${ref}:go.mod" | awk '
        $1 == "replace" && $2 == "(" { in_replace = 1; next }
        in_replace && $1 == ")" { in_replace = 0; next }
        ($1 == "replace" && $2 == "github.com/zncdatadev/operator-go") ||
        (in_replace && $1 == "github.com/zncdatadev/operator-go") { found = 1 }
        END { exit found ? 0 : 1 }
    '
}

for executable in cmp git make "$kind" "$helm" "$container_tool" kubectl python3 sha256sum; do
    require_command "$executable"
done
require_command "$kustomize"

if [[ ! "$cluster" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || (( ${#cluster} > 63 )); then
    echo "UPGRADE_CLUSTER must be a valid DNS label no longer than 63 characters: $cluster" >&2
    exit 1
fi
if "$kind" get clusters | grep -Fxq "$cluster"; then
    echo "Refusing to reuse kind cluster '$cluster'; inspect or delete the previous run first." >&2
    exit 1
fi
if [[ -n "$(git -C "$root" status --porcelain --untracked-files=all)" ]]; then
    echo "Framework upgrade acceptance requires a clean committed candidate HEAD." >&2
    git -C "$root" status --short --untracked-files=all >&2
    exit 1
fi

current_head=$(git -C "$root" rev-parse --verify HEAD)
current_short=$(git -C "$root" rev-parse --short=12 HEAD)
candidate_operator_go=$(module_version_at_ref "$current_head")
if [[ "$candidate_operator_go" != "v0.13.0" ]]; then
    echo "Candidate HEAD must use operator-go v0.13.0, got '$candidate_operator_go' at $current_head." >&2
    exit 1
fi
if has_operator_go_replace_at_ref "$current_head"; then
    echo "Candidate HEAD must not replace github.com/zncdatadev/operator-go in go.mod." >&2
    exit 1
fi

baseline_commit=$(git -C "$root" rev-parse --verify "${baseline_ref}^{commit}")
baseline_operator_go=$(module_version_at_ref "$baseline_commit")
if [[ "$baseline_operator_go" != "v0.12.6" ]]; then
    echo "Pinned baseline must use operator-go v0.12.6, got '$baseline_operator_go' at $baseline_commit." >&2
    exit 1
fi

for dependency in $operator_depends; do
    if [[ ! "$dependency" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]]; then
        echo "Invalid dependency chart name: $dependency" >&2
        exit 1
    fi
done

mkdir -p "$evidence_base"
evidence_base=$(cd "$evidence_base" && pwd)
case "$run_dir" in
    "$evidence_base"/run.*) ;;
    *)
        echo "Frozen run directory is outside the configured evidence directory: $run_dir" >&2
        exit 1
        ;;
esac
fixture_dir="$run_dir/fixtures"
tool_dir="$run_dir/tools"
chart_dir="$run_dir/dependency-charts"
mkdir -p "$fixture_dir" "$tool_dir" "$chart_dir"
kubeconfig="$run_dir/kubeconfig"

temp_parent=$(mktemp -d "${TMPDIR:-/tmp}/spark-framework-upgrade-worktree.XXXXXX")
baseline_dir="$temp_parent/baseline"
candidate_dir="$temp_parent/candidate"
baseline_worktree_added=false
candidate_worktree_added=false
cleanup_local() {
    if [[ "$candidate_worktree_added" == true ]]; then
        git -C "$root" worktree remove --force "$candidate_dir" >/dev/null 2>&1 || true
    fi
    if [[ "$baseline_worktree_added" == true ]]; then
        git -C "$root" worktree remove --force "$baseline_dir" >/dev/null 2>&1 || true
    fi
    if [[ -d "$temp_parent" && "$temp_parent" == "${TMPDIR:-/tmp}"/spark-framework-upgrade-worktree.* ]]; then
        rmdir "$temp_parent" >/dev/null 2>&1 || true
    fi
}
trap cleanup_local EXIT

git -C "$root" worktree add --detach "$baseline_dir" "$baseline_commit"
baseline_worktree_added=true
git -C "$root" worktree add --detach "$candidate_dir" "$current_head"
candidate_worktree_added=true
test "$(git -C "$baseline_dir" rev-parse HEAD)" = "$baseline_commit"
test "$(git -C "$candidate_dir" rev-parse HEAD)" = "$current_head"
test -z "$(git -C "$baseline_dir" status --porcelain)"
test -z "$(git -C "$candidate_dir" status --porcelain)"

# The bootstrap has already exec'd the frozen runner. Prove those executing
# bytes are exactly the candidate HEAD before freezing the remaining inputs.
if ! cmp -s \
    "$candidate_dir/hack/run-framework-upgrade.sh" \
    "$run_dir/run-framework-upgrade.sh"; then
    echo "Executed frozen runner differs from the committed candidate HEAD." >&2
    exit 1
fi

# The worker, comparator, CRD, fixtures, and rendering binary are copied from the
# detached candidate before any image build or cluster mutation. Only these
# copies and the two detached worktrees are consumed by the acceptance run.
cp "$candidate_dir/hack/test-framework-upgrade.sh" "$run_dir/test-framework-upgrade.sh"
cp "$candidate_dir/hack/compare-framework-upgrade.py" "$run_dir/compare-framework-upgrade.py"
cp "$candidate_dir/test/e2e/setup/minio.yaml" "$fixture_dir/minio.yaml"
cp "$candidate_dir/test/e2e/setup/minio-s3-connection.yaml" "$fixture_dir/minio-s3-connection.yaml"
cp "$candidate_dir/test/e2e/logging/vector-aggregator.yaml" "$fixture_dir/vector-aggregator.yaml"
cp "$candidate_dir/test/e2e/logging/vector-aggregator-values.yaml" "$fixture_dir/vector-aggregator-values.yaml"
cp "$candidate_dir/deploy/helm/spark-k8s-operator/crds/crds.yaml" "$fixture_dir/framework-crds.yaml"
cp "$kustomize" "$tool_dir/kustomize"
chmod +x \
    "$run_dir/run-framework-upgrade.sh" \
    "$run_dir/test-framework-upgrade.sh" \
    "$run_dir/compare-framework-upgrade.py" \
    "$tool_dir/kustomize"
frozen_kustomize="$tool_dir/kustomize"

# Resolve mutable OCI coordinates to local immutable bytes before touching the
# cluster. Their hashes become part of the retained evidence.
for dependency in $operator_depends; do
    "$helm" pull "oci://quay.io/kubedoopcharts/$dependency" \
        --version "$dependency_chart_version" \
        --destination "$chart_dir" \
        2>&1 | tee "$run_dir/$dependency-pull.log"
    test -f "$chart_dir/$dependency-$dependency_chart_version.tgz"
done
"$helm" pull vector \
    --repo "$vector_chart_repo" \
    --version "$vector_chart_version" \
    --destination "$chart_dir" \
    2>&1 | tee "$run_dir/vector-chart-pull.log"
vector_chart="$chart_dir/vector-$vector_chart_version.tgz"
test -f "$vector_chart"

frozen_files=(
    run-framework-upgrade.sh
    test-framework-upgrade.sh
    compare-framework-upgrade.py
    fixtures/minio.yaml
    fixtures/minio-s3-connection.yaml
    fixtures/vector-aggregator.yaml
    fixtures/vector-aggregator-values.yaml
    fixtures/framework-crds.yaml
    tools/kustomize
)
for dependency in $operator_depends; do
    frozen_files+=("dependency-charts/$dependency-$dependency_chart_version.tgz")
done
frozen_files+=("dependency-charts/vector-$vector_chart_version.tgz")
(
    cd "$run_dir"
    sha256sum "${frozen_files[@]}" > frozen-inputs.sha256
)

run_token=$(date -u +%Y%m%dT%H%M%SZ)-$$
baseline_image=${UPGRADE_BASELINE_IMAGE:-spark-k8s-operator:framework-upgrade-v012-${baseline_commit:0:12}-$run_token}
framework_image=${UPGRADE_FRAMEWORK_IMAGE:-spark-k8s-operator:framework-upgrade-v013-${current_short}-$run_token}
kind_image="kindest/node:v${k8s_version#v}"

{
    printf 'started_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf 'baseline_ref=%s\n' "$baseline_ref"
    printf 'baseline_commit=%s\n' "$baseline_commit"
    printf 'baseline_operator_go=%s\n' "$baseline_operator_go"
    printf 'candidate_head=%s\n' "$current_head"
    printf 'candidate_operator_go=%s\n' "$candidate_operator_go"
    printf 'candidate_operator_go_replace=false\n'
    printf 'cluster=%s\n' "$cluster"
    printf 'kind_image=%s\n' "$kind_image"
    printf 'kubernetes_version=%s\n' "$k8s_version"
    printf 'product_version=%s\n' "$product_version"
    printf 'dependency_chart_version=%s\n' "$dependency_chart_version"
    printf 'operator_dependencies=%s\n' "$operator_depends"
    printf 'vector_chart_version=%s\n' "$vector_chart_version"
    printf 'vector_chart_repo=%s\n' "$vector_chart_repo"
    printf 'baseline_image=%s\n' "$baseline_image"
    printf 'framework_image=%s\n' "$framework_image"
} > "$run_dir/run-metadata.env"
git -C "$candidate_dir" status --short --untracked-files=all > "$run_dir/candidate-worktree-status.txt"
git -C "$candidate_dir" show --no-ext-diff --stat --oneline HEAD > "$run_dir/candidate-commit.txt"

{
    git --version
    make --version | sed -n '1p'
    "$kind" version
    "$helm" version --short
    "$container_tool" --version
    kubectl version --client=true
    python3 --version
    "$frozen_kustomize" version
    sha256sum --version | sed -n '1p'
} > "$run_dir/tool-versions.txt" 2>&1

echo "Building pinned v0.12 baseline image: $baseline_image"
make -C "$baseline_dir" docker-build \
    IMG="$baseline_image" CONTAINER_TOOL="$container_tool" \
    2>&1 | tee "$run_dir/baseline-image-build.log"

echo "Building frozen v0.13 candidate image: $framework_image"
make -C "$candidate_dir" docker-build \
    IMG="$framework_image" CONTAINER_TOOL="$container_tool" \
    2>&1 | tee "$run_dir/framework-image-build.log"

test "$(git -C "$baseline_dir" rev-parse HEAD)" = "$baseline_commit"
test "$(git -C "$candidate_dir" rev-parse HEAD)" = "$current_head"
git -C "$baseline_dir" status --short --untracked-files=all > "$run_dir/baseline-post-build-status.txt"
git -C "$candidate_dir" status --short --untracked-files=all > "$run_dir/candidate-post-build-status.txt"
test ! -s "$run_dir/baseline-post-build-status.txt"
test ! -s "$run_dir/candidate-post-build-status.txt"

baseline_image_id=$("$container_tool" image inspect --format '{{.Id}}' "$baseline_image")
framework_image_id=$("$container_tool" image inspect --format '{{.Id}}' "$framework_image")
{
    printf 'baseline_image_id=%s\n' "$baseline_image_id"
    printf 'framework_image_id=%s\n' "$framework_image_id"
} | tee -a "$run_dir/run-metadata.env" > "$run_dir/image-ids.env"

echo "Creating dedicated kind cluster: $cluster"
"$kind" create cluster \
    --name "$cluster" \
    --image "$kind_image" \
    --kubeconfig "$kubeconfig" \
    2>&1 | tee "$run_dir/cluster-create.log"

for dependency in $operator_depends; do
    echo "Installing frozen dependency chart: $dependency"
    "$helm" upgrade --install \
        --create-namespace \
        --namespace kubedoop-operators \
        --kubeconfig "$kubeconfig" \
        --wait \
        "$dependency" "$chart_dir/$dependency-$dependency_chart_version.tgz" \
        2>&1 | tee "$run_dir/$dependency-install.log"
    "$helm" get manifest "$dependency" \
        --namespace kubedoop-operators \
        --kubeconfig "$kubeconfig" \
        > "$run_dir/$dependency-manifest.yaml"
done
"$helm" list --all-namespaces --output json --kubeconfig "$kubeconfig" \
    > "$run_dir/helm-list.json"

(
    cd "$run_dir"
    sha256sum --check frozen-inputs.sha256
) > "$run_dir/frozen-inputs-check.txt"

KUBECONFIG="$kubeconfig" \
CHAINSAW_CLUSTER="$cluster" \
UPGRADE_CANDIDATE_DIR="$candidate_dir" \
UPGRADE_BASELINE_DIR="$baseline_dir" \
UPGRADE_BASELINE_IMAGE="$baseline_image" \
UPGRADE_FRAMEWORK_IMAGE="$framework_image" \
UPGRADE_EVIDENCE_DIR="$run_dir" \
UPGRADE_FIXTURE_DIR="$fixture_dir" \
UPGRADE_COMPARE_SCRIPT="$run_dir/compare-framework-upgrade.py" \
UPGRADE_VECTOR_CHART="$vector_chart" \
KIND="$kind" HELM="$helm" KUSTOMIZE="$frozen_kustomize" PRODUCT_VERSION="$product_version" \
    bash "$run_dir/test-framework-upgrade.sh"

printf 'completed_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >> "$run_dir/run-metadata.env"
echo "Evidence: $run_dir"
echo "The disposable cluster is retained for inspection."
echo "Cleanup: make cleanup-framework-upgrade-e2e UPGRADE_CLUSTER=$cluster"
