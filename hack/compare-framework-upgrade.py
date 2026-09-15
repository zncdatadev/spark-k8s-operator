#!/usr/bin/env python3
"""Compare normalized v0.12 -> v0.13 -> v0.12 acceptance snapshots."""

from __future__ import annotations

import base64
import copy
import difflib
import io
import json
import pathlib
import re
import sys
import zipfile
from typing import Any


WORKLOAD = "upgrade-node-default"
METRICS_SERVICE = f"{WORKLOAD}-metrics"
VECTOR_WORKLOAD = "upgrade-node-vector"
VECTOR_METRICS_SERVICE = f"{VECTOR_WORKLOAD}-metrics"
REMOVED_WORKLOAD = "upgrade-node-removed"
REMOVED_EPHEMERAL_PVC = f"{REMOVED_WORKLOAD}-0-s3-credentials"
ROLE_PDB = "upgrade-node"
GENERATED_SERVICE_ACCOUNT = "sparkhistoryserver-upgrade"
METRICS_SLOT_LABEL = "metrics.kubedoop.dev/service"
SERVICE_METADATA_CHURN_LABELS = {
    "app.kubernetes.io/managed-by",
    "app.kubernetes.io/version",
}
BANZAICLOUD_LAST_APPLIED_ANNOTATION = "banzaicloud.com/last-applied"
METRICS_PROMETHEUS_LABELS = {"prometheus.io/scrape": "true"}
METRICS_PROMETHEUS_ANNOTATIONS = {
    "prometheus.io/scrape": "true",
    "prometheus.io/port": "18081",
    "prometheus.io/scheme": "http",
}
EXPECTED_HEALTHY_CONDITIONS = {
    "Available": "True",
    "Degraded": "False",
    "Progressing": "False",
    "ReconcileComplete": "True",
}
EXPECTED_DIFFERENCES = (
    "framework ownership, role-group marker, slot, version, apply bookkeeping, resourceVersion, generation, and timestamp metadata",
    "the dormant unauthenticated oidc:4180 client-Service port is absent only under v0.13",
    "equivalent shell command serialization may differ while start-history-server semantics remain",
    "the legacy-only unused log EmptyDir, node mount, and log4j2 FILE appender/ref are absent only under v0.13; console identifiers may be renamed without changing their settings",
    "the default group explicitly disables Vector: v0.12 ignores that role-group flag while the aggregator is configured, whereas v0.13 removes its legacy Vector container, vector.yaml, log volume, and FILE appender; rollback restores them",
    "the persistent vector group moves the Vector agent from a regular container to a restartable native init container; the only semantic vector.yaml delta is the v0.13 agent self-metrics source and sink (legacy doubled VRL delimiters and presentation-only scalar/operator whitespace are syntax-normalized)",
    "v0.13 creates an owned per-CR ServiceAccount, but the adopted StatefulSet keeps default",
    "v0.13 adopts the legacy PDB owner and narrows its selector to stable cross-generation labels; rollback restores the v0.12 selector",
    "StatefulSet revisions and Pod UIDs may change during controller and health-probe rollouts",
    "v0.13 status conditions and roleGroups are additive and remain after controller-only rollback",
    "the role group removed while v0.12 is stopped is cleaned by the first v0.13 reconciliation",
)


class ContractError(RuntimeError):
    """Raised when an upgrade contract is violated."""


def require(condition: bool, message: str) -> None:
    if not condition:
        raise ContractError(message)


def load_json(path: pathlib.Path) -> Any:
    return json.loads(path.read_text())


def resource_map(root: pathlib.Path, phase: str) -> dict[tuple[str, str], dict[str, Any]]:
    items = load_json(root / f"{phase}-resources.json")["items"]
    return {(item["kind"], item["metadata"]["name"]): item for item in items}


def owner_reference(item: dict[str, Any], kind: str | None = None) -> dict[str, Any] | None:
    references = item.get("metadata", {}).get("ownerReferences", [])
    for reference in references:
        if kind is None or reference.get("kind") == kind:
            return reference
    return None


def has_exact_cluster_controller_owner(
    references: list[dict[str, Any]], cluster_name: str, cluster_uid: str
) -> bool:
    return references == [
        {
            "apiVersion": "spark.kubedoop.dev/v1alpha1",
            "blockOwnerDeletion": True,
            "controller": True,
            "kind": "SparkHistoryServer",
            "name": cluster_name,
            "uid": cluster_uid,
        }
    ]


def self_test_cluster_controller_owner_contract() -> None:
    valid = [
        {
            "apiVersion": "spark.kubedoop.dev/v1alpha1",
            "blockOwnerDeletion": True,
            "controller": True,
            "kind": "SparkHistoryServer",
            "name": "upgrade",
            "uid": "cluster-uid",
        }
    ]
    require(
        has_exact_cluster_controller_owner(valid, "upgrade", "cluster-uid"),
        "controller-owner self-test rejected the exact owner",
    )
    for field, value in (
        ("apiVersion", "spark.kubedoop.dev/v2"),
        ("blockOwnerDeletion", False),
        ("controller", False),
        ("kind", "ConfigMap"),
        ("name", "other"),
        ("uid", "other-uid"),
    ):
        tampered = copy.deepcopy(valid)
        tampered[0][field] = value
        require(
            not has_exact_cluster_controller_owner(tampered, "upgrade", "cluster-uid"),
            f"controller-owner self-test accepted a wrong {field}",
        )
    require(
        not has_exact_cluster_controller_owner(valid + [{}], "upgrade", "cluster-uid"),
        "controller-owner self-test accepted an extra owner",
    )


def role_groups(cluster: dict[str, Any]) -> dict[str, list[str]] | None:
    groups = cluster.get("status", {}).get("roleGroups")
    if groups is None:
        return None
    return {role: sorted(names) for role, names in sorted(groups.items())}


def conditions(cluster: dict[str, Any]) -> dict[str, dict[str, Any]]:
    result = {}
    for condition in cluster.get("status", {}).get("conditions", []):
        result[condition["type"]] = {
            key: condition.get(key)
            for key in ("status", "reason", "message", "observedGeneration")
            if key in condition
        }
    return dict(sorted(result.items()))


def application_ids(root: pathlib.Path, phase: str) -> dict[str, str]:
    applications = load_json(root / f"{phase}-applications.json")
    return {
        application["name"]: application["id"]
        for application in applications
        if application.get("name") and application.get("id")
    }


def parse_java_properties(value: str) -> dict[str, str]:
    logical_lines: list[str] = []
    pending = ""
    for physical_line in value.splitlines():
        line = pending + physical_line.lstrip() if pending else physical_line
        trailing_slashes = len(line) - len(line.rstrip("\\"))
        if trailing_slashes % 2 == 1:
            pending = line[:-1]
            continue
        logical_lines.append(line)
        pending = ""
    if pending:
        logical_lines.append(pending)

    properties: dict[str, str] = {}
    for line in logical_lines:
        stripped = line.lstrip()
        if not stripped or stripped.startswith(("#", "!")):
            continue
        separator = None
        escaped = False
        for index, character in enumerate(stripped):
            if escaped:
                escaped = False
                continue
            if character == "\\":
                escaped = True
                continue
            if character in "=:\t ":
                separator = index
                break
        if separator is None:
            key, property_value = stripped, ""
        else:
            key = stripped[:separator].rstrip()
            remainder = stripped[separator:].lstrip()
            if remainder.startswith(("=", ":")):
                remainder = remainder[1:].lstrip()
            property_value = remainder.rstrip()
        require(key not in properties, f"duplicate Java property {key!r}")
        properties[key] = property_value
    return dict(sorted(properties.items()))


def normalize_config_data(data: dict[str, str]) -> dict[str, Any]:
    normalized: dict[str, Any] = {}
    for name, value in sorted(data.items()):
        if name == "spark-defaults.conf" or name.endswith(".properties"):
            normalized[name] = {"format": "java-properties", "values": parse_java_properties(value)}
        else:
            normalized[name] = {"format": "text", "value": value.rstrip() + "\n"}
    return normalized


def canonical_log4j2_contract(properties: dict[str, str], expect_file: bool) -> dict[str, Any]:
    """Model log4j2 semantics while ignoring only internal console identifiers.

    v0.12 calls its console appender/ref CONSOLE; v0.13 calls the property ids
    console/stdout and the appender itself STDOUT.  Those names are internal
    wiring, so the contract resolves references by appender name before it
    compares settings.  FILE may exist only in the baseline.  Every console
    setting and every unrelated property remains exact.  In particular, the
    Log4j2 default for an omitted Console target is SYSTEM_OUT, which deliberately
    does not compare equal to the legacy explicit SYSTEM_ERR target.
    """
    result = copy.deepcopy(properties)

    def pop_csv(key: str) -> list[str]:
        require(key in result, f"log4j2 config is missing {key}")
        entries = [entry.strip() for entry in result.pop(key).split(",")]
        require(all(entries), f"log4j2 config has an empty {key} identifier")
        require(len(entries) == len(set(entries)), f"log4j2 config has duplicate {key} identifiers")
        return entries

    def pop_attributes(prefix: str) -> dict[str, str]:
        attributes = {
            key[len(prefix) :]: result.pop(key)
            for key in list(result)
            if key.startswith(prefix)
        }
        require(attributes, f"log4j2 config has no properties for {prefix[:-1]}")
        return dict(sorted(attributes.items()))

    appender_names: dict[str, str] = {}
    console_appenders: list[dict[str, str]] = []
    file_appenders: list[dict[str, str]] = []
    for identifier in pop_csv("appenders"):
        attributes = pop_attributes(f"appender.{identifier}.")
        name = attributes.pop("name", None)
        require(name is not None, f"log4j2 appender {identifier} has no name")
        require(name not in appender_names, f"log4j2 appender name {name!r} is duplicated")
        appender_type = attributes.get("type")
        if appender_type == "Console":
            attributes.setdefault("target", "SYSTEM_OUT")
            appender_names[name] = "console"
            console_appenders.append(dict(sorted(attributes.items())))
        elif appender_type == "RollingFile" and name == "FILE":
            appender_names[name] = "file"
            file_appenders.append(dict(sorted(attributes.items())))
        else:
            raise ContractError(
                f"log4j2 appender {identifier} is outside the console/legacy-FILE allowlist"
            )

    root_console_refs: list[dict[str, str]] = []
    root_file_refs = 0
    for identifier in pop_csv("rootLogger.appenderRefs"):
        attributes = pop_attributes(f"rootLogger.appenderRef.{identifier}.")
        target = attributes.pop("ref", None)
        require(target is not None, f"log4j2 root appender ref {identifier} has no target")
        require(target in appender_names, f"log4j2 root appender ref {identifier} targets unknown appender {target!r}")
        if appender_names[target] == "console":
            root_console_refs.append(dict(sorted(attributes.items())))
        else:
            root_file_refs += 1

    require(len(console_appenders) == 1, "log4j2 config must declare exactly one console appender")
    require(len(root_console_refs) == 1, "log4j2 root logger must reference the console appender exactly once")
    expected_file_count = 1 if expect_file else 0
    require(len(file_appenders) == expected_file_count, f"log4j2 FILE appender count is {len(file_appenders)}, expected {expected_file_count}")
    require(root_file_refs == expected_file_count, f"log4j2 root FILE ref count is {root_file_refs}, expected {expected_file_count}")
    if file_appenders:
        file_appender = file_appenders[0]
        require(
            file_appender.get("fileName") == "/kubedoop/log/node/spark.log4j2.xml",
            "log4j2 FILE appender target changed",
        )
        require(
            file_appender.get("filePattern") == "/kubedoop/log/node/spark.log4j2.xml.%i",
            "log4j2 FILE rollover target changed",
        )
        require(
            file_appender.get("filter.threshold.type") == "ThresholdFilter"
            and file_appender.get("filter.threshold.level") == "INFO",
            "log4j2 FILE threshold is not INFO",
        )
        require(
            file_appender.get("policies.size.size") == "10MB",
            "log4j2 FILE rollover size is not 10MB",
        )
    require(
        not any(key.startswith(("appender.", "rootLogger.appenderRef.")) for key in result),
        "log4j2 config contains an undeclared appender or root reference",
    )
    return {
        "console": console_appenders[0],
        "rootConsoleRef": root_console_refs[0],
        "file": file_appenders[0] if file_appenders else None,
        "properties": dict(sorted(result.items())),
    }


