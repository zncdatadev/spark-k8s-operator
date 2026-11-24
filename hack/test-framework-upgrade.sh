#!/usr/bin/env bash
# Destructive acceptance test confined to a fresh, explicitly selected kind cluster.
set -euo pipefail

python_cmd=(env -u PYTHONOPTIMIZE python3 -I)

validate_vector_delivery_event() {
    local marker=$1
    local expected_namespace=$2
    local expected_cluster=$3
    "${python_cmd[@]}" -c '
import json
import sys

marker, namespace, cluster = sys.argv[1:]
expected = {
    "message": marker,
    "namespace": namespace,
    "cluster": cluster,
    "role": "node",
    "roleGroup": "vector",
    "container": "node",
    "file": "spark.log4j2.xml",
    "level": "INFO",
    "errors": [],
}
matches = []
for raw_line in sys.stdin:
    try:
        event = json.loads(raw_line)
    except json.JSONDecodeError:
        continue
    if all(event.get(key) == value for key, value in expected.items()):
        matches.append(event)
if not matches:
    raise SystemExit(1)
print(json.dumps(matches[-1], sort_keys=True))
' "$marker" "$expected_namespace" "$expected_cluster"
}

self_test_vector_delivery_validator() {
    local valid_event
    valid_event='{"message":"phase-marker","namespace":"spark-framework-upgrade","cluster":"upgrade","role":"node","roleGroup":"vector","container":"node","file":"spark.log4j2.xml","level":"INFO","errors":[]}'
    printf 'non-json startup noise\n%s\n' "$valid_event" |
        validate_vector_delivery_event \
            phase-marker spark-framework-upgrade upgrade >/dev/null
    if printf '%s\n' "${valid_event/phase-marker/old-phase-marker}" |
        validate_vector_delivery_event \
            phase-marker spark-framework-upgrade upgrade >/dev/null 2>&1; then
        echo "Vector delivery validator accepted an old marker." >&2
        return 1
    fi
    if printf '%s\n' "${valid_event/\"roleGroup\":\"vector\"/\"roleGroup\":\"default\"}" |
        validate_vector_delivery_event \
            phase-marker spark-framework-upgrade upgrade >/dev/null 2>&1; then
        echo "Vector delivery validator accepted the wrong role group." >&2
        return 1
    fi
    printf '%s\n' "PASS: Vector delivery validator positive and negative self-tests"
}

if [[ "${1:-}" == --self-test ]]; then
    self_test_vector_delivery_validator
    exit 0
fi

: "${KUBECONFIG:?Set KUBECONFIG to the disposable kind cluster}"
: "${CHAINSAW_CLUSTER:?Set the disposable kind cluster name}"
: "${UPGRADE_CANDIDATE_DIR:?Provide the frozen v0.13 candidate checkout}"
: "${UPGRADE_BASELINE_DIR:?Provide the pinned v0.12 source checkout}"
: "${UPGRADE_BASELINE_IMAGE:?Build the pinned v0.12 image first}"
: "${UPGRADE_FRAMEWORK_IMAGE:?Build the current v0.13 image first}"
: "${UPGRADE_EVIDENCE_DIR:?Provide the frozen evidence directory}"
: "${UPGRADE_FIXTURE_DIR:?Provide the frozen fixture directory}"
: "${UPGRADE_COMPARE_SCRIPT:?Provide the frozen comparison script}"
: "${UPGRADE_VECTOR_CHART:?Provide the frozen Vector Helm chart}"

kind=${KIND:-kind}
helm=${HELM:-helm}
kustomize=${KUSTOMIZE:?Provide the frozen kustomize executable}
product_version=${PRODUCT_VERSION:-3.5.5}
namespace=spark-framework-upgrade
cluster_name=upgrade
workload=upgrade-node-default
vector_workload=upgrade-node-vector
removed_workload=upgrade-node-removed
failed_probe_image=localhost/framework-upgrade-missing:never
operator_namespace=spark-k8s-operator-system
operator_deployment=spark-k8s-operator-controller-manager
evidence=$UPGRADE_EVIDENCE_DIR

kubectl_cmd=(kubectl --kubeconfig "$KUBECONFIG")
k() {
    "${kubectl_cmd[@]}" -n "$namespace" "$@"
}

