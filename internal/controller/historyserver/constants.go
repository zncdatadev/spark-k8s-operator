/*
Copyright 2023 zncdatadev.

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

const (
	// HTTP is the history server UI/REST port; its name and number are e2e contract.
	HttpPortName = "http"
	HttpPort     = 18080

	// Metrics is served by the JMX prometheus javaagent inside the main container.
	MetricsPortName = "metrics"
	MetricsPort     = 18081

	// Oidc is the oauth2-proxy entrypoint fronting the UI when OIDC authentication is on.
	OidcPortName = "oidc"
	OidcPort     = 4180

	// SparkDefaultsFileName is the properties file the history server is started with.
	SparkDefaultsFileName = "spark-defaults.conf"

	// LogConfigFileName is the ConfigMap key (and in-container file name under
	// /kubedoop/config) of the log4j2 configuration; asserted by the logging e2e suite.
	LogConfigFileName = "log4j2.properties"

	// LogFileName is the rolling log file the Vector pipeline collects
	// ("/kubedoop/log/node/spark.log4j2.xml"); the base name is e2e contract.
	LogFileName = "spark.log4j2.xml"

	// ConsoleConversionPattern matches the log4j2 console layout of the legacy implementation.
	ConsoleConversionPattern = "%d{ISO8601} %p [%t] %c - %m%n"

	trueValue = "true"
)