def yaml_mapping_block(lines: list[str], parent: str, child: str) -> tuple[int, int, list[str]]:
    """Return one two-space child mapping block from the generated Vector YAML."""
    parent_indexes = [index for index, line in enumerate(lines) if line == f"{parent}:"]
    require(len(parent_indexes) == 1, f"vector.yaml must contain exactly one {parent} mapping")
    parent_index = parent_indexes[0]
    parent_end = parent_index + 1
    while parent_end < len(lines):
        line = lines[parent_end]
        if line and not line.startswith(" "):
            break
        parent_end += 1
    child_indexes = [
        index
        for index in range(parent_index + 1, parent_end)
        if lines[index] == f"  {child}:"
    ]
    require(len(child_indexes) == 1, f"vector.yaml must contain exactly one {parent}.{child} mapping")
    start = child_indexes[0]
    end = start + 1
    while end < len(lines):
        line = lines[end]
        if line and len(line) - len(line.lstrip(" ")) <= 2:
            break
        end += 1
    block = [line[2:] if line.startswith("  ") else line for line in lines[start:end] if line]
    return start, end, block


def normalize_legacy_vrl_braces(line: str) -> str:
    """Collapse v0.12's doubled Go-template braces outside VRL strings.

    operator-go v0.12 escaped every VRL block delimiter through text/template,
    so its rendered source contains ``{{``/``}}`` (and ``{{}}`` for an empty
    object). v0.13 emits the corresponding single VRL delimiters. Quoted text
    is data, not syntax, and is deliberately left byte-for-byte unchanged.
    """
    output: list[str] = []
    quote: str | None = None
    escaped = False
    index = 0
    while index < len(line):
        character = line[index]
        if quote is not None:
            output.append(character)
            if escaped:
                escaped = False
            elif character == "\\":
                escaped = True
            elif character == quote:
                quote = None
            index += 1
            continue
        if character in {'"', "'"}:
            quote = character
            output.append(character)
            index += 1
            continue
        pair = line[index : index + 2]
        if pair == "{{":
            output.append("{")
            index += 2
            continue
        if pair == "}}":
            output.append("}")
            index += 2
            continue
        output.append(character)
        index += 1
    require(quote is None, "vector.yaml contains an unterminated VRL string")
    return "".join(output)


def vector_yaml_contract(value: str, expect_self_metrics: bool) -> str:
    """Compare the full generated pipeline, allowing only v0.13 self-observability."""
    require("\t" not in value, "vector.yaml contains a tab")
    lines = [line.rstrip() for line in value.expandtabs(8).splitlines()]
    require(lines, "vector.yaml is empty")
    if expect_self_metrics:
        source_start, source_end, source_block = yaml_mapping_block(lines, "sources", "internal_metrics")
        require(
            source_block == ["internal_metrics:", "  type: internal_metrics"],
            "v0.13 Vector internal_metrics source changed",
        )
        del lines[source_start:source_end]
        sink_start, sink_end, sink_block = yaml_mapping_block(lines, "sinks", "metrics")
        require(
            sink_block
            == [
                "metrics:",
                "  inputs:",
                "    - internal_metrics",
                "  type: prometheus_exporter",
                "  address: 0.0.0.0:9598",
            ],
            "v0.13 Vector metrics sink changed",
        )
        del lines[sink_start:sink_end]
    else:
        require(
            "  internal_metrics:" not in lines and "  metrics:" not in lines,
            "legacy vector.yaml unexpectedly contains v0.13 self-metrics",
        )

    # The v0.13 renderer removed redundant quotes around a few plain YAML
    # scalars (for example type: "file" -> type: file). This quote normalization
    # applies only outside block scalars; VRL blocks receive only the explicit
    # legacy-brace and operator-whitespace handling above.
    normalized: list[str] = []
    block_indent: int | None = None
    block_is_vrl = False
    for line in lines:
        stripped = line.lstrip(" ")
        indent = len(line) - len(stripped)
        if block_indent is not None:
            if not stripped or indent > block_indent:
                if stripped:
                    if block_is_vrl and not expect_self_metrics:
                        line = normalize_legacy_vrl_braces(line)
                    normalized.append(re.sub(r'"\s*\+\s*', '" + ', line))
                continue
            block_indent = None
            block_is_vrl = False
        if stripped.endswith(": |") or stripped.endswith(": >"):
            block_indent = indent
            block_is_vrl = stripped in {"source: |", "source: >"}
        elif stripped.startswith('- "') and stripped.endswith('"') and "\\" not in stripped[3:-1]:
            line = " " * indent + "- " + stripped[3:-1]
        elif ": " in stripped:
            key, scalar = stripped.split(": ", 1)
            if scalar.startswith('"') and scalar.endswith('"') and "\\" not in scalar[1:-1]:
                line = " " * indent + key + ": " + scalar[1:-1]
        if stripped:
            normalized.append(re.sub(r'"\s*\+\s*', '" + ', line))
    return "\n".join(normalized).strip() + "\n"


def assert_vector_yaml_compatibility(before: str, after: str, rollback: str) -> None:
    require(before == rollback, "rollback did not restore vector.yaml exactly")
    baseline = vector_yaml_contract(before, expect_self_metrics=False)
    upgraded = vector_yaml_contract(after, expect_self_metrics=True)
    require(upgraded == baseline, "v0.13 changed Vector delivery semantics beyond self-metrics")


def assert_config_map_compatibility(
    before: dict[str, Any],
    after: dict[str, Any],
    rollback: dict[str, Any],
    *,
    vector_enabled_after: bool,
) -> None:
    require(before["uid"] == after["uid"] == rollback["uid"], "ConfigMap UID changed")
    require("spark-defaults.conf" in before["data"], "spark-defaults.conf is missing")
    require(before["binaryData"] == after["binaryData"] == rollback["binaryData"], "ConfigMap binaryData changed")
    require(before["data"] == rollback["data"], "rollback did not restore configuration semantics")
    expected_after_files = set(before["data"])
    if not vector_enabled_after:
        require("vector.yaml" in expected_after_files, "baseline ConfigMap has no vector.yaml")
        expected_after_files.remove("vector.yaml")
    require(set(after["data"]) == expected_after_files, "v0.13 changed the ConfigMap data-file inventory")

    for file_name, baseline_file in before["data"].items():
        if file_name == "vector.yaml":
            require(
                baseline_file.get("format") == "text",
                "vector.yaml is not captured as text",
            )
            if vector_enabled_after:
                upgraded_file = after["data"][file_name]
                assert_vector_yaml_compatibility(
                    baseline_file["value"], upgraded_file["value"], rollback["data"][file_name]["value"]
                )
            else:
                require(file_name not in after["data"], "v0.13 retained vector.yaml for a disabled group")
            continue
        upgraded_file = after["data"][file_name]
        if file_name != "log4j2.properties":
            require(
                upgraded_file == baseline_file,
                f"v0.13 changed rendered configuration semantics in {file_name}",
            )
            continue

        require(
            baseline_file.get("format") == upgraded_file.get("format") == "java-properties",
            "log4j2.properties is not Java properties in every migration phase",
        )
        baseline_contract = canonical_log4j2_contract(baseline_file["values"], expect_file=True)
        upgraded_contract = canonical_log4j2_contract(
            upgraded_file["values"], expect_file=vector_enabled_after
        )
        if not vector_enabled_after:
            baseline_contract["file"] = None
        require(
            upgraded_contract == baseline_contract,
            "v0.13 changed log4j2 semantics beyond the reviewed Vector transition and console identifiers",
        )


def self_test_log4j2_file_allowlist() -> None:
    baseline = {
        "appenders": "FILE, CONSOLE",
        "appender.CONSOLE.name": "CONSOLE",
        "appender.CONSOLE.type": "Console",
        "appender.CONSOLE.target": "SYSTEM_ERR",
        "appender.CONSOLE.layout.pattern": "%d{ISO8601} %p [%t] %c - %m%n",
        "appender.CONSOLE.layout.type": "PatternLayout",
        "appender.CONSOLE.filter.threshold.level": "INFO",
        "appender.CONSOLE.filter.threshold.type": "ThresholdFilter",
        "appender.FILE.fileName": "/kubedoop/log/node/spark.log4j2.xml",
        "appender.FILE.filePattern": "/kubedoop/log/node/spark.log4j2.xml.%i",
        "appender.FILE.name": "FILE",
        "appender.FILE.type": "RollingFile",
        "appender.FILE.filter.threshold.level": "INFO",
        "appender.FILE.filter.threshold.type": "ThresholdFilter",
        "appender.FILE.policies.size.size": "10MB",
        "rootLogger.appenderRef.CONSOLE.ref": "CONSOLE",
        "rootLogger.appenderRef.FILE.ref": "FILE",
        "rootLogger.appenderRefs": "CONSOLE, FILE",
        "rootLogger.level": "INFO",
    }
    expected = {
        "appenders": "console",
        "appender.console.name": "STDOUT",
        "appender.console.type": "Console",
        "appender.console.target": "SYSTEM_ERR",
        "appender.console.layout.pattern": "%d{ISO8601} %p [%t] %c - %m%n",
        "appender.console.layout.type": "PatternLayout",
        "appender.console.filter.threshold.level": "INFO",
        "appender.console.filter.threshold.type": "ThresholdFilter",
        "rootLogger.appenderRef.stdout.ref": "STDOUT",
        "rootLogger.appenderRefs": "stdout",
        "rootLogger.level": "INFO",
    }
    baseline_without_file = canonical_log4j2_contract(baseline, expect_file=True)
    baseline_without_file["file"] = None
    require(
        baseline_without_file == canonical_log4j2_contract(expected, expect_file=False),
        "log4j2 semantic allowlist self-test failed",
    )

    def contract(
        values: dict[str, str],
        spark_defaults: str = "file:/events",
        *,
        include_vector: bool,
    ) -> dict[str, Any]:
        data: dict[str, Any] = {
            "log4j2.properties": {
                "format": "java-properties",
                "values": dict(sorted(values.items())),
            },
            "spark-defaults.conf": {
                "format": "java-properties",
                "values": {"spark.history.fs.logDirectory": spark_defaults},
            },
        }
        if include_vector:
            data["vector.yaml"] = {"format": "text", "value": "sources:\n  vector:\n    type: internal_logs\n"}
        return {
            "uid": "config-map-uid",
            "data": data,
            "binaryData": {"fixture": "c3RyaWN0"},
        }

    def require_rejected(
        description: str,
        before: dict[str, Any],
        after: dict[str, Any],
        rollback: dict[str, Any],
    ) -> None:
        try:
            assert_config_map_compatibility(
                before, after, rollback, vector_enabled_after=False
            )
        except ContractError:
            return
        raise ContractError(f"log4j2 FILE allowlist self-test accepted {description}")

    before = contract(baseline, include_vector=True)
    after = contract(expected, include_vector=False)
    rollback = copy.deepcopy(before)
    assert_config_map_compatibility(
        before, after, rollback, vector_enabled_after=False
    )

    tampered_logger = copy.deepcopy(after)
    tampered_logger["data"]["log4j2.properties"]["values"]["rootLogger.level"] = "DEBUG"
    require_rejected("an unrelated logger change", before, tampered_logger, rollback)

    changed_target = copy.deepcopy(after)
    changed_target["data"]["log4j2.properties"]["values"].pop("appender.console.target")
    require_rejected("the SYSTEM_ERR to default SYSTEM_OUT target change", before, changed_target, rollback)

    missing_threshold = copy.deepcopy(after)
    threshold = missing_threshold["data"]["log4j2.properties"]["values"]
    threshold.pop("appender.console.filter.threshold.level")
    threshold.pop("appender.console.filter.threshold.type")
    require_rejected("removal of the console threshold", before, missing_threshold, rollback)

    changed_file_threshold = copy.deepcopy(before)
    changed_file_threshold["data"]["log4j2.properties"]["values"][
        "appender.FILE.filter.threshold.level"
    ] = "DEBUG"
    require_rejected(
        "a non-INFO FILE threshold",
        changed_file_threshold,
        after,
        copy.deepcopy(changed_file_threshold),
    )

    changed_rollover_size = copy.deepcopy(before)
    changed_rollover_size["data"]["log4j2.properties"]["values"][
        "appender.FILE.policies.size.size"
    ] = "20MB"
    require_rejected(
        "a non-10MB FILE rollover",
        changed_rollover_size,
        after,
        copy.deepcopy(changed_rollover_size),
    )

    tampered_other_file = contract(
        expected, "s3a://different/events", include_vector=False
    )
    require_rejected("a change to another ConfigMap file", before, tampered_other_file, rollback)

    incomplete_rollback = copy.deepcopy(after)
    require_rejected("an incomplete rollback", before, after, incomplete_rollback)