current_context=$("${kubectl_cmd[@]}" config current-context)
if [[ "$current_context" != "kind-$CHAINSAW_CLUSTER" ]]; then
    echo "Refusing destructive acceptance against context '$current_context'; expected 'kind-$CHAINSAW_CLUSTER'." >&2
    exit 1
fi
if "${kubectl_cmd[@]}" get crd sparkhistoryservers.spark.kubedoop.dev >/dev/null 2>&1; then
    existing=$("${kubectl_cmd[@]}" get sparkhistoryservers.spark.kubedoop.dev -A -o name)
    if [[ -n "$existing" ]]; then
        echo "The disposable cluster already contains SparkHistoryServers; refusing to continue." >&2
        exit 1
    fi
fi
if "${kubectl_cmd[@]}" get namespace "$namespace" >/dev/null 2>&1; then
    echo "Namespace '$namespace' already exists; inspect the previous run before retrying." >&2
    exit 1
fi

scratch_base=${TMPDIR:-/tmp}
scratch=$(mktemp -d "$scratch_base/spark-framework-upgrade-worker.XXXXXX")
finish() {
    local result=$?
    if (( result != 0 )); then
        if "${kubectl_cmd[@]}" get namespace "$namespace" >/dev/null 2>&1; then
            k get sparkhistoryservers,statefulsets,pods,services,endpoints,configmaps,poddisruptionbudgets,serviceaccounts,persistentvolumeclaims \
                -o yaml > "$evidence/failure-resources.yaml" 2>&1 || true
            k get events --sort-by=.lastTimestamp > "$evidence/failure-events.txt" 2>&1 || true
            k logs statefulset/vector-aggregator -c vector --tail=300 \
                > "$evidence/failure-vector-aggregator.log" 2>&1 || true
        fi
        "${kubectl_cmd[@]}" -n "$operator_namespace" logs \
            "deployment/$operator_deployment" --tail=300 \
            > "$evidence/failure-operator.log" 2>&1 || true
        printf 'FAIL exit_code=%s\n' "$result" > "$evidence/result.txt"
    fi
    if [[ -d "$scratch" && "$scratch" == "$scratch_base"/spark-framework-upgrade-worker.* ]]; then
        rm -rf -- "$scratch"
    fi
}
trap finish EXIT

wait_for() {
    local description=$1
    shift
    local deadline=$((SECONDS + 600))
    until "$@"; do
        if (( SECONDS >= deadline )); then
            echo "Timed out: $description" >&2
            k get statefulsets,pods,services,endpoints -o wide || true
            k get events --sort-by=.lastTimestamp || true
            return 1
        fi
        sleep 5
    done
}

statefulset_ready() {
    local target=${1:-$workload}
    k get statefulset "$target" -o json |
        "${python_cmd[@]}" -c '
import json
import sys

statefulset = json.load(sys.stdin)
spec = statefulset["spec"]
status = statefulset.get("status", {})
desired = spec.get("replicas", 1)
checks = (
    status.get("readyReplicas", 0) == desired,
    status.get("currentReplicas", 0) == desired,
    status.get("observedGeneration", 0) >= statefulset["metadata"]["generation"],
    status.get("currentRevision") == status.get("updateRevision"),
)
if not all(checks):
    raise SystemExit(1)
' 2>/dev/null
}

workload_template_recovered_from_failure_probe() {
    k get statefulset "$workload" -o json |
        "${python_cmd[@]}" -c '
import json
import sys

failed_image = sys.argv[1]
statefulset = json.load(sys.stdin)
status = statefulset.get("status", {})
if status.get("observedGeneration", 0) < statefulset["metadata"]["generation"]:
    raise SystemExit(1)
node = next(
    container
    for container in statefulset["spec"]["template"]["spec"]["containers"]
    if container["name"] == "node"
)
if node.get("image") == failed_image or node.get("imagePullPolicy") == "Never":
    raise SystemExit(1)
' "$failed_probe_image" 2>/dev/null
}

