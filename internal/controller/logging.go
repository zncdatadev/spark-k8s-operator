package controller

import (
	"context"
	stackv1alpha1 "github.com/zncdata-labs/spark-k8s-operator/api/v1alpha1"
	"github.com/zncdata-labs/spark-k8s-operator/internal/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const DefaultLog4jProperties = `
# Set everything to be logged to the console
rootLogger.level = info
rootLogger.appenderRef.stdout.ref = console

# In the pattern layout configuration below, we specify an explicit '%ex' conversion
# pattern for logging Throwables. If this was omitted, then (by default) Log4J would
# implicitly add an '%xEx' conversion pattern which logs stacktraces with additional
# class packaging information. That extra information can sometimes add a substantial
# performance overhead, so we disable it in our default logging config.
# For more information, see SPARK-39361.
appender.console.type = Console
appender.console.name = console
appender.console.target = SYSTEM_ERR
appender.console.layout.type = PatternLayout
appender.console.layout.pattern = %d{yy/MM/dd HH:mm:ss} %p %c{1}: %m%n%ex

# Set the default spark-shell/spark-sql log level to WARN. When running the
# spark-shell/spark-sql, the log level for these classes is used to overwrite
# the root logger's log level, so that the user can have different defaults
# for the shell and regular Spark apps.
logger.repl.name = org.apache.spark.repl.Main
logger.repl.level = warn

logger.thriftserver.name = org.apache.spark.sql.hive.thriftserver.SparkSQLCLIDriver
logger.thriftserver.level = warn

# Settings to quiet third party logs that are too verbose
logger.jetty1.name = org.sparkproject.jetty
logger.jetty1.level = warn
logger.jetty2.name = org.sparkproject.jetty.util.component.AbstractLifeCycle
logger.jetty2.level = error
logger.replexprTyper.name = org.apache.spark.repl.SparkIMain$exprTyper
logger.replexprTyper.level = info
logger.replSparkILoopInterpreter.name = org.apache.spark.repl.SparkILoop$SparkILoopInterpreter
logger.replSparkILoopInterpreter.level = info
logger.parquet1.name = org.apache.parquet
logger.parquet1.level = error
logger.parquet2.name = parquet
logger.parquet2.level = error

# SPARK-9183: Settings to avoid annoying messages when looking up nonexistent UDFs in SparkSQL with Hive support
logger.RetryingHMSHandler.name = org.apache.hadoop.hive.metastore.RetryingHMSHandler
logger.RetryingHMSHandler.level = fatal
logger.FunctionRegistry.name = org.apache.hadoop.hive.ql.exec.FunctionRegistry
logger.FunctionRegistry.level = error

# For deploying Spark ThriftServer
# SPARK-34128: Suppress undesirable TTransportException warnings involved in THRIFT-4805
appender.console.filter.1.type = RegexFilter
appender.console.filter.1.regex = .*Thrift error occurred during processing of message.*
appender.console.filter.1.onMatch = deny
appender.console.filter.1.onMismatch = neutral
`
const Log4jCfgName = "log4j2.properties"

type Logger string

type RoleLoggingDataBuilder interface {
	MakeContainerLog4jData() map[string]string
}

type LoggingRecociler struct {
	common.GeneralResourceStyleReconciler[*stackv1alpha1.SparkHistoryServer, any]
	RoleLoggingDataBuilder RoleLoggingDataBuilder
}

// NewLogging  new logging reconcile
func NewLogging(
	scheme *runtime.Scheme,
	instance *stackv1alpha1.SparkHistoryServer,
	client client.Client,
	groupName string,
	mergedLabels map[string]string,
	mergedCfg any,
	logDataBuilder RoleLoggingDataBuilder,
) *LoggingRecociler {
	return &LoggingRecociler{
		GeneralResourceStyleReconciler: *common.NewGeneraResourceStyleReconciler(
			scheme,
			instance,
			client,
			groupName,
			mergedLabels,
			mergedCfg,
		),
		RoleLoggingDataBuilder: logDataBuilder,
	}
}

// Build log4j config map
func (l *LoggingRecociler) Build(_ context.Context) (client.Object, error) {
	cmData := l.RoleLoggingDataBuilder.MakeContainerLog4jData()
	if len(cmData) == 0 {
		return nil, nil
	}
	obj := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      CreateRoleGroupLoggingConfigMapName(l.Instance.Name, l.GroupName),
			Namespace: l.Instance.Namespace,
			Labels:    l.MergedLabels,
		},
		Data: cmData,
	}
	return obj, nil
}

func PropertiesValue(_ Logger, loggingConfig *stackv1alpha1.LoggingConfigSpec) string {
	properties := make(map[string]string)
	if loggingConfig.Loggers != nil {
		for k, level := range loggingConfig.Loggers {
			if level != nil {
				v := *level
				properties["logger."+k+".level"] = v.Level
			}
		}
	}
	if loggingConfig.Console != nil {
		properties["appender.console.filter.threshold.type"] = "ThresholdFilter"
		properties["appender.console.filter.threshold.level"] = loggingConfig.Console.Level
	}

	if loggingConfig.File != nil {
		properties["appender.file.type"] = "RollingRandomAccessFile"
		properties["appender.file.name"] = "file"
		properties["appender.file.fileName"] = "logs/$history-server.log"
		//Use %pid in the filePattern to append <process-id>@<host-name> to the filename if you want separate log files for different CLI session
		properties["appender.file.filePattern"] = "logs/history-server.log.%d{yyyy-MM-dd}"
		properties["appender.file.layout.type"] = "PatternLayout"
		properties["appender.file.layout.pattern"] = "%d{ISO8601} %5p [%t] %c{2}: %m%n"
		properties["appender.file.policies.type"] = "Policies"
		properties["appender.file.policies.time.type"] = "TimeBasedTriggeringPolicy"
		properties["appender.file.policies.time.interval"] = "1"
		properties["appender.file.policies.time.modulate"] = "true"
		properties["appender.file.strategy.type"] = "DefaultRolloverStrategy"
		properties["appender.file.strategy.max"] = "30"

		properties["appender.file.filter.threshold.type"] = "ThresholdFilter"
		properties["appender.file.filter.threshold.level"] = loggingConfig.File.Level

		properties["rootLogger.appenderRef.stdout.ref"] = "console, file"
	}

	props := log4jProperties(properties)

	return DefaultLog4jProperties +
		"\n\n" +
		"# spark-history-operator modify logging\n" +
		props
}

func log4jProperties(properties map[string]string) string {
	data := ""
	for k, v := range properties {
		data += k + "=" + v + "\n"
	}
	return data
}