def self_test_vector_yaml_contract() -> None:
    baseline = """api:
  enabled: true
  marker: "{{not-vrl}}"
sources:
  vector:
    type: internal_logs
transforms:
  processed_files_log4j2:
    type: remap
    source: |
      literal = "{{quoted-vrl-data}}"
      event = {{}}
      if err != null {{
        error = "not parsable: "+ err
      }} else {{
        event = object!(parsed_event.Event)
      }}
sinks:
  aggregator:
    inputs:
      - extended_logs
    type: vector
    address: "vector-aggregator:6000"
"""
    upgraded = """api:
  enabled: true
  marker: "{{not-vrl}}"
sources:
  vector:
    type: internal_logs
  internal_metrics:
    type: internal_metrics
transforms:
  processed_files_log4j2:
    type: remap
    source: |
      literal = "{{quoted-vrl-data}}"
      event = {}
      if err != null {
        error = "not parsable: " + err
      } else {
        event = object!(parsed_event.Event)
      }
sinks:
  aggregator:
    inputs:
      - extended_logs
    type: vector
    address: "vector-aggregator:6000"
  metrics:
    inputs:
      - internal_metrics
    type: prometheus_exporter
    address: 0.0.0.0:9598
"""
    assert_vector_yaml_compatibility(baseline, upgraded, baseline)

    def require_rejected(description: str, candidate: str, rollback: str = baseline) -> None:
        try:
            assert_vector_yaml_compatibility(baseline, candidate, rollback)
        except ContractError:
            return
        raise ContractError(f"vector.yaml self-test accepted {description}")

    require_rejected(
        "a changed aggregator address",
        upgraded.replace("vector-aggregator:6000", "vector-aggregator:7000"),
    )
    require_rejected(
        "a missing self-metrics source",
        upgraded.replace("  internal_metrics:\n    type: internal_metrics\n", ""),
    )
    require_rejected(
        "a changed VRL condition",
        upgraded.replace("if err != null {", "if err == null {"),
    )
    require_rejected(
        "a changed brace sequence inside a quoted VRL string",
        upgraded.replace("{{quoted-vrl-data}}", "{quoted-vrl-data}", 1),
    )
    require_rejected(
        "brace rewriting outside a VRL source block",
        upgraded.replace("{{not-vrl}}", "{not-vrl}"),
    )
    require_rejected("an incomplete rollback", upgraded, upgraded)

    require(
        normalize_legacy_vrl_braces('if ok {{ value = "{{literal}}" }}')
        == 'if ok { value = "{{literal}}" }',
        "legacy VRL brace normalizer changed quoted data or missed syntax",
    )


def config_map_contract(config_map: dict[str, Any]) -> dict[str, Any]:
    return {
        "uid": config_map["metadata"]["uid"],
        "data": normalize_config_data(config_map.get("data", {})),
        "binaryData": dict(sorted(config_map.get("binaryData", {}).items())),
    }


def normalized_named_items(items: list[dict[str, Any]], ignored: set[str] | None = None) -> list[dict[str, Any]]:
    ignored = ignored or set()
    return sorted(
        (copy.deepcopy(item) for item in items if item.get("name") not in ignored),
        key=lambda item: (item.get("name", ""), json.dumps(item, sort_keys=True)),
    )


def container_contract(container: dict[str, Any]) -> dict[str, Any]:
    return {
        "image": container.get("image"),
        "imagePullPolicy": container.get("imagePullPolicy"),
        "resources": container.get("resources", {}),
        "env": normalized_named_items(container.get("env", [])),
        "envFrom": container.get("envFrom", []),
        "ports": normalized_named_items(container.get("ports", [])),
        "volumeMounts": normalized_named_items(container.get("volumeMounts", []), {"log"}),
        "securityContext": container.get("securityContext", {}),
        "livenessProbe": container.get("livenessProbe"),
        "readinessProbe": container.get("readinessProbe"),
        "startupProbe": container.get("startupProbe"),
        "lifecycle": container.get("lifecycle"),
        "workingDir": container.get("workingDir"),
        "stdin": container.get("stdin", False),
        "tty": container.get("tty", False),
        "commandText": " ".join(container.get("command", []) + container.get("args", [])),
    }


def vector_sidecar_contract(template: dict[str, Any]) -> dict[str, Any] | None:
    spec = template["spec"]
    matches = [
        ("container", container)
        for container in spec.get("containers", [])
        if container.get("name") == "vector"
    ] + [
        ("initContainer", container)
        for container in spec.get("initContainers", [])
        if container.get("name") == "vector"
    ]
    require(len(matches) <= 1, "StatefulSet template has duplicate Vector agents")
    if not matches:
        return None
    location, container = matches[0]
    return {
        "location": location,
        "image": container.get("image"),
        "imagePullPolicy": container.get("imagePullPolicy"),
        "command": container.get("command", []),
        "args": container.get("args", []),
        "restartPolicy": container.get("restartPolicy"),
        "resources": container.get("resources", {}),
        "env": normalized_named_items(container.get("env", [])),
        "ports": normalized_named_items(container.get("ports", [])),
        "volumeMounts": normalized_named_items(container.get("volumeMounts", [])),
        "securityContext": container.get("securityContext", {}),
        "livenessProbe": container.get("livenessProbe"),
        "readinessProbe": container.get("readinessProbe"),
        "startupProbe": container.get("startupProbe"),
        "lifecycle": container.get("lifecycle"),
    }


def pod_spec_contract(template: dict[str, Any]) -> dict[str, Any]:
    spec = template["spec"]
    containers = {container["name"]: container for container in spec.get("containers", [])}
    require("node" in containers, "StatefulSet template has no node container")
    return {
        "stableLabels": {
            key: template.get("metadata", {}).get("labels", {}).get(key)
            for key in ("app.kubernetes.io/name", "app.kubernetes.io/instance", "app.kubernetes.io/component")
        },
        "containerNames": sorted(containers),
        "initContainerNames": sorted(container["name"] for container in spec.get("initContainers", [])),
        "node": container_contract(containers["node"]),
        "allNodeVolumeMountNames": sorted(
            mount.get("name") for mount in containers["node"].get("volumeMounts", [])
        ),
        "serviceAccountName": spec.get("serviceAccountName") or "default",
        "enableServiceLinks": spec.get("enableServiceLinks", True),
        "imagePullSecrets": normalized_named_items(spec.get("imagePullSecrets", [])),
        "securityContext": spec.get("securityContext", {}),
        "terminationGracePeriodSeconds": spec.get("terminationGracePeriodSeconds"),
        "restartPolicy": spec.get("restartPolicy", "Always"),
        "dnsPolicy": spec.get("dnsPolicy", "ClusterFirst"),
        "schedulerName": spec.get("schedulerName", "default-scheduler"),
        "nodeSelector": spec.get("nodeSelector", {}),
        "affinity": spec.get("affinity"),
        "tolerations": spec.get("tolerations", []),
        "priorityClassName": spec.get("priorityClassName"),
        "volumes": normalized_named_items(spec.get("volumes", []), {"log"}),
        "allVolumeNames": sorted(volume.get("name") for volume in spec.get("volumes", [])),
        "vectorSidecar": vector_sidecar_contract(template),
    }


def statefulset_contract(statefulset: dict[str, Any]) -> dict[str, Any]:
    status = statefulset.get("status", {})
    log_volumes = [
        volume
        for volume in statefulset["spec"]["template"]["spec"].get("volumes", [])
        if volume.get("name") == "log"
    ]
    require(len(log_volumes) <= 1, "StatefulSet template has duplicate log volumes")
    return {
        "uid": statefulset["metadata"]["uid"],
        "replicas": statefulset["spec"].get("replicas", 1),
        "selector": statefulset["spec"]["selector"],
        "serviceName": statefulset["spec"]["serviceName"],
        "podManagementPolicy": statefulset["spec"].get("podManagementPolicy", "OrderedReady"),
        "updateStrategy": statefulset["spec"].get("updateStrategy", {"type": "RollingUpdate"}),
        "minReadySeconds": statefulset["spec"].get("minReadySeconds", 0),
        "persistentVolumeClaimRetentionPolicy": statefulset["spec"].get("persistentVolumeClaimRetentionPolicy"),
        "volumeClaimTemplates": statefulset["spec"].get("volumeClaimTemplates", []),
        "template": pod_spec_contract(statefulset["spec"]["template"]),
        "logVolume": copy.deepcopy(log_volumes[0]) if log_volumes else None,
        "status": {
            key: status.get(key)
            for key in ("currentRevision", "updateRevision", "currentReplicas", "readyReplicas", "updatedReplicas", "availableReplicas")
        },
    }


def statefulset_behavior(contract: dict[str, Any]) -> dict[str, Any]:
    behavior = copy.deepcopy(contract)
    behavior.pop("uid")
    behavior.pop("status")
    behavior.pop("logVolume")
    command = behavior["template"]["node"].pop("commandText")
    behavior["template"].pop("vectorSidecar")
    behavior["template"]["containerNames"] = [
        name for name in behavior["template"]["containerNames"] if name != "vector"
    ]
    behavior["template"]["initContainerNames"] = [
        name for name in behavior["template"]["initContainerNames"] if name != "vector"
    ]
    behavior["template"]["volumes"] = [
        volume
        for volume in behavior["template"]["volumes"]
        if volume.get("name") not in {"vector-config", "vector-data"}
    ]
    behavior["template"].pop("allNodeVolumeMountNames")
    behavior["template"].pop("allVolumeNames")
    required_command_fragments = (
        "start-history-server.sh",
        "spark-defaults.conf",
        "/kubedoop/mount/config",
        "/kubedoop/config",
        "AWS_ACCESS_KEY_ID",
        "AWS_SECRET_ACCESS_KEY",
        "/kubedoop/secret/s3-credentials/ACCESS_KEY",
        "/kubedoop/secret/s3-credentials/SECRET_KEY",
    )
    missing = [fragment for fragment in required_command_fragments if fragment not in command]
    require(not missing, f"node command lost required behavior: {missing}")
    return behavior


def service_port_contract(port: dict[str, Any]) -> dict[str, Any]:
    return {
        "name": port.get("name"),
        "port": port.get("port"),
        "protocol": port.get("protocol", "TCP"),
        "targetPort": port.get("targetPort"),
        "nodePort": port.get("nodePort"),
        "appProtocol": port.get("appProtocol"),
    }


def banzaicloud_last_applied_contract(annotations: dict[str, str]) -> dict[str, Any] | None:
    encoded = annotations.get(BANZAICLOUD_LAST_APPLIED_ANNOTATION)
    if encoded is None:
        return None
    try:
        archive_bytes = base64.b64decode(encoded, validate=True)
        with zipfile.ZipFile(io.BytesIO(archive_bytes)) as archive:
            require(
                archive.namelist() == ["original"],
                "Service apply-bookkeeping archive does not contain exactly the original entry",
            )
            document = json.loads(archive.read("original"))
    except ContractError:
        raise
    except (ValueError, OSError, zipfile.BadZipFile) as error:
        raise ContractError(f"Service apply-bookkeeping annotation is invalid: {error}") from error
    require(isinstance(document, dict), "Service apply-bookkeeping payload is not an object")
    normalized = copy.deepcopy(document)
    spec = normalized.get("spec")
    require(isinstance(spec, dict), "Service apply-bookkeeping payload has no spec object")
    # The legacy patch helper records the immutable live ClusterIP after the v0.13
    # controller changes a client Service and v0.12 later takes it back. The live
    # Service contract validates ClusterIP separately; no other payload drift is allowed.
    spec.pop("clusterIP", None)
    return normalized