failure_probe_pod_still_present() {
    k get pod "$workload-0" -o json |
        "${python_cmd[@]}" -c '
import json
import sys

failed_image = sys.argv[1]
pod = json.load(sys.stdin)
if not any(container.get("image") == failed_image for container in pod["spec"]["containers"]):
    raise SystemExit(1)
' "$failed_probe_image" 2>/dev/null
}

service_has_http_endpoint() {
    local target=${1:-$workload}
    k get endpoints "$target" -o json |
        "${python_cmd[@]}" -c '
import json
import sys

endpoint = json.load(sys.stdin)
subsets = endpoint.get("subsets", [])
reachable = any(
    subset.get("addresses")
    and any(port.get("name") == "http" and port.get("port") == 18080 for port in subset.get("ports", []))
    for subset in subsets
)
if not reachable:
    raise SystemExit(1)
' 2>/dev/null
}

vector_aggregator_logs_since() {
    local since_time=$1
    k logs statefulset/vector-aggregator -c vector --since-time="$since_time"
}

vector_delivery_marker_received() {
    local marker=$1
    local since_time=$2
    vector_aggregator_logs_since "$since_time" 2>/dev/null |
        validate_vector_delivery_event "$marker" "$namespace" "$cluster_name" >/dev/null
}

prove_vector_log_delivery() {
    local phase=$1
    local since_time marker epoch_millis raw_log
    since_time=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    marker="framework-upgrade-vector-$phase-$(date -u +%s%N)-$$"
    epoch_millis=$(date -u +%s%3N)
    printf 'marker=%s\nsince_time=%s\n' "$marker" "$since_time" \
        > "$evidence/$phase-vector-delivery.env"

    k exec "$vector_workload-0" -c node -- /bin/bash -ceu '
marker=$1
epoch_millis=$2
log_file=/kubedoop/log/node/spark.log4j2.xml
mkdir -p "$(dirname "$log_file")"
printf '\''<Event xmlns="http://logging.apache.org/log4j/2.0/events" timeMillis="%s" thread="framework-upgrade-e2e" level="INFO" loggerName="framework-upgrade-e2e"><Message>%s</Message></Event>\r\n'\'' \
    "$epoch_millis" "$marker" >> "$log_file"
' _ "$marker" "$epoch_millis"

    wait_for "$phase Vector marker delivery to the aggregator" \
        vector_delivery_marker_received "$marker" "$since_time"
    raw_log="$evidence/$phase-vector-aggregator-since.log"
    vector_aggregator_logs_since "$since_time" > "$raw_log"
    validate_vector_delivery_event "$marker" "$namespace" "$cluster_name" \
        < "$raw_log" > "$evidence/$phase-vector-delivery-event.json"
}

rollback_endpoint_ports_restored() {
    k get endpoints "$workload" -o json |
        "${python_cmd[@]}" -c '
import json
import pathlib
import sys

before_items = json.loads(pathlib.Path(sys.argv[1]).read_text())["items"]
before = next(
    item
    for item in before_items
    if item["kind"] == "Endpoints" and item["metadata"]["name"] == "upgrade-node-default"
)
current = json.load(sys.stdin)


def ports(endpoint):
    return {
        (port.get("name"), port.get("port"), port.get("protocol", "TCP"))
        for subset in endpoint.get("subsets", [])
        for port in subset.get("ports", [])
    }


if ports(current) != ports(before):
    raise SystemExit(1)
' "$evidence/before-resources.json" 2>/dev/null
}

framework_status_ready() {
    k get sparkhistoryserver "$cluster_name" -o json |
        "${python_cmd[@]}" -c '
import json
import sys

cluster = json.load(sys.stdin)
status = cluster.get("status", {})
conditions = {item.get("type"): item for item in status.get("conditions", [])}
expected = {
    "Available": "True",
    "Progressing": "False",
    "ReconcileComplete": "True",
    "Degraded": "False",
}
generation = cluster["metadata"]["generation"]
if status.get("observedGeneration") != generation:
    raise SystemExit(1)
if status.get("roleGroups", {}).get("node") != ["default", "vector"]:
    raise SystemExit(1)
if any(
    conditions.get(name, {}).get("status") != value
    or conditions.get(name, {}).get("observedGeneration") != generation
    for name, value in expected.items()
):
    raise SystemExit(1)
' 2>/dev/null
}

