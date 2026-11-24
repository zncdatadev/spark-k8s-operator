/*
Copyright 2026 zncdatadev.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package historyserver

import (
	"fmt"
	"strings"

	"github.com/zncdatadev/operator-go/pkg/reconciler"
	corev1 "k8s.io/api/core/v1"
)

const legacyLog4j2DefaultLevel = "INFO"

// preserveLegacyGeneratedLog4j2 keeps the observable logging defaults from the v0.12
// controller while still using operator-go's v0.13 renderer. User-provided
// log4j2.properties remains untouched and retains highest precedence.
func preserveLegacyGeneratedLog4j2(
	configMap *corev1.ConfigMap,
	buildCtx *reconciler.RoleGroupBuildContext,
) error {
	if buildCtx != nil && buildCtx.MergedConfig != nil {
		if _, overridden := buildCtx.MergedConfig.ConfigFiles[LogConfigFileName]; overridden {
			return nil
		}
	}
	if configMap == nil {
		return fmt.Errorf("framework returned no ConfigMap for generated logging configuration")
	}
	content, exists := configMap.Data[LogConfigFileName]
	if !exists {
		return fmt.Errorf("framework returned no generated %s", LogConfigFileName)
	}

	compatible, err := preserveLegacyLog4j2Defaults(content)
	if err != nil {
		return fmt.Errorf("preserving legacy %s defaults: %w", LogConfigFileName, err)
	}
	configMap.Data[LogConfigFileName] = compatible
	return nil
}

func preserveLegacyLog4j2Defaults(content string) (string, error) {
	properties, err := generatedJavaProperties(content)
	if err != nil {
		return "", err
	}
	if properties["appender.console.type"] != "Console" || properties["appender.console.name"] != "STDOUT" {
		return "", fmt.Errorf("unexpected generated console appender")
	}

	consoleProperties := make([]string, 0, 3)
	if target, exists := properties["appender.console.target"]; exists {
		if target != "SYSTEM_ERR" {
			return "", fmt.Errorf("unexpected generated console target %q", target)
		}
	} else {
		consoleProperties = append(consoleProperties, "appender.console.target=SYSTEM_ERR")
	}
	consoleProperties, err = ensureThresholdDefaults(
		properties,
		"appender.console.filter.threshold",
		consoleProperties,
	)
	if err != nil {
		return "", err
	}
	content, err = insertPropertiesAfter(content, "appender.console.name", consoleProperties)
	if err != nil {
		return "", err
	}

	fileType, hasFile := properties["appender.file.type"]
	if !hasFile {
		return content, nil
	}
	if fileType != "RollingFile" || properties["appender.file.name"] != "FILE" {
		return "", fmt.Errorf("unexpected generated file appender")
	}
	fileProperties, err := ensureThresholdDefaults(
		properties,
		"appender.file.filter.threshold",
		nil,
	)
	if err != nil {
		return "", err
	}
	content, err = insertPropertiesAfter(content, "appender.file.name", fileProperties)
	if err != nil {
		return "", err
	}

	maxFileSize := properties["appender.file.policies.size.size"]
	switch maxFileSize {
	case "5MB":
		content, err = replacePropertyValue(content, "appender.file.policies.size.size", "10MB")
		if err != nil {
			return "", err
		}
	case "10MB":
		// Already compatible, for example after a future framework gains a product override.
	default:
		return "", fmt.Errorf("unexpected generated file rollover size %q", maxFileSize)
	}
	return content, nil
}

func ensureThresholdDefaults(
	properties map[string]string,
	prefix string,
	additions []string,
) ([]string, error) {
	typeValue, hasType := properties[prefix+".type"]
	level, hasLevel := properties[prefix+".level"]
	if hasType != hasLevel {
		return nil, fmt.Errorf("generated threshold %s is incomplete", prefix)
	}
	if hasType {
		if typeValue != "ThresholdFilter" || level == "" {
			return nil, fmt.Errorf("unexpected generated threshold %s", prefix)
		}
		return additions, nil
	}
	return append(additions,
		prefix+".type=ThresholdFilter",
		prefix+".level="+legacyLog4j2DefaultLevel,
	), nil
}

func generatedJavaProperties(content string) (map[string]string, error) {
	properties := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "!") {
			continue
		}
		key, value, found := strings.Cut(trimmed, "=")
		if !found {
			return nil, fmt.Errorf("generated property has no '=' separator: %q", line)
		}
		key = strings.TrimSpace(key)
		if _, duplicate := properties[key]; duplicate {
			return nil, fmt.Errorf("generated property %q is duplicated", key)
		}
		properties[key] = strings.TrimSpace(value)
	}
	return properties, nil
}

func insertPropertiesAfter(content, anchor string, additions []string) (string, error) {
	if len(additions) == 0 {
		return content, nil
	}
	lines := strings.Split(content, "\n")
	for index, line := range lines {
		key, _, found := strings.Cut(strings.TrimSpace(line), "=")
		if found && strings.TrimSpace(key) == anchor {
			updated := make([]string, 0, len(lines)+len(additions))
			updated = append(updated, lines[:index+1]...)
			updated = append(updated, additions...)
			updated = append(updated, lines[index+1:]...)
			return strings.Join(updated, "\n"), nil
		}
	}
	return "", fmt.Errorf("generated property %q is missing", anchor)
}

func replacePropertyValue(content, key, value string) (string, error) {
	lines := strings.Split(content, "\n")
	for index, line := range lines {
		candidate, _, found := strings.Cut(strings.TrimSpace(line), "=")
		if found && strings.TrimSpace(candidate) == key {
			lines[index] = key + "=" + value
			return strings.Join(lines, "\n"), nil
		}
	}
	return "", fmt.Errorf("generated property %q is missing", key)
}