def encode_last_applied_for_self_test(document: dict[str, Any]) -> str:
    output = io.BytesIO()
    with zipfile.ZipFile(output, "w", zipfile.ZIP_DEFLATED) as archive:
        archive.writestr("original", json.dumps(document))
    return base64.b64encode(output.getvalue()).decode()


def service_contract(service: dict[str, Any]) -> dict[str, Any]:
    spec = service["spec"]
    base_spec = {key: value for key, value in spec.items() if key not in {"ports", "selector"}}
    metadata = service["metadata"]
    labels = metadata.get("labels", {})
    annotations = metadata.get("annotations", {})
    instance = labels.get("app.kubernetes.io/instance")
    role_group = labels.get("app.kubernetes.io/role-group")
    framework_role_group_marker = f"{instance}-{role_group}" if instance and role_group else None
    return {
        "uid": metadata["uid"],
        "frameworkRoleGroupMarker": (
            labels.get(framework_role_group_marker) if framework_role_group_marker else None
        ),
        "stableLabels": dict(
            sorted(
                (key, value)
                for key, value in labels.items()
                if key not in SERVICE_METADATA_CHURN_LABELS
                and not (key == framework_role_group_marker and value == "true")
            )
        ),
        "annotations": dict(
            sorted(
                (key, value)
                for key, value in annotations.items()
                if key != BANZAICLOUD_LAST_APPLIED_ANNOTATION
            )
        ),
        "applyBookkeeping": banzaicloud_last_applied_contract(annotations),
        "selector": dict(sorted(spec.get("selector", {}).items())),
        "ports": sorted(
            (service_port_contract(port) for port in spec.get("ports", [])),
            key=lambda port: (port["name"] or "", port["port"] or 0),
        ),
        "baseSpec": base_spec,
    }


def without_metrics_slot_label(labels: dict[str, str]) -> dict[str, str]:
    result = dict(labels)
    result.pop(METRICS_SLOT_LABEL, None)
    return result


def self_test_service_metadata_contract() -> None:
    apply_payload = {
        "metadata": {"name": "sparkhistory", "namespace": "default"},
        "spec": {"ports": [{"name": "http", "port": 18080}], "type": "ClusterIP"},
    }
    service = {
        "metadata": {
            "uid": "service-uid",
            "labels": {
                "app.kubernetes.io/name": "sparkhistoryserver",
                "app.kubernetes.io/instance": "sparkhistory",
                "app.kubernetes.io/role-group": "default",
                "app.kubernetes.io/managed-by": "spark.kubedoop.dev",
                **METRICS_PROMETHEUS_LABELS,
            },
            "annotations": {
                **METRICS_PROMETHEUS_ANNOTATIONS,
                BANZAICLOUD_LAST_APPLIED_ANNOTATION: encode_last_applied_for_self_test(
                    apply_payload
                ),
            },
        },
        "spec": {"clusterIP": "None", "ports": [], "selector": {}},
    }
    baseline = service_contract(service)

    framework_metadata = copy.deepcopy(service)
    framework_metadata["metadata"]["labels"]["app.kubernetes.io/managed-by"] = "operator-go"
    require(
        service_contract(framework_metadata) == baseline,
        "Service metadata self-test did not isolate reviewed managed-by label churn",
    )

    apply_bookkeeping = copy.deepcopy(framework_metadata)
    apply_payload_with_cluster_ip = copy.deepcopy(apply_payload)
    apply_payload_with_cluster_ip["spec"]["clusterIP"] = "10.0.0.1"
    apply_bookkeeping["metadata"]["annotations"][BANZAICLOUD_LAST_APPLIED_ANNOTATION] = (
        encode_last_applied_for_self_test(apply_payload_with_cluster_ip)
    )
    require(
        service_contract(apply_bookkeeping) == baseline,
        "Service metadata self-test did not isolate the recorded ClusterIP",
    )

    tampered_apply_bookkeeping = copy.deepcopy(framework_metadata)
    tampered_apply_payload = copy.deepcopy(apply_payload)
    tampered_apply_payload["spec"]["ports"][0]["port"] = 9999
    tampered_apply_bookkeeping["metadata"]["annotations"][BANZAICLOUD_LAST_APPLIED_ANNOTATION] = (
        encode_last_applied_for_self_test(tampered_apply_payload)
    )
    require(
        service_contract(tampered_apply_bookkeeping) != baseline,
        "Service metadata self-test ignored apply-bookkeeping payload drift",
    )

    invalid_apply_bookkeeping = copy.deepcopy(framework_metadata)
    invalid_apply_bookkeeping["metadata"]["annotations"][BANZAICLOUD_LAST_APPLIED_ANNOTATION] = "invalid"
    try:
        service_contract(invalid_apply_bookkeeping)
    except ContractError:
        pass
    else:
        raise ContractError("Service metadata self-test accepted invalid apply bookkeeping")

    role_group_marker = copy.deepcopy(framework_metadata)
    role_group_marker["metadata"]["labels"]["sparkhistory-default"] = "true"
    role_group_marker_contract = service_contract(role_group_marker)
    require(
        role_group_marker_contract["stableLabels"] == baseline["stableLabels"]
        and role_group_marker_contract["frameworkRoleGroupMarker"] == "true",
        "Service metadata self-test did not isolate the framework role-group marker",
    )

    tampered_role_group_marker = copy.deepcopy(role_group_marker)
    tampered_role_group_marker["metadata"]["labels"]["sparkhistory-default"] = "false"
    require(
        service_contract(tampered_role_group_marker) != baseline,
        "Service metadata self-test ignored a malformed framework role-group marker",
    )

    metrics_slot = copy.deepcopy(framework_metadata)
    metrics_slot["metadata"]["labels"][METRICS_SLOT_LABEL] = "true"
    require(
        without_metrics_slot_label(service_contract(metrics_slot)["stableLabels"])
        == baseline["stableLabels"],
        "Service metadata self-test did not isolate the metrics slot label",
    )
    require(
        service_contract(metrics_slot)["stableLabels"].get(METRICS_SLOT_LABEL) == "true",
        "Service metadata self-test lost the metrics slot label",
    )

    tampered_label = copy.deepcopy(service)
    tampered_label["metadata"]["labels"]["prometheus.io/scrape"] = "false"
    require(
        service_contract(tampered_label) != baseline,
        "Service metadata self-test ignored a Prometheus label change",
    )

    tampered_annotation = copy.deepcopy(service)
    tampered_annotation["metadata"]["annotations"]["prometheus.io/port"] = "9999"
    require(
        service_contract(tampered_annotation) != baseline,
        "Service metadata self-test ignored a Prometheus annotation change",
    )


def endpoint_contract(endpoint: dict[str, Any]) -> dict[str, Any]:
    addresses = []
    ports = set()
    for subset in endpoint.get("subsets", []):
        for address in subset.get("addresses", []):
            target = address.get("targetRef", {})
            addresses.append(
                {
                    "ip": address.get("ip"),
                    "targetKind": target.get("kind"),
                    "targetName": target.get("name"),
                    "targetUID": target.get("uid"),
                }
            )
        for port in subset.get("ports", []):
            ports.add((port.get("name"), port.get("port"), port.get("protocol", "TCP")))
    return {
        "uid": endpoint["metadata"]["uid"],
        "addresses": sorted(addresses, key=lambda item: (item["ip"] or "", item["targetName"] or "")),
        "ports": [
            {"name": name, "port": port, "protocol": protocol}
            for name, port, protocol in sorted(ports, key=lambda item: (item[0] or "", item[1] or 0))
        ],
    }


def selector_matches(selector: dict[str, Any], labels: dict[str, str]) -> bool:
    if any(labels.get(key) != value for key, value in selector.get("matchLabels", {}).items()):
        return False
    for expression in selector.get("matchExpressions", []):
        key = expression["key"]
        operator = expression["operator"]
        values = expression.get("values", [])
        if operator == "In":
            if labels.get(key) not in values:
                return False
        elif operator == "NotIn":
            # Kubernetes NotIn requires the key to exist as well as its value to
            # be outside the set. A missing key must not make a PDB look matched.
            if key not in labels or labels[key] in values:
                return False
        elif operator == "Exists":
            if key not in labels:
                return False
        elif operator == "DoesNotExist":
            if key in labels:
                return False
        else:
            raise ContractError(f"unsupported label-selector operator {operator!r}")
    return True


def self_test_selector_matches() -> None:
    not_in = {"matchExpressions": [{"key": "zone", "operator": "NotIn", "values": ["blocked"]}]}
    require(not selector_matches(not_in, {}), "selector self-test let NotIn match a missing key")
    require(selector_matches(not_in, {"zone": "allowed"}), "selector self-test rejected a valid NotIn match")
    require(not selector_matches(not_in, {"zone": "blocked"}), "selector self-test accepted a blocked NotIn value")


def pdb_contract(pdb: dict[str, Any]) -> dict[str, Any]:
    status = pdb.get("status", {})
    return {
        "uid": pdb["metadata"]["uid"],
        "ownerReferences": pdb["metadata"].get("ownerReferences", []),
        "generation": pdb["metadata"]["generation"],
        "selector": pdb["spec"].get("selector", {}),
        "minAvailable": pdb["spec"].get("minAvailable"),
        "maxUnavailable": pdb["spec"].get("maxUnavailable"),
        "unhealthyPodEvictionPolicy": pdb["spec"].get("unhealthyPodEvictionPolicy"),
        "status": {
            key: status.get(key)
            for key in (
                "observedGeneration",
                "expectedPods",
                "currentHealthy",
                "desiredHealthy",
                "disruptionsAllowed",
            )
        },
    }


def pdb_status_is_current(pdb: dict[str, Any]) -> bool:
    observed_generation = pdb["status"].get("observedGeneration")
    return isinstance(observed_generation, int) and observed_generation >= pdb["generation"]


def pdb_counts_are_healthy(pdb: dict[str, Any], expected_pods: int) -> bool:
    status = pdb["status"]
    return (
        status.get("expectedPods") == expected_pods
        and status.get("desiredHealthy") == expected_pods - 1
        and status.get("currentHealthy") == expected_pods
        and status.get("disruptionsAllowed") == 1
    )


def self_test_pdb_status_contract() -> None:
    current = {
        "generation": 4,
        "status": {
            "observedGeneration": 4,
            "expectedPods": 2,
            "desiredHealthy": 1,
            "currentHealthy": 2,
            "disruptionsAllowed": 1,
        },
    }
    stale = copy.deepcopy(current)
    stale["status"]["observedGeneration"] = 3
    missing = copy.deepcopy(current)
    missing["status"]["observedGeneration"] = None
    require(pdb_status_is_current(current), "PDB status self-test rejected a current observation")
    require(not pdb_status_is_current(stale), "PDB status self-test accepted a stale observation")
    require(not pdb_status_is_current(missing), "PDB status self-test accepted a missing observation")
    require(pdb_counts_are_healthy(current, 2), "PDB status self-test rejected healthy disruption counts")
    blocked = copy.deepcopy(current)
    blocked["status"]["disruptionsAllowed"] = 0
    require(not pdb_counts_are_healthy(blocked, 2), "PDB status self-test accepted a blocked disruption budget")


def pod_is_ready(pod: dict[str, Any]) -> bool:
    ready_condition = any(
        condition.get("type") == "Ready" and condition.get("status") == "True"
        for condition in pod.get("status", {}).get("conditions", [])
    )
    statuses = pod.get("status", {}).get("containerStatuses", [])
    return ready_condition and bool(statuses) and all(status.get("ready") for status in statuses)