framework_reports_pod_failure() {
    k get sparkhistoryserver "$cluster_name" -o json |
        "${python_cmd[@]}" -c '
import json
import sys

cluster = json.load(sys.stdin)
degraded = next(
    (item for item in cluster.get("status", {}).get("conditions", []) if item.get("type") == "Degraded"),
    {},
)
if degraded.get("status") != "True" or degraded.get("reason") != "PodFailure":
    raise SystemExit(1)
' 2>/dev/null
}

removed_role_group_gone() {
    local resource
    for resource in \
        "statefulset/$removed_workload" \
        "configmap/$removed_workload" \
        "service/$removed_workload" \
        "service/$removed_workload-metrics"; do
        if k get "$resource" >/dev/null 2>&1; then
            return 1
        fi
    done
    if k get pod "$removed_workload-0" >/dev/null 2>&1; then
        return 1
    fi
    return 0
}

pdb_has_healthy_pods() {
    local expected=$1
    k get poddisruptionbudget "upgrade-node" -o json |
        "${python_cmd[@]}" -c '
import json
import sys

expected = int(sys.argv[1])
pdb = json.load(sys.stdin)
status = pdb.get("status", {})
if (status.get("observedGeneration") or 0) < pdb["metadata"]["generation"]:
    raise SystemExit(1)
if (
    status.get("expectedPods") != expected
    or status.get("desiredHealthy") != expected - 1
    or status.get("currentHealthy") != expected
    or status.get("disruptionsAllowed") != 1
):
    raise SystemExit(1)
' "$expected" 2>/dev/null
}

old_controller_reconciled() {
    "${kubectl_cmd[@]}" -n "$operator_namespace" logs \
        "deployment/$operator_deployment" --tail=300 2>/dev/null |
        grep -F "Reconciliation completed, all resources are ready" >/dev/null
}

history_pod() {
    k get pods \
        -l app.kubernetes.io/name=sparkhistoryserver \
        -o json |
        "${python_cmd[@]}" -c '
import json
import sys

pods = json.load(sys.stdin).get("items", [])
ready = [
    pod["metadata"]["name"]
    for pod in pods
    if pod.get("status", {}).get("phase") == "Running"
    and all(status.get("ready") for status in pod.get("status", {}).get("containerStatuses", []))
]
if not ready:
    raise SystemExit(1)
print(sorted(ready)[0])
'
}

history_json() {
    local pod
    pod=$(history_pod)
    k exec "$pod" -c node -- \
        curl -fsS --max-time 15 http://localhost:18080/api/v1/applications
}

wait_for_application() {
    local application_name=$1
    local deadline=$((SECONDS + 300))
    local response application_id
    until (( SECONDS >= deadline )); do
        response=$(history_json 2>/dev/null || true)
        application_id=$(
            "${python_cmd[@]}" -c '
import json
import sys

name = sys.argv[1]
try:
    applications = json.load(sys.stdin)
except json.JSONDecodeError:
    applications = []
print(next((item.get("id", "") for item in applications if item.get("name") == name), ""))
' "$application_name" <<< "$response"
        )
        if [[ -n "$application_id" ]]; then
            printf '%s\n' "$application_id"
            return 0
        fi
        echo "Waiting for Spark application '$application_name' in History Server..." >&2
        sleep 5
    done
    echo "Timed out waiting for Spark application '$application_name'." >&2
    return 1
}

