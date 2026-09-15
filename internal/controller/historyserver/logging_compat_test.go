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
	"strings"
	"testing"
)

const (
	generatedConsoleType      = "appender.console.type=Console"
	generatedConsoleName      = "appender.console.name=STDOUT"
	generatedConsoleThreshold = "appender.console.filter.threshold.type=ThresholdFilter"
)

func TestPreserveLegacyLog4j2Defaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      string
		contains   []string
		notContain []string
		wantError  bool
	}{
		{
			name: "console only defaults",
			input: strings.Join([]string{
				"rootLogger.level=INFO",
				"rootLogger.appenderRefs=stdout",
				"rootLogger.appenderRef.stdout.ref=STDOUT",
				"appenders=console",
				generatedConsoleType,
				generatedConsoleName,
				"appender.console.layout.type=PatternLayout",
				"appender.console.layout.pattern=%m%n",
				"",
			}, "\n"),
			contains: []string{
				"appender.console.target=SYSTEM_ERR",
				generatedConsoleThreshold,
				"appender.console.filter.threshold.level=INFO",
			},
			notContain: []string{"appender.file."},
		},
		{
			name: "explicit console level and file defaults",
			input: strings.Join([]string{
				"rootLogger.level=DEBUG",
				"rootLogger.appenderRefs=stdout,file",
				"rootLogger.appenderRef.stdout.ref=STDOUT",
				"rootLogger.appenderRef.file.ref=FILE",
				"appenders=console,file",
				generatedConsoleType,
				generatedConsoleName,
				generatedConsoleThreshold,
				"appender.console.filter.threshold.level=DEBUG",
				"appender.console.layout.type=PatternLayout",
				"appender.console.layout.pattern=%m%n",
				"appender.file.type=RollingFile",
				"appender.file.name=FILE",
				"appender.file.fileName=/kubedoop/log/node/spark.log4j2.xml",
				"appender.file.filePattern=/kubedoop/log/node/spark.log4j2.xml.%i",
				"appender.file.layout.type=XMLLayout",
				"appender.file.policies.type=Policies",
				"appender.file.policies.size.type=SizeBasedTriggeringPolicy",
				"appender.file.policies.size.size=5MB",
				"appender.file.strategy.type=DefaultRolloverStrategy",
				"appender.file.strategy.max=1",
				"",
			}, "\n"),
			contains: []string{
				"appender.console.target=SYSTEM_ERR",
				"appender.console.filter.threshold.level=DEBUG",
				"appender.file.filter.threshold.type=ThresholdFilter",
				"appender.file.filter.threshold.level=INFO",
				"appender.file.policies.size.size=10MB",
			},
			notContain: []string{"appender.console.filter.threshold.level=INFO"},
		},
		{
			name: "unexpected console target fails closed",
			input: strings.Join([]string{
				generatedConsoleType,
				generatedConsoleName,
				"appender.console.target=SYSTEM_OUT",
				"",
			}, "\n"),
			wantError: true,
		},
		{
			name: "duplicate generated property fails closed",
			input: strings.Join([]string{
				generatedConsoleType,
				generatedConsoleType,
				generatedConsoleName,
				"",
			}, "\n"),
			wantError: true,
		},
		{
			name: "incomplete generated threshold fails closed",
			input: strings.Join([]string{
				generatedConsoleType,
				generatedConsoleName,
				generatedConsoleThreshold,
				"",
			}, "\n"),
			wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			actual, err := preserveLegacyLog4j2Defaults(test.input)
			if test.wantError {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("preserveLegacyLog4j2Defaults() error = %v", err)
			}
			for _, expected := range test.contains {
				if !strings.Contains(actual, expected) {
					t.Errorf("result does not contain %q:\n%s", expected, actual)
				}
			}
			for _, unexpected := range test.notContain {
				if strings.Contains(actual, unexpected) {
					t.Errorf("result unexpectedly contains %q:\n%s", unexpected, actual)
				}
			}
		})
	}
}