def workload_pod(
    pods: list[dict[str, Any]], statefulset_uid: str, workload: str = WORKLOAD
) -> dict[str, Any]:
    matches = [
        pod
        for pod in pods
        if pod["metadata"]["name"].startswith(f"{workload}-")
        and (owner_reference(pod, "StatefulSet") or {}).get("uid") == statefulset_uid
    ]
    names = [pod["metadata"]["name"] for pod in matches]
    require(len(matches) == 1, f"expected one {workload} Pod, found {names}")
    return matches[0]


def pod_volume_claims(pod: dict[str, Any]) -> tuple[list[str], dict[str, str]]:
    """Resolve ordinary and generic ephemeral PVC names referenced by a Pod."""
    persistent_claims: list[str] = []
    ephemeral_claims: dict[str, str] = {}
    pod_name = pod["metadata"]["name"]
    for volume in pod["spec"].get("volumes", []):
        if "persistentVolumeClaim" in volume:
            persistent_claims.append(volume["persistentVolumeClaim"]["claimName"])
        if "ephemeral" in volume:
            volume_name = volume.get("name")
            require(volume_name, f"Pod {pod_name} has an unnamed generic ephemeral volume")
            require(
                volume_name not in ephemeral_claims,
                f"Pod {pod_name} has duplicate generic ephemeral volume {volume_name}",
            )
            ephemeral_claims[volume_name] = f"{pod_name}-{volume_name}"
    return sorted(persistent_claims), dict(sorted(ephemeral_claims.items()))


def pod_contract(pod: dict[str, Any]) -> dict[str, Any]:
    status_by_name = {
        status["name"]: {
            "image": status.get("image"),
            "imageID": status.get("imageID"),
            "ready": status.get("ready"),
            "restartCount": status.get("restartCount"),
            "state": status.get("state", {}),
        }
        for status in pod.get("status", {}).get("containerStatuses", [])
    }
    init_status_by_name = {
        status["name"]: {
            "image": status.get("image"),
            "imageID": status.get("imageID"),
            "ready": status.get("ready"),
            "restartCount": status.get("restartCount"),
            "started": status.get("started"),
            "state": status.get("state", {}),
        }
        for status in pod.get("status", {}).get("initContainerStatuses", [])
    }
    persistent_claims, ephemeral_claims = pod_volume_claims(pod)
    return {
        "name": pod["metadata"]["name"],
        "uid": pod["metadata"]["uid"],
        "labels": dict(sorted(pod["metadata"].get("labels", {}).items())),
        "owner": owner_reference(pod, "StatefulSet"),
        "phase": pod.get("status", {}).get("phase"),
        "ready": pod_is_ready(pod),
        "nodeName": pod["spec"].get("nodeName"),
        "podIP": pod.get("status", {}).get("podIP"),
        "containers": dict(sorted(status_by_name.items())),
        "initContainers": dict(sorted(init_status_by_name.items())),
        "persistentVolumeClaims": persistent_claims,
        "ephemeralVolumeClaims": ephemeral_claims,
    }


def pvc_contract(pvc: dict[str, Any]) -> dict[str, Any]:
    return {
        "uid": pvc["metadata"]["uid"],
        "podOwner": owner_reference(pvc, "Pod"),
        "volumeName": pvc["spec"].get("volumeName"),
        "storageClassName": pvc["spec"].get("storageClassName"),
        "accessModes": sorted(pvc["spec"].get("accessModes", [])),
        "requestedStorage": pvc["spec"].get("resources", {}).get("requests", {}).get("storage"),
        "capacityStorage": pvc.get("status", {}).get("capacity", {}).get("storage"),
        "volumeMode": pvc["spec"].get("volumeMode"),
        "phase": pvc.get("status", {}).get("phase"),
    }


def assert_s3_ephemeral_claim(
    phase: str,
    workload: str,
    pod: dict[str, Any],
    claims: dict[str, dict[str, Any]],
) -> None:
    expected_name = f"{pod['name']}-s3-credentials"
    require(
        set(claims) == {expected_name},
        f"{phase}: {workload} generic ephemeral PVC inventory changed: {sorted(claims)}",
    )
    require(
        pod["ephemeralVolumeClaims"] == {"s3-credentials": expected_name},
        f"{phase}: {workload} must declare exactly the s3-credentials generic ephemeral volume",
    )
    claim = claims[expected_name]
    owner = claim["podOwner"] or {}
    require(
        owner.get("name") == pod["name"] and owner.get("uid") == pod["uid"],
        f"{phase}: {workload} ephemeral PVC is not owned by the current Pod",
    )
    require(claim["phase"] == "Bound", f"{phase}: {workload} ephemeral PVC is not Bound")
    require(
        claim["requestedStorage"] == "1Mi" and claim["capacityStorage"] == "1Mi",
        f"{phase}: {workload} ephemeral PVC request/capacity is not 1Mi",
    )
    require(
        claim["storageClassName"] == "secrets.kubedoop.dev",
        f"{phase}: {workload} ephemeral PVC storage class changed",
    )
    require(
        claim["accessModes"] == ["ReadWriteOnce"] and claim["volumeMode"] == "Filesystem",
        f"{phase}: {workload} ephemeral PVC access or volume mode changed",
    )


def self_test_ephemeral_pvc_contract() -> None:
    raw_pod = {
        "metadata": {"name": "upgrade-node-default-0", "uid": "pod-uid"},
        "spec": {
            "volumes": [
                {"name": "data", "persistentVolumeClaim": {"claimName": "ordinary-data"}},
                {"name": "s3-credentials", "ephemeral": {"volumeClaimTemplate": {}}},
            ]
        },
    }
    persistent, ephemeral = pod_volume_claims(raw_pod)
    require(persistent == ["ordinary-data"], "ephemeral PVC self-test lost an ordinary PVC")
    require(
        ephemeral == {"s3-credentials": "upgrade-node-default-0-s3-credentials"},
        "ephemeral PVC self-test resolved the generated claim name incorrectly",
    )
    pod = {
        "name": raw_pod["metadata"]["name"],
        "uid": raw_pod["metadata"]["uid"],
        "ephemeralVolumeClaims": ephemeral,
    }
    raw_pvc = {
        "metadata": {
            "uid": "pvc-uid",
            "ownerReferences": [
                {"kind": "Pod", "name": pod["name"], "uid": pod["uid"], "controller": True}
            ],
        },
        "spec": {
            "volumeName": "pv-name",
            "storageClassName": "secrets.kubedoop.dev",
            "accessModes": ["ReadWriteOnce"],
            "resources": {"requests": {"storage": "1Mi"}},
            "volumeMode": "Filesystem",
        },
        "status": {"phase": "Bound", "capacity": {"storage": "1Mi"}},
    }
    claims = {ephemeral["s3-credentials"]: pvc_contract(raw_pvc)}
    assert_s3_ephemeral_claim("self-test", "default", pod, claims)

    wrong_owner = copy.deepcopy(claims)
    wrong_owner[ephemeral["s3-credentials"]]["podOwner"]["uid"] = "stale-pod-uid"
    try:
        assert_s3_ephemeral_claim("self-test", "default", pod, wrong_owner)
    except ContractError:
        return
    raise ContractError("ephemeral PVC self-test accepted a stale Pod owner UID")


def service_account_contract(service_account: dict[str, Any]) -> dict[str, Any]:
    return {
        "uid": service_account["metadata"]["uid"],
        "ownerReferences": service_account["metadata"].get("ownerReferences", []),
        "imagePullSecrets": normalized_named_items(service_account.get("imagePullSecrets", [])),
        "automountServiceAccountToken": service_account.get("automountServiceAccountToken"),
    }


def tracked_inventory(
    resources: dict[tuple[str, str], dict[str, Any]],
    cluster_uid: str,
    pvc_names: set[str],
) -> list[str]:
    keys = set()
    for (kind, name), item in resources.items():
        owners = item.get("metadata", {}).get("ownerReferences", [])
        if any(owner.get("uid") == cluster_uid for owner in owners):
            keys.add(f"{kind}/{name}")
        if kind == "Endpoints" and name == WORKLOAD:
            keys.add(f"{kind}/{name}")
        if kind == "PersistentVolumeClaim" and name in pvc_names:
            keys.add(f"{kind}/{name}")
    return sorted(keys)


def phase_contract(root: pathlib.Path, phase: str) -> dict[str, Any]:
    resources = resource_map(root, phase)
    cluster = load_json(root / f"{phase}-cluster.json")
    cluster_uid = cluster["metadata"]["uid"]
    required = {
        ("StatefulSet", WORKLOAD),
        ("ConfigMap", WORKLOAD),
        ("Service", WORKLOAD),
        ("Service", METRICS_SERVICE),
        ("Endpoints", WORKLOAD),
        ("PodDisruptionBudget", ROLE_PDB),
        ("ServiceAccount", "default"),
        ("StatefulSet", VECTOR_WORKLOAD),
        ("ConfigMap", VECTOR_WORKLOAD),
        ("Service", VECTOR_WORKLOAD),
        ("Service", VECTOR_METRICS_SERVICE),
        ("Endpoints", VECTOR_WORKLOAD),
    }
    missing = required - set(resources)
    require(not missing, f"{phase}: missing resources {sorted(missing)}")

    statefulset = statefulset_contract(resources["StatefulSet", WORKLOAD])
    pods = load_json(root / f"{phase}-pods.json")["items"]
    pod = pod_contract(workload_pod(pods, statefulset["uid"]))
    require(pod["phase"] == "Running" and pod["ready"], f"{phase}: workload Pod is not Ready")

    vector_statefulset = statefulset_contract(resources["StatefulSet", VECTOR_WORKLOAD])
    vector_pod = pod_contract(
        workload_pod(pods, vector_statefulset["uid"], VECTOR_WORKLOAD)
    )
    require(
        vector_pod["phase"] == "Running" and vector_pod["ready"],
        f"{phase}: Vector workload Pod is not Ready",
    )
    require(
        not pod["persistentVolumeClaims"] and not vector_pod["persistentVolumeClaims"],
        f"{phase}: workload unexpectedly uses an ordinary persistentVolumeClaim volume",
    )

    pvc_names = set(pod["persistentVolumeClaims"]) | set(vector_pod["persistentVolumeClaims"])
    pvcs = {
        name: pvc_contract(resources["PersistentVolumeClaim", name])
        for name in sorted(pvc_names)
        if ("PersistentVolumeClaim", name) in resources
    }
    require(set(pvcs) == pvc_names, f"{phase}: a referenced PVC was not captured")

    ephemeral_pvcs: dict[str, dict[str, dict[str, Any]]] = {}
    for workload, workload_pod_contract in (
        ("default", pod),
        ("vector", vector_pod),
    ):
        names = set(workload_pod_contract["ephemeralVolumeClaims"].values())
        claims = {
            name: pvc_contract(resources["PersistentVolumeClaim", name])
            for name in sorted(names)
            if ("PersistentVolumeClaim", name) in resources
        }
        require(set(claims) == names, f"{phase}: {workload} ephemeral PVC was not captured")
        assert_s3_ephemeral_claim(phase, workload, workload_pod_contract, claims)
        ephemeral_pvcs[workload] = claims

    tracked_pvc_names = set(pvc_names)
    # Keep tracking the removed role group's Pod-owned claim even after its Pod
    # disappears, so a leaked generic ephemeral PVC cannot evade the inventory.
    tracked_pvc_names.add(REMOVED_EPHEMERAL_PVC)
    for candidate in pods:
        if not candidate["metadata"]["name"].startswith("upgrade-node-"):
            continue
        persistent_names, ephemeral_names = pod_volume_claims(candidate)
        tracked_pvc_names.update(persistent_names)
        tracked_pvc_names.update(ephemeral_names.values())

    generated_sa = resources.get(("ServiceAccount", GENERATED_SERVICE_ACCOUNT))
    service_accounts = {"default": service_account_contract(resources["ServiceAccount", "default"])}
    if generated_sa is not None:
        service_accounts[GENERATED_SERVICE_ACCOUNT] = service_account_contract(generated_sa)

    return {
        "cluster": {
            "uid": cluster_uid,
            "generation": cluster["metadata"]["generation"],
            "specRoleGroups": sorted(cluster["spec"]["node"]["roleGroups"]),
            "observedGeneration": cluster.get("status", {}).get("observedGeneration"),
            "roleGroups": role_groups(cluster),
            "conditions": conditions(cluster),
        },
        "statefulSet": statefulset,
        "configMap": config_map_contract(resources["ConfigMap", WORKLOAD]),
        "services": {
            WORKLOAD: service_contract(resources["Service", WORKLOAD]),
            METRICS_SERVICE: service_contract(resources["Service", METRICS_SERVICE]),
        },
        "endpoints": endpoint_contract(resources["Endpoints", WORKLOAD]),
        "podDisruptionBudget": pdb_contract(resources["PodDisruptionBudget", ROLE_PDB]),
        "serviceAccounts": service_accounts,
        "pod": pod,
        "persistentVolumeClaims": pvcs,
        "ephemeralPersistentVolumeClaims": ephemeral_pvcs,
        "inventory": tracked_inventory(resources, cluster_uid, tracked_pvc_names),
        "applications": application_ids(root, phase),
        "vectorWorkload": {
            "statefulSet": vector_statefulset,
            "configMap": config_map_contract(resources["ConfigMap", VECTOR_WORKLOAD]),
            "services": {
                VECTOR_WORKLOAD: service_contract(resources["Service", VECTOR_WORKLOAD]),
                VECTOR_METRICS_SERVICE: service_contract(
                    resources["Service", VECTOR_METRICS_SERVICE]
                ),
            },
            "endpoints": endpoint_contract(resources["Endpoints", VECTOR_WORKLOAD]),
            "pod": vector_pod,
        },
    }