submit_application() {
    local phase=$1
    local application_name=$2
    local pod spark_script
    pod=$(history_pod)
    spark_script='
export AWS_ACCESS_KEY_ID=$(cat /kubedoop/secret/s3-credentials/ACCESS_KEY)
export AWS_SECRET_ACCESS_KEY=$(cat /kubedoop/secret/s3-credentials/SECRET_KEY)
# SparkPi hard-codes its application name. spark-shell preserves the phase-specific
# --name so History Server continuity can be matched without guessing by timestamp.
# The Scala compiler needs more than the 96 MiB Metaspace cap used by SparkPi;
# the Pod memory limit still bounds the combined History Server and probe process.
printf "%s\n" \
  "val exitCode = try { if (sc.parallelize(1 to 20, 1).count() == 20L) 0 else 1 } catch { case error: Throwable => error.printStackTrace(); 1 }; try { sc.stop() } finally { System.exit(exitCode) }" |
./bin/spark-shell \
  --name "$1" \
  --master local[1] \
  --deploy-mode client \
  --conf spark.driver.host=127.0.0.1 \
  --conf spark.driver.memory=512m \
  --conf spark.driver.memoryOverhead=128m \
  --conf spark.driver.extraJavaOptions="-XX:+UseSerialGC -XX:MaxDirectMemorySize=48m" \
  --conf spark.executor.memory=512m \
  --conf spark.executor.memoryOverhead=128m \
  --conf spark.executor.extraJavaOptions="-XX:MaxMetaspaceSize=96m -XX:+UseSerialGC -XX:MaxDirectMemorySize=48m" \
  --conf spark.ui.enabled=false \
  --conf spark.eventLog.enabled=true \
  --conf spark.eventLog.dir=s3a://spark-history/events \
  --conf spark.hadoop.fs.s3a.endpoint="http://minio.$2.svc.cluster.local:9000" \
  --conf spark.hadoop.fs.s3a.path.style.access=true \
  --conf spark.hadoop.fs.s3a.connection.ssl.enabled=false
'
    k exec "$pod" -c node -- /bin/bash -ceu "$spark_script" _ "$application_name" "$namespace" \
        2>&1 | tee "$evidence/$phase-spark-submit.log"
}

render_operator() {
    local source=$1
    local image=$2
    local output=$3
    local render
    render=$(mktemp -d "$scratch/render.XXXXXX")
    cp -R "$source/config" "$render/config"
    (
        cd "$render/config/manager"
        "$kustomize" edit set image "controller=$image"
    )
    "$kustomize" build "$render/config/default" > "$output"
}

without_crd() {
    local input=$1
    local output=$2
    "${python_cmd[@]}" - "$input" "$output" <<'PY'
import pathlib
import re
import sys

source = pathlib.Path(sys.argv[1]).read_text()
documents = re.split(r"(?m)^---[ \t]*\n", source)
kept = [
    document.rstrip()
    for document in documents
    if document.strip()
    and not re.search(r"(?m)^kind:[ \t]*CustomResourceDefinition[ \t]*$", document)
]
pathlib.Path(sys.argv[2]).write_text("\n---\n".join(kept) + "\n")
PY
}

stop_operator() {
    "${kubectl_cmd[@]}" -n "$operator_namespace" scale \
        "deployment/$operator_deployment" --replicas=0
    "${kubectl_cmd[@]}" -n "$operator_namespace" wait \
        --for=delete pod \
        -l control-plane=controller-manager \
        --timeout=180s
}

deploy_operator() {
    local manifest=$1
    "${kubectl_cmd[@]}" apply -f "$manifest"
    "${kubectl_cmd[@]}" -n "$operator_namespace" rollout status \
        "deployment/$operator_deployment" --timeout=300s
}

snapshot() {
    local phase=$1
    shift
    k get statefulsets,configmaps,services,endpoints,poddisruptionbudgets,serviceaccounts,persistentvolumeclaims \
        -o json > "$evidence/$phase-resources.json"
    k get pods -o json > "$evidence/$phase-pods.json"
    k get sparkhistoryserver "$cluster_name" -o json > "$evidence/$phase-cluster.json"
    history_json > "$evidence/$phase-applications.json"
    "${python_cmd[@]}" - "$evidence/$phase-applications.json" "$@" <<'PY'
import json
import pathlib
import sys

applications = json.loads(pathlib.Path(sys.argv[1]).read_text())
present = {item.get("name") for item in applications}
missing = set(sys.argv[2:]) - present
if missing:
    raise SystemExit(f"History Server is missing applications: {sorted(missing)}")
PY
    "${kubectl_cmd[@]}" -n "$operator_namespace" logs \
        "deployment/$operator_deployment" --tail=300 \
        > "$evidence/$phase-operator.log" 2>&1
}

sed \
    -e 's/($SPARK_MINIO_USER)/spark/g' \
    -e 's/($SPARK_MINIO_PASSWORD)/sparkspark/g' \
    -e 's/($SPARK_MINIO_BUCKET)/spark-history/g' \
    "$UPGRADE_FIXTURE_DIR/minio.yaml" > "$evidence/minio.yaml"
sed \
    -e "s/(\$namespace)/$namespace/g" \
    "$UPGRADE_FIXTURE_DIR/minio-s3-connection.yaml" > "$evidence/minio-s3-connection.yaml"

cat > "$evidence/input-cluster.yaml" <<YAML
apiVersion: spark.kubedoop.dev/v1alpha1
kind: SparkHistoryServer
metadata:
  name: $cluster_name
spec:
  image:
    productVersion: "$product_version"
  clusterConfig:
    listenerClass: cluster-internal
    vectorAggregatorConfigMapName: vector-aggregator-discovery
    logFileDirectory:
      s3:
        prefix: events
        bucket:
          reference: spark-history
  node:
    roleConfig:
      podDisruptionBudget:
        enabled: true
        maxUnavailable: 1
    roleGroups:
      default:
        replicas: 1
        config:
          cleaner: false
          logging:
            enableVectorAgent: false
        podOverrides:
          spec:
            containers:
            - name: node
              resources:
                requests:
                  cpu: 1000m
                  memory: 1536Mi
                limits:
                  cpu: 2000m
                  memory: 2048Mi
      vector:
        replicas: 1
        config:
          cleaner: false
          logging:
            enableVectorAgent: true
        podOverrides:
          spec:
            containers:
            - name: node
              resources:
                requests:
                  cpu: 1000m
                  memory: 1536Mi
                limits:
                  cpu: 2000m
                  memory: 2048Mi
      removed:
        replicas: 1
        config:
          cleaner: false
        podOverrides:
          spec:
            containers:
            - name: node
              resources:
                requests:
                  cpu: 1000m
                  memory: 1536Mi
                limits:
                  cpu: 2000m
                  memory: 2048Mi
YAML

render_operator \
    "$UPGRADE_BASELINE_DIR" "$UPGRADE_BASELINE_IMAGE" \
    "$evidence/baseline-operator-full.yaml"
render_operator \
    "$UPGRADE_CANDIDATE_DIR" "$UPGRADE_FRAMEWORK_IMAGE" \
    "$evidence/framework-operator-full.yaml"
without_crd \
    "$evidence/baseline-operator-full.yaml" \
    "$evidence/baseline-controller.yaml"
without_crd \
    "$evidence/framework-operator-full.yaml" \
    "$evidence/framework-controller.yaml"

"$kind" load docker-image --name "$CHAINSAW_CLUSTER" \
    "$UPGRADE_BASELINE_IMAGE" "$UPGRADE_FRAMEWORK_IMAGE"

echo "Installing the pinned operator-go v0.12 baseline."
deploy_operator "$evidence/baseline-operator-full.yaml"
"${kubectl_cmd[@]}" create namespace "$namespace"
"$helm" upgrade --install vector-aggregator "$UPGRADE_VECTOR_CHART" \
    --namespace "$namespace" \
    --kubeconfig "$KUBECONFIG" \
    --values "$UPGRADE_FIXTURE_DIR/vector-aggregator-values.yaml" \
    --wait \
    --timeout 10m \
    2>&1 | tee "$evidence/vector-aggregator-install.log"
k apply -f "$UPGRADE_FIXTURE_DIR/vector-aggregator.yaml"
wait_for "Vector aggregator StatefulSet readiness" \
    k rollout status statefulset/vector-aggregator --timeout=10s
"$helm" get manifest vector-aggregator \
    --namespace "$namespace" \
    --kubeconfig "$KUBECONFIG" \
    > "$evidence/vector-aggregator-manifest.yaml"
k apply -f "$evidence/minio.yaml"
wait_for "MinIO StatefulSet readiness" \
    k rollout status statefulset/minio --timeout=10s
wait_for "MinIO initialization" \
    k wait --for=condition=complete job/minio-init --timeout=10s