def endpoint_port_set(contract: dict[str, Any]) -> set[tuple[str | None, int | None, str]]:
    return {
        (port["name"], port["port"], port["protocol"])
        for port in contract["endpoints"]["ports"]
    }


def assert_healthy_status(phase: str, contract: dict[str, Any]) -> None:
    cluster = contract["cluster"]
    require(
        cluster["observedGeneration"] == cluster["generation"],
        f"{phase}: status.observedGeneration is stale",
    )
    for name, expected in EXPECTED_HEALTHY_CONDITIONS.items():
        condition = cluster["conditions"].get(name, {})
        require(
            condition.get("status") == expected,
            f"{phase}: expected condition {name}={expected}, got {condition.get('status')}",
        )
        require(
            condition.get("observedGeneration") == cluster["generation"],
            f"{phase}: condition {name} has stale observedGeneration",
        )


def self_test_healthy_status_contract() -> None:
    cluster = {
        "generation": 7,
        "observedGeneration": 7,
        "conditions": {
            name: {"status": status, "observedGeneration": 7}
            for name, status in EXPECTED_HEALTHY_CONDITIONS.items()
        },
    }
    assert_healthy_status("self-test", {"cluster": cluster})

    def require_rejected(description: str, candidate: dict[str, Any]) -> None:
        try:
            assert_healthy_status("self-test", {"cluster": candidate})
        except ContractError:
            return
        raise ContractError(f"healthy-status self-test accepted {description}")

    stale_top_level = copy.deepcopy(cluster)
    stale_top_level["observedGeneration"] = 6
    require_rejected("a stale top-level observedGeneration", stale_top_level)
    stale_condition = copy.deepcopy(cluster)
    stale_condition["conditions"]["Available"]["observedGeneration"] = 6
    require_rejected("a stale condition observedGeneration", stale_condition)


def assert_application_continuity(contracts: dict[str, dict[str, Any]]) -> None:
    before = contracts["before"]["applications"]
    after = contracts["after"]["applications"]
    rollback = contracts["rollback"]["applications"]
    before_name = "framework-upgrade-before"
    after_name = "framework-upgrade-after"
    rollback_name = "framework-upgrade-rollback"
    require(before_name in before, "baseline application is missing")
    require(after.get(before_name) == before[before_name], "baseline application ID changed after upgrade")
    require(rollback.get(before_name) == before[before_name], "baseline application ID changed after rollback")
    require(after_name in after, "post-upgrade application is missing")
    require(rollback.get(after_name) == after[after_name], "post-upgrade application ID changed after rollback")
    require(rollback_name in rollback, "post-rollback application is missing")


def assert_health_probe(root: pathlib.Path, stable_statefulset_uid: str, cluster_uid: str) -> dict[str, Any]:
    cluster = load_json(root / "health-failure-cluster.json")
    statefulset = load_json(root / "health-failure-statefulset.json")
    pods = load_json(root / "health-failure-pods.json")["items"]
    require(cluster["metadata"]["uid"] == cluster_uid, "health probe replaced the SparkHistoryServer")
    require(statefulset["metadata"]["uid"] == stable_statefulset_uid, "health probe replaced the StatefulSet")
    degraded = conditions(cluster).get("Degraded", {})
    require(degraded.get("status") == "True", "health probe did not set Degraded=True")
    require(degraded.get("reason") == "PodFailure", "health probe did not report reason PodFailure")
    pod = workload_pod(pods, stable_statefulset_uid)
    container_images = [container.get("image", "") for container in pod["spec"].get("containers", [])]
    require(
        "localhost/framework-upgrade-missing:never" in container_images,
        "health probe snapshot did not capture the intentionally missing image",
    )
    require(not pod_is_ready(pod), "health probe Pod unexpectedly remained Ready")
    return {
        "clusterConditions": conditions(cluster),
        "statefulSetUID": statefulset["metadata"]["uid"],
        "pod": pod_contract(pod),
    }


def require_log_volume(phase: str, statefulset: dict[str, Any]) -> None:
    volume = statefulset["logVolume"]
    require(volume is not None, f"{phase}: Vector workload has no shared log volume")
    empty_dir = volume.get("emptyDir") or {}
    require(empty_dir.get("sizeLimit") == "30Mi", f"{phase}: Vector log volume is not 30Mi")
    require(not empty_dir.get("medium"), f"{phase}: Vector log volume unexpectedly changed medium")
    require(
        "log" in statefulset["template"]["allNodeVolumeMountNames"],
        f"{phase}: node container lost the Vector log mount",
    )


def assert_vector_workload_compatibility(
    contracts: dict[str, dict[str, Any]],
) -> None:
    vector = {phase: contract["vectorWorkload"] for phase, contract in contracts.items()}
    before, after, rollback = (vector[phase] for phase in ("before", "after", "rollback"))

    statefulset_uid = before["statefulSet"]["uid"]
    require(
        statefulset_uid
        == after["statefulSet"]["uid"]
        == rollback["statefulSet"]["uid"],
        "Vector StatefulSet UID changed across the controller migration",
    )
    require(
        before["statefulSet"]["serviceName"] == VECTOR_WORKLOAD,
        "legacy Vector StatefulSet serviceName changed",
    )
    baseline_behavior = statefulset_behavior(before["statefulSet"])
    require(
        statefulset_behavior(after["statefulSet"]) == baseline_behavior,
        "v0.13 changed the Vector group's main workload behavior",
    )
    require(
        statefulset_behavior(rollback["statefulSet"]) == baseline_behavior,
        "rollback did not restore the Vector group's main workload behavior",
    )
    require(
        rollback["statefulSet"]["template"]["volumes"]
        == before["statefulSet"]["template"]["volumes"],
        "rollback did not restore the Vector group's auxiliary volumes",
    )
    for phase, contract in vector.items():
        require_log_volume(phase, contract["statefulSet"])
    require(
        rollback["statefulSet"]["logVolume"] == before["statefulSet"]["logVolume"],
        "rollback did not restore the Vector group's exact log volume",
    )

    assert_config_map_compatibility(
        before["configMap"],
        after["configMap"],
        rollback["configMap"],
        vector_enabled_after=True,
    )

    before_sidecar = before["statefulSet"]["template"]["vectorSidecar"]
    after_sidecar = after["statefulSet"]["template"]["vectorSidecar"]
    rollback_sidecar = rollback["statefulSet"]["template"]["vectorSidecar"]
    require(before_sidecar is not None, "baseline Vector group has no Vector agent")
    require(after_sidecar is not None, "v0.13 Vector group has no Vector agent")
    require(rollback_sidecar == before_sidecar, "rollback did not restore the legacy Vector agent")
    require(
        before_sidecar["location"] == "container" and before_sidecar["restartPolicy"] is None,
        "baseline Vector agent is not a regular container",
    )
    require(
        after_sidecar["location"] == "initContainer"
        and after_sidecar["restartPolicy"] == "Always",
        "v0.13 Vector agent is not a restartable native init container",
    )
    require(
        after_sidecar["securityContext"].get("runAsUser") == 1001
        and after_sidecar["securityContext"].get("runAsGroup") == 1001
        and after_sidecar["securityContext"].get("runAsNonRoot") is True,
        "v0.13 Vector agent does not have the executable kubedoop non-root identity",
    )
    require(after_sidecar["image"] == before_sidecar["image"], "Vector product image changed")

    before_command = " ".join(before_sidecar["command"] + before_sidecar["args"])
    after_command = " ".join(after_sidecar["command"] + after_sidecar["args"])
    require(
        "vector --config /kubedoop/config/vector.yaml" in before_command,
        "baseline Vector command lost its config file",
    )
    require(
        "exec vector --config /etc/vector/vector.yaml" in after_command,
        "v0.13 Vector command lost its native-sidecar config file",
    )

    before_mounts = {
        mount["name"]: mount.get("mountPath") for mount in before_sidecar["volumeMounts"]
    }
    after_mounts = {
        mount["name"]: mount.get("mountPath") for mount in after_sidecar["volumeMounts"]
    }
    require(
        before_mounts
        == {
            "config": "/kubedoop/config/",
            "log": "/kubedoop/log/",
            "vector-data": "/kubedoop/vector/var",
        },
        "baseline Vector volume mounts changed",
    )
    require(
        after_mounts
        == {
            "vector-config": "/etc/vector",
            "log": "/kubedoop/log/",
            "vector-data": "/kubedoop/vector/var",
        },
        "v0.13 Vector native-sidecar volume mounts changed",
    )
    require(
        before_sidecar["readinessProbe"] is not None
        and before_sidecar["readinessProbe"].get("httpGet")
        == {"path": "/health", "port": 8686, "scheme": "HTTP"},
        "baseline Vector readiness endpoint changed",
    )
    require(after_sidecar["readinessProbe"] is None, "v0.13 Vector sidecar gates Pod readiness")
    require(
        after_sidecar["livenessProbe"] is not None
        and after_sidecar["livenessProbe"].get("httpGet")
        == {"path": "/metrics", "port": 9598, "scheme": "HTTP"},
        "v0.13 Vector liveness endpoint changed",
    )
    require(
        before_sidecar["ports"]
        == [{"containerPort": 8686, "name": "vector", "protocol": "TCP"}],
        "baseline Vector API port changed",
    )
    require(
        after_sidecar["ports"]
        == [{"containerPort": 9598, "name": "vector-metrics", "protocol": "TCP"}],
        "v0.13 Vector metrics port changed",
    )
    after_mount_specs = {mount["name"]: mount for mount in after_sidecar["volumeMounts"]}
    require(
        after_mount_specs["vector-config"].get("readOnly") is True,
        "v0.13 Vector config mount is not read-only",
    )
    require(
        not after_mount_specs["log"].get("readOnly", False)
        and not after_mount_specs["vector-data"].get("readOnly", False),
        "v0.13 Vector writable volumes became read-only",
    )

    require(
        "vector" in before["pod"]["containers"]
        and "vector" not in before["pod"]["initContainers"],
        "baseline Pod did not run Vector as a regular container",
    )
    require(
        "vector" not in after["pod"]["containers"]
        and "vector" in after["pod"]["initContainers"],
        "v0.13 Pod did not run Vector as a native init container",
    )
    vector_status = after["pod"]["initContainers"]["vector"]
    require(
        vector_status.get("started") is True
        and "running" in vector_status.get("state", {}),
        "v0.13 native Vector sidecar is not running",
    )
    require(
        "vector" in rollback["pod"]["containers"]
        and "vector" not in rollback["pod"]["initContainers"],
        "rollback did not restore Vector as a regular container",
    )

    for service_name, metrics_service_name in (
        (VECTOR_WORKLOAD, False),
        (VECTOR_METRICS_SERVICE, True),
    ):
        baseline_service = before["services"][service_name]
        upgraded_service = after["services"][service_name]
        rolled_back_service = rollback["services"][service_name]
        require(
            baseline_service["uid"]
            == upgraded_service["uid"]
            == rolled_back_service["uid"],
            f"Vector Service {service_name} UID changed",
        )
        require(
            baseline_service["frameworkRoleGroupMarker"] is None
            and upgraded_service["frameworkRoleGroupMarker"] == "true"
            and rolled_back_service["frameworkRoleGroupMarker"] is None,
            f"Vector Service {service_name} role-group marker lifecycle changed",
        )
        if metrics_service_name:
            require(
                without_metrics_slot_label(baseline_service["stableLabels"])
                == without_metrics_slot_label(upgraded_service["stableLabels"])
                == without_metrics_slot_label(rolled_back_service["stableLabels"]),
                f"Vector Service {service_name} stable labels changed",
            )
            require(
                baseline_service["stableLabels"].get(METRICS_SLOT_LABEL) is None
                and upgraded_service["stableLabels"].get(METRICS_SLOT_LABEL) == "true"
                and rolled_back_service["stableLabels"].get(METRICS_SLOT_LABEL) in (None, "true"),
                "Vector metrics Service slot-label allowlist mismatch",
            )
        else:
            require(
                baseline_service["stableLabels"]
                == upgraded_service["stableLabels"]
                == rolled_back_service["stableLabels"],
                f"Vector Service {service_name} stable labels changed",
            )
        require(
            baseline_service["annotations"]
            == upgraded_service["annotations"]
            == rolled_back_service["annotations"],
            f"Vector Service {service_name} annotations changed",
        )
        require(
            baseline_service["applyBookkeeping"]
            == upgraded_service["applyBookkeeping"]
            == rolled_back_service["applyBookkeeping"]
            and baseline_service["applyBookkeeping"] is not None,
            f"Vector Service {service_name} apply bookkeeping changed",
        )
        require(
            baseline_service["baseSpec"]
            == upgraded_service["baseSpec"]
            == rolled_back_service["baseSpec"],
            f"Vector Service {service_name} base spec changed",
        )
        require(
            baseline_service["selector"]
            == upgraded_service["selector"]
            == rolled_back_service["selector"],
            f"Vector Service {service_name} selector changed",
        )
        require(
            rolled_back_service["ports"] == baseline_service["ports"],
            f"Vector Service {service_name} ports were not restored",
        )

    before_client_ports = before["services"][VECTOR_WORKLOAD]["ports"]
    after_client_ports = after["services"][VECTOR_WORKLOAD]["ports"]
    require(
        [port for port in after_client_ports if port not in before_client_ports] == [],
        "v0.13 added an unexpected Vector client Service port",
    )
    removed_ports = [port for port in before_client_ports if port not in after_client_ports]
    require(
        len(removed_ports) == 1
        and removed_ports[0]["name"] == "oidc"
        and removed_ports[0]["port"] == 4180,
        f"Vector client Service port allowlist mismatch: removed={removed_ports}",
    )
    require(
        after["services"][VECTOR_METRICS_SERVICE]["ports"]
        == before["services"][VECTOR_METRICS_SERVICE]["ports"],
        "v0.13 changed the Vector metrics Service ports",
    )

    for phase, contract in vector.items():
        selector = contract["statefulSet"]["selector"]["matchLabels"]
        require(
            contract["services"][VECTOR_WORKLOAD]["selector"] == selector,
            f"{phase}: Vector client Service selector does not match StatefulSet",
        )
        require(
            contract["services"][VECTOR_METRICS_SERVICE]["selector"] == selector,
            f"{phase}: Vector metrics Service selector does not match StatefulSet",
        )
        metrics = contract["services"][VECTOR_METRICS_SERVICE]
        require(
            all(metrics["stableLabels"].get(key) == value for key, value in METRICS_PROMETHEUS_LABELS.items()),
            f"{phase}: Vector metrics Service lost its Prometheus scrape label",
        )
        require(
            all(metrics["annotations"].get(key) == value for key, value in METRICS_PROMETHEUS_ANNOTATIONS.items()),
            f"{phase}: Vector metrics Service Prometheus annotations changed",
        )
        require(contract["endpoints"]["addresses"], f"{phase}: Vector client Service has no endpoint")
        require(
            contract["endpoints"]["addresses"][0]["targetUID"] == contract["pod"]["uid"],
            f"{phase}: Vector endpoint does not target the captured Pod",
        )
        require(
            ("http", 18080, "TCP") in endpoint_port_set(contract),
            f"{phase}: Vector History Server HTTP endpoint changed",
        )
    require(
        before["endpoints"]["uid"]
        == after["endpoints"]["uid"]
        == rollback["endpoints"]["uid"],
        "Vector client Endpoints UID changed",
    )
    require(
        endpoint_port_set(after)
        == endpoint_port_set(before) - {("oidc", 4180, "TCP")},
        "Vector endpoint OIDC-port allowlist mismatch",
    )
    require(
        endpoint_port_set(rollback) == endpoint_port_set(before),
        "rollback did not restore Vector endpoint ports",
    )