k apply -f "$evidence/minio-s3-connection.yaml"
k apply -f "$evidence/input-cluster.yaml"
wait_for "baseline StatefulSet readiness" statefulset_ready
wait_for "baseline Vector role-group readiness" statefulset_ready "$vector_workload"
wait_for "baseline removable role-group readiness" statefulset_ready "$removed_workload"
wait_for "baseline client Service endpoints" service_has_http_endpoint
wait_for "baseline Vector client Service endpoints" service_has_http_endpoint "$vector_workload"
wait_for "baseline controller reconcile" old_controller_reconciled
wait_for "baseline PDB health" pdb_has_healthy_pods 3
prove_vector_log_delivery before

before_name=framework-upgrade-before
submit_application before "$before_name"
wait_for_application "$before_name" > "$evidence/before-application-id.txt"
snapshot before "$before_name"

echo "Upgrading in place: stop v0.12, explicitly apply the v0.13 CRD, then start v0.13."
stop_operator
k patch sparkhistoryserver "$cluster_name" --type=json -p '[
  {"op":"remove","path":"/spec/node/roleGroups/removed"}
]'
k get \
    "statefulset/$removed_workload" \
    "configmap/$removed_workload" \
    "service/$removed_workload" \
    "service/$removed_workload-metrics" \
    -o json > "$evidence/orphan-pre-upgrade-resources.json"
k get sparkhistoryserver "$cluster_name" -o json > "$evidence/orphan-pre-upgrade-cluster.json"
"${kubectl_cmd[@]}" apply --server-side \
    --field-manager=framework-upgrade-e2e \
    --force-conflicts \
    -f "$UPGRADE_FIXTURE_DIR/framework-crds.yaml"
"${kubectl_cmd[@]}" wait \
    --for=condition=Established \
    crd/sparkhistoryservers.spark.kubedoop.dev \
    --timeout=120s
"${kubectl_cmd[@]}" get \
    crd/sparkhistoryservers.spark.kubedoop.dev -o json |
    "${python_cmd[@]}" -c '
import json
import sys

crd = json.load(sys.stdin)
version = next(item for item in crd["spec"]["versions"] if item["name"] == "v1alpha1")
status = version["schema"]["openAPIV3Schema"]["properties"]["status"]["properties"]
required = {"conditions", "observedGeneration", "roleGroups", "generation", "name", "type", "urls"}
missing = required - set(status)
if missing:
    raise SystemExit(f"CRD status schema is missing fields: {sorted(missing)}")
'
deploy_operator "$evidence/framework-controller.yaml"
wait_for "v0.13 cleanup of the removed legacy role group" removed_role_group_gone
wait_for "v0.13 healthy status and role-group ledger" framework_status_ready
wait_for "v0.13 StatefulSet rollout" statefulset_ready
wait_for "v0.13 Vector role-group rollout" statefulset_ready "$vector_workload"
wait_for "v0.13 client Service endpoints" service_has_http_endpoint
wait_for "v0.13 Vector client Service endpoints" service_has_http_endpoint "$vector_workload"
wait_for_application "$before_name" > "$evidence/after-before-application-id.txt"
prove_vector_log_delivery after

echo "Verifying legacy-selector Pod failures reach the v0.13 Degraded condition."
k patch sparkhistoryserver "$cluster_name" --type=json -p '[
  {"op":"add","path":"/spec/node/roleGroups/default/podOverrides/spec/containers/0/image","value":"localhost/framework-upgrade-missing:never"},
  {"op":"add","path":"/spec/node/roleGroups/default/podOverrides/spec/containers/0/imagePullPolicy","value":"Never"}
]'
wait_for "v0.13 legacy workload PodFailure condition" framework_reports_pod_failure
k get sparkhistoryserver "$cluster_name" -o json > "$evidence/health-failure-cluster.json"
k get statefulset "$workload" -o json > "$evidence/health-failure-statefulset.json"
k get pods -o json > "$evidence/health-failure-pods.json"
k patch sparkhistoryserver "$cluster_name" --type=json -p '[
  {"op":"remove","path":"/spec/node/roleGroups/default/podOverrides/spec/containers/0/image"},
  {"op":"remove","path":"/spec/node/roleGroups/default/podOverrides/spec/containers/0/imagePullPolicy"}
]'
wait_for "v0.13 restored StatefulSet template after the PodFailure probe" \
    workload_template_recovered_from_failure_probe