def main(root: pathlib.Path) -> None:
    phases = ("before", "after", "rollback")
    contracts = {phase: phase_contract(root, phase) for phase in phases}
    before, after, rollback = (contracts[phase] for phase in phases)

    cluster_uid = before["cluster"]["uid"]
    require(cluster_uid == after["cluster"]["uid"] == rollback["cluster"]["uid"], "SparkHistoryServer UID changed")
    require(before["cluster"]["specRoleGroups"] == ["default", "removed", "vector"], "baseline role-group fixture is incomplete")
    require(after["cluster"]["specRoleGroups"] == ["default", "vector"], "removed role group remains in the v0.13 spec")
    require(rollback["cluster"]["specRoleGroups"] == ["default", "vector"], "rollback recreated the removed role group in spec")
    require(before["cluster"]["roleGroups"] in (None, {}), "v0.12 unexpectedly wrote a role-group ledger")
    require(after["cluster"]["roleGroups"] == {"node": ["default", "vector"]}, "v0.13 role-group ledger is incomplete")
    require(rollback["cluster"]["roleGroups"] == after["cluster"]["roleGroups"], "rollback lost the v0.13 role-group ledger")
    require(not before["cluster"]["conditions"], "v0.12 unexpectedly wrote generic status conditions")
    assert_healthy_status("after", after)
    assert_healthy_status("rollback", rollback)

    statefulset_uid = before["statefulSet"]["uid"]
    require(
        statefulset_uid == after["statefulSet"]["uid"] == rollback["statefulSet"]["uid"],
        "StatefulSet UID changed across the controller migration",
    )
    require(before["statefulSet"]["serviceName"] == WORKLOAD, "legacy StatefulSet serviceName changed")
    require(before["statefulSet"]["podManagementPolicy"] == "OrderedReady", "legacy podManagementPolicy changed")
    baseline_behavior = statefulset_behavior(before["statefulSet"])
    require(statefulset_behavior(after["statefulSet"]) == baseline_behavior, "v0.13 changed critical StatefulSet behavior")
    require(statefulset_behavior(rollback["statefulSet"]) == baseline_behavior, "rollback did not restore StatefulSet behavior")
    require("log" in before["statefulSet"]["template"]["allVolumeNames"], "baseline legacy log volume is missing")
    require("log" in before["statefulSet"]["template"]["allNodeVolumeMountNames"], "baseline legacy log mount is missing")
    require("log" not in after["statefulSet"]["template"]["allVolumeNames"], "v0.13 retained the unused legacy log volume")
    require("log" not in after["statefulSet"]["template"]["allNodeVolumeMountNames"], "v0.13 retained the unused legacy log mount")
    require("log" in rollback["statefulSet"]["template"]["allVolumeNames"], "rollback did not restore the legacy log volume")
    require("log" in rollback["statefulSet"]["template"]["allNodeVolumeMountNames"], "rollback did not restore the legacy log mount")
    require_log_volume("before default", before["statefulSet"])
    require(after["statefulSet"]["logVolume"] is None, "v0.13 retained the default group's log volume")
    require_log_volume("rollback default", rollback["statefulSet"])
    require(
        rollback["statefulSet"]["logVolume"] == before["statefulSet"]["logVolume"],
        "rollback did not restore the default group's exact log volume",
    )
    require(
        before["statefulSet"]["template"]["vectorSidecar"] is not None,
        "v0.12 did not preserve its aggregator-driven Vector enablement",
    )
    require(
        before["statefulSet"]["template"]["vectorSidecar"]["location"] == "container",
        "v0.12 default-group Vector agent is not a regular container",
    )
    require(
        after["statefulSet"]["template"]["vectorSidecar"] is None,
        "v0.13 ignored logging.enableVectorAgent=false for the default group",
    )
    require(
        rollback["statefulSet"]["template"]["vectorSidecar"]
        == before["statefulSet"]["template"]["vectorSidecar"],
        "rollback did not restore the default group's legacy Vector agent",
    )

    assert_config_map_compatibility(
        before["configMap"],
        after["configMap"],
        rollback["configMap"],
        vector_enabled_after=False,
    )
    assert_vector_workload_compatibility(contracts)

    for service_name, baseline_service in before["services"].items():
        upgraded_service = after["services"][service_name]
        rolled_back_service = rollback["services"][service_name]
        require(
            baseline_service["uid"] == upgraded_service["uid"] == rolled_back_service["uid"],
            f"Service {service_name} UID changed",
        )
        require(
            baseline_service["frameworkRoleGroupMarker"] is None
            and upgraded_service["frameworkRoleGroupMarker"] == "true"
            and rolled_back_service["frameworkRoleGroupMarker"] is None,
            f"Service {service_name} role-group marker lifecycle changed",
        )
        if service_name == METRICS_SERVICE:
            require(
                without_metrics_slot_label(baseline_service["stableLabels"])
                == without_metrics_slot_label(upgraded_service["stableLabels"])
                == without_metrics_slot_label(rolled_back_service["stableLabels"]),
                f"Service {service_name} stable labels changed",
            )
            require(
                baseline_service["stableLabels"].get(METRICS_SLOT_LABEL) is None
                and upgraded_service["stableLabels"].get(METRICS_SLOT_LABEL) == "true"
                and rolled_back_service["stableLabels"].get(METRICS_SLOT_LABEL) in (None, "true"),
                "metrics Service slot-label allowlist mismatch",
            )
        else:
            require(
                baseline_service["stableLabels"]
                == upgraded_service["stableLabels"]
                == rolled_back_service["stableLabels"],
                f"Service {service_name} stable labels changed",
            )
            require(
                baseline_service["stableLabels"].get(METRICS_SLOT_LABEL) is None,
                "client Service was mislabeled as the metrics slot",
            )
        require(
            baseline_service["annotations"]
            == upgraded_service["annotations"]
            == rolled_back_service["annotations"],
            f"Service {service_name} annotations changed",
        )
        require(
            baseline_service["applyBookkeeping"]
            == upgraded_service["applyBookkeeping"]
            == rolled_back_service["applyBookkeeping"]
            and baseline_service["applyBookkeeping"] is not None,
            f"Service {service_name} apply bookkeeping changed",
        )
        require(baseline_service["baseSpec"] == upgraded_service["baseSpec"] == rolled_back_service["baseSpec"], f"Service {service_name} base spec changed")
        require(baseline_service["selector"] == upgraded_service["selector"] == rolled_back_service["selector"], f"Service {service_name} selector changed")
        require(rolled_back_service["ports"] == baseline_service["ports"], f"Service {service_name} ports were not restored")

    before_client_ports = before["services"][WORKLOAD]["ports"]
    after_client_ports = after["services"][WORKLOAD]["ports"]
    removed_ports = [port for port in before_client_ports if port not in after_client_ports]
    added_ports = [port for port in after_client_ports if port not in before_client_ports]
    require(not added_ports, f"v0.13 added unexpected client Service ports: {added_ports}")
    require(
        len(removed_ports) == 1
        and removed_ports[0]["name"] == "oidc"
        and removed_ports[0]["port"] == 4180,
        f"v0.13 client Service port allowlist mismatch: removed={removed_ports}",
    )
    require(
        after["services"][METRICS_SERVICE]["ports"] == before["services"][METRICS_SERVICE]["ports"],
        "v0.13 changed the metrics Service ports",
    )
    for phase, contract in contracts.items():
        metrics_service = contract["services"][METRICS_SERVICE]
        require(
            all(metrics_service["stableLabels"].get(key) == value for key, value in METRICS_PROMETHEUS_LABELS.items()),
            f"{phase}: metrics Service lost its Prometheus scrape label",
        )
        require(
            all(
                metrics_service["annotations"].get(key) == value
                for key, value in METRICS_PROMETHEUS_ANNOTATIONS.items()
            ),
            f"{phase}: metrics Service Prometheus annotations changed",
        )


    for phase, contract in contracts.items():
        selector = contract["statefulSet"]["selector"]["matchLabels"]
        require(contract["services"][WORKLOAD]["selector"] == selector, f"{phase}: client Service selector does not match StatefulSet")
        require(contract["services"][METRICS_SERVICE]["selector"] == selector, f"{phase}: metrics Service selector does not match StatefulSet")
        require(contract["endpoints"]["addresses"], f"{phase}: client Service has no ready endpoint")
        require(("http", 18080, "TCP") in endpoint_port_set(contract), f"{phase}: History Server HTTP endpoint changed")
        require(contract["endpoints"]["addresses"][0]["targetUID"] == contract["pod"]["uid"], f"{phase}: Endpoint does not target the captured Pod")
    require(
        before["endpoints"]["uid"] == after["endpoints"]["uid"] == rollback["endpoints"]["uid"],
        "client Endpoints UID changed",
    )
    require(endpoint_port_set(after) == endpoint_port_set(before) - {("oidc", 4180, "TCP")}, "endpoint OIDC-port allowlist mismatch")
    require(endpoint_port_set(rollback) == endpoint_port_set(before), "rollback did not restore endpoint ports")

    pdb_uid = before["podDisruptionBudget"]["uid"]
    require(pdb_uid == after["podDisruptionBudget"]["uid"] == rollback["podDisruptionBudget"]["uid"], "PDB UID changed")
    require(not before["podDisruptionBudget"]["ownerReferences"], "baseline PDB unexpectedly has an owner")
    require(
        has_exact_cluster_controller_owner(
            after["podDisruptionBudget"]["ownerReferences"], "upgrade", cluster_uid
        ),
        "v0.13 did not set the exact legacy PDB controller owner",
    )
    require(
        rollback["podDisruptionBudget"]["ownerReferences"] == after["podDisruptionBudget"]["ownerReferences"],
        "controller rollback changed the adopted PDB owner",
    )
    for phase, contract in contracts.items():
        pdb = contract["podDisruptionBudget"]
        require(pdb["maxUnavailable"] == 1 and pdb["minAvailable"] is None, f"{phase}: PDB availability contract changed")
        require(selector_matches(pdb["selector"], contract["pod"]["labels"]), f"{phase}: PDB does not select the workload")
        require(pdb_status_is_current(pdb), f"{phase}: PDB status.observedGeneration is stale")
        expected_pods = 3 if phase == "before" else 2
        require(
            pdb_counts_are_healthy(pdb, expected_pods),
            f"{phase}: PDB health/disruption counts do not cover {expected_pods} healthy Pod(s)",
        )
    require(rollback["podDisruptionBudget"]["selector"] == before["podDisruptionBudget"]["selector"], "rollback did not restore the v0.12 PDB selector")
    require(after["podDisruptionBudget"]["selector"] != before["podDisruptionBudget"]["selector"], "v0.13 did not apply the cross-generation PDB selector")
    after_pdb_labels = after["podDisruptionBudget"]["selector"].get("matchLabels", {})
    require(after_pdb_labels.get("app.kubernetes.io/instance") == "upgrade", "v0.13 PDB lacks stable instance label")
    require(after_pdb_labels.get("app.kubernetes.io/component") == "node", "v0.13 PDB lacks stable component label")

    default_sa_uid = before["serviceAccounts"]["default"]["uid"]
    require(default_sa_uid == after["serviceAccounts"]["default"]["uid"] == rollback["serviceAccounts"]["default"]["uid"], "default ServiceAccount UID changed")
    require(set(before["serviceAccounts"]) == {"default"}, "baseline unexpectedly owns a generated ServiceAccount")
    require(set(after["serviceAccounts"]) == {"default", GENERATED_SERVICE_ACCOUNT}, "v0.13 generated ServiceAccount allowlist mismatch")
    require(set(rollback["serviceAccounts"]) == set(after["serviceAccounts"]), "rollback changed generated ServiceAccount inventory")
    generated_sa = after["serviceAccounts"][GENERATED_SERVICE_ACCOUNT]
    require(
        has_exact_cluster_controller_owner(generated_sa["ownerReferences"], "upgrade", cluster_uid),
        "generated ServiceAccount does not have the exact SparkHistoryServer controller owner",
    )
    require(
        rollback["serviceAccounts"][GENERATED_SERVICE_ACCOUNT]["uid"] == generated_sa["uid"],
        "rollback replaced the generated ServiceAccount",
    )
    require(
        has_exact_cluster_controller_owner(
            rollback["serviceAccounts"][GENERATED_SERVICE_ACCOUNT]["ownerReferences"],
            "upgrade",
            cluster_uid,
        ),
        "rollback changed the generated ServiceAccount controller owner",
    )
    for phase, contract in contracts.items():
        require(contract["statefulSet"]["template"]["serviceAccountName"] == "default", f"{phase}: adopted StatefulSet service account changed")

    pod_names = {contract["pod"]["name"] for contract in contracts.values()}
    require(pod_names == {f"{WORKLOAD}-0"}, f"workload Pod name changed: {sorted(pod_names)}")
    for phase, contract in contracts.items():
        require(contract["pod"]["owner"]["uid"] == statefulset_uid, f"{phase}: Pod owner UID changed")
        require(
            not contract["persistentVolumeClaims"],
            f"{phase}: ordinary persistent PVCs are outside this acceptance fixture",
        )
    pvc_names = {tuple(sorted(contract["persistentVolumeClaims"])) for contract in contracts.values()}
    require(len(pvc_names) == 1, "workload PVC inventory changed across migration")
    for name in before["persistentVolumeClaims"]:
        require(
            before["persistentVolumeClaims"][name]["uid"]
            == after["persistentVolumeClaims"][name]["uid"]
            == rollback["persistentVolumeClaims"][name]["uid"],
            f"PVC {name} UID changed",
        )

    before_inventory = set(before["inventory"])
    after_inventory = set(after["inventory"])
    rollback_inventory = set(rollback["inventory"])
    removed_owned_inventory = {
        f"ConfigMap/{REMOVED_WORKLOAD}",
        f"Service/{REMOVED_WORKLOAD}",
        f"Service/{REMOVED_WORKLOAD}-metrics",
        f"StatefulSet/{REMOVED_WORKLOAD}",
    }
    removed_ephemeral_pvc = f"PersistentVolumeClaim/{REMOVED_EPHEMERAL_PVC}"
    removed_inventory = removed_owned_inventory | {removed_ephemeral_pvc}
    expected_adopted_inventory = {
        f"PodDisruptionBudget/{ROLE_PDB}",
        f"ServiceAccount/{GENERATED_SERVICE_ACCOUNT}",
    }
    require(after_inventory - before_inventory == expected_adopted_inventory, f"unexpected v0.13 resource inventory delta: {sorted(after_inventory - before_inventory)}")
    require(before_inventory - after_inventory == removed_inventory, f"v0.13 cleanup delta mismatch: {sorted(before_inventory - after_inventory)}")
    require(rollback_inventory == after_inventory, "controller rollback changed tracked resource inventory")

    orphan_resources = load_json(root / "orphan-pre-upgrade-resources.json")["items"]
    orphan_cluster = load_json(root / "orphan-pre-upgrade-cluster.json")
    require(orphan_cluster["metadata"]["uid"] == cluster_uid, "orphan pre-upgrade snapshot replaced the SparkHistoryServer")
    require(
        sorted(orphan_cluster["spec"]["node"]["roleGroups"]) == ["default", "vector"],
        "removed role group was still desired in the orphan pre-upgrade snapshot",
    )
    orphan_keys = {f"{item['kind']}/{item['metadata']['name']}" for item in orphan_resources}
    require(
        orphan_keys == removed_owned_inventory,
        f"removed role-group direct-owner pre-upgrade evidence is incomplete: {sorted(orphan_keys)}",
    )
    require(
        all(any(reference.get("uid") == cluster_uid for reference in item["metadata"].get("ownerReferences", [])) for item in orphan_resources),
        "removed role-group pre-upgrade resources are not owned by the SparkHistoryServer",
    )
    orphan_identity = {
        f"{item['kind']}/{item['metadata']['name']}": item["metadata"]["uid"]
        for item in orphan_resources
    }

    health_probe = assert_health_probe(root, statefulset_uid, cluster_uid)
    assert_application_continuity(contracts)

    output = {
        "expectedDifferences": EXPECTED_DIFFERENCES,
        "phases": contracts,
        "healthProbe": health_probe,
        "orphanCleanup": {
            "preUpgradeResourceUIDs": dict(sorted(orphan_identity.items())),
            "removedEphemeralPersistentVolumeClaim": removed_ephemeral_pvc,
            "postUpgradeResourcesPresent": [],
        },
        "identityTransitions": {
            "statefulSetUIDs": {phase: contracts[phase]["statefulSet"]["uid"] for phase in phases},
            "statefulSetRevisions": {phase: contracts[phase]["statefulSet"]["status"] for phase in phases},
            "podUIDs": {phase: contracts[phase]["pod"]["uid"] for phase in phases},
            "persistentVolumeClaims": {phase: contracts[phase]["persistentVolumeClaims"] for phase in phases},
            "ephemeralPersistentVolumeClaims": {
                phase: contracts[phase]["ephemeralPersistentVolumeClaims"]
                for phase in phases
            },
            "vectorStatefulSetUIDs": {
                phase: contracts[phase]["vectorWorkload"]["statefulSet"]["uid"]
                for phase in phases
            },
            "vectorPodUIDs": {
                phase: contracts[phase]["vectorWorkload"]["pod"]["uid"]
                for phase in phases
            },
        },
    }
    (root / "contracts.json").write_text(json.dumps(output, indent=2, sort_keys=True) + "\n")
    (root / "expected-differences.json").write_text(json.dumps(EXPECTED_DIFFERENCES, indent=2) + "\n")

    before_lines = json.dumps(before, indent=2, sort_keys=True).splitlines(True)
    for phase in ("after", "rollback"):
        candidate_lines = json.dumps(contracts[phase], indent=2, sort_keys=True).splitlines(True)
        diff = difflib.unified_diff(
            before_lines,
            candidate_lines,
            fromfile="before-contract",
            tofile=f"{phase}-contract",
        )
        (root / f"{phase}-contract.diff").write_text("".join(diff))

    print(
        "Upgrade contracts passed: stable StatefulSet/ConfigMap/Service/PDB identity, "
        "equivalent workload behavior, legacy-Pod health reporting, retained status, "
        "and continuous Spark event history"
    )


if __name__ == "__main__":
    if not __debug__:
        raise SystemExit("optimized Python is unsupported for acceptance validation")
    try:
        require(len(sys.argv) == 2, "usage: compare-framework-upgrade.py EVIDENCE_DIR")
        self_test_cluster_controller_owner_contract()
        self_test_log4j2_file_allowlist()
        self_test_vector_yaml_contract()
        self_test_service_metadata_contract()
        self_test_selector_matches()
        self_test_pdb_status_contract()
        self_test_ephemeral_pvc_contract()
        self_test_healthy_status_contract()
        main(pathlib.Path(sys.argv[1]))
    except (ContractError, KeyError, TypeError, ValueError) as error:
        print(f"upgrade contract failed: {error}", file=sys.stderr)
        raise SystemExit(1) from error