# OrderedReady StatefulSets cannot roll back an already-unready Pod to a restored
# template on their own. Remove only the fault-injection Pod if it still carries
# the deliberately missing image; its stable identity and ephemeral Secret PVC
# are then recreated by the StatefulSet controller.
if failure_probe_pod_still_present; then
    k delete pod "$workload-0" --wait=true --ignore-not-found=true
fi
wait_for "v0.13 recovery after the PodFailure probe" statefulset_ready
wait_for "v0.13 healthy status after the PodFailure probe" framework_status_ready
wait_for "v0.13 PDB health after cleanup" pdb_has_healthy_pods 2
wait_for_application "$before_name" > "$evidence/after-health-before-application-id.txt"

after_name=framework-upgrade-after
submit_application after "$after_name"
wait_for_application "$after_name" > "$evidence/after-application-id.txt"
snapshot after "$before_name" "$after_name"

echo "Rolling back only the controller; the additive v0.13 CRD stays installed."
stop_operator
deploy_operator "$evidence/baseline-controller.yaml"
k annotate sparkhistoryserver "$cluster_name" \
    framework-upgrade.kubedoop.dev/phase=rollback --overwrite
wait_for "rollback v0.12 controller reconcile" old_controller_reconciled
wait_for "rollback StatefulSet rollout" statefulset_ready
wait_for "rollback Vector role-group rollout" statefulset_ready "$vector_workload"
wait_for "rollback client Service endpoints" service_has_http_endpoint
wait_for "rollback Vector client Service endpoints" service_has_http_endpoint "$vector_workload"
wait_for "rollback endpoint ports restored" rollback_endpoint_ports_restored
wait_for "healthy status and role-group ledger retained under v0.12" framework_status_ready
wait_for "rollback PDB health" pdb_has_healthy_pods 2
prove_vector_log_delivery rollback
wait_for_application "$before_name" > "$evidence/rollback-before-application-id.txt"
wait_for_application "$after_name" > "$evidence/rollback-after-application-id.txt"

rollback_name=framework-upgrade-rollback
submit_application rollback "$rollback_name"
wait_for_application "$rollback_name" > "$evidence/rollback-application-id.txt"
snapshot rollback "$before_name" "$after_name" "$rollback_name"

"${python_cmd[@]}" "$UPGRADE_COMPARE_SCRIPT" "$evidence"

# Prove the comparator is executable and fail-closed by tampering with one
# critical field in a copy of the evidence. A successful tampered comparison is
# itself an acceptance failure.
tamper_dir="$scratch/comparator-tamper"
mkdir -p "$tamper_dir"
cp "$evidence"/*.json "$tamper_dir/"
"${python_cmd[@]}" - "$tamper_dir/after-resources.json" <<'PY'
import json
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
document = json.loads(path.read_text())
for item in document["items"]:
    if item.get("kind") == "StatefulSet" and item.get("metadata", {}).get("name") == "upgrade-node-default":
        item["spec"]["serviceName"] = "tampered-service"
        break
else:
    raise SystemExit("tamper self-test could not find the target StatefulSet")
path.write_text(json.dumps(document) + "\n")
PY
if "${python_cmd[@]}" "$UPGRADE_COMPARE_SCRIPT" "$tamper_dir" \
    > "$evidence/comparator-tamper-test.log" 2>&1; then
    echo "Comparator tamper self-test unexpectedly passed." >&2
    exit 1
fi
if ! grep -Fq "v0.13 changed critical StatefulSet behavior" \
    "$evidence/comparator-tamper-test.log"; then
    echo "Comparator tamper self-test failed for an unexpected reason." >&2
    cat "$evidence/comparator-tamper-test.log" >&2
    exit 1
fi
printf 'PASS: comparator rejected a tampered StatefulSet serviceName\n' \
    > "$evidence/comparator-tamper-test.txt"

printf '%s\n' \
    "PASS: operator-go v0.12 -> v0.13 -> v0.12 kept workload and Vector identity/configuration/history, delivered phase-unique logs from the persistent Vector group, honored the explicit Vector disable, cleaned a removed legacy group, reported PodFailure, and survived rollback" |
    tee "$evidence/result.txt"
