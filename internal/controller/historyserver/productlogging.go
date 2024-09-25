package historyserver

import (
	commonsv1alph1 "github.com/zncdatadev/operator-go/pkg/apis/commons/v1alpha1"
	"github.com/zncdatadev/operator-go/pkg/builder"
	"github.com/zncdatadev/operator-go/pkg/productlogging"

	"github.com/zncdatadev/spark-k8s-operator/api/v1alpha1"
)

func ProductLogging(
	logging *v1alpha1.LoggingSpec,
	containerName string,
	configMapBuilder builder.ConfigBuilder,
) {
	var containerLoging *commonsv1alph1.LoggingConfigSpec
	if logging != nil {
		if o, ok := logging.Containers[containerName]; ok {
			containerLoging = &o
		}
	}

	log4j2Config := productlogging.NewLog4j2ConfigGenerator(
		containerLoging,
		containerName,
		"%d{ISO8601} %p [%t] %c - %m%n",
		nil,
		"spark.log4j2.xml",
		"",
	)

	logConfig := log4j2Config.Generate()

	configMapBuilder.AddData(map[string]string{"log4j2.properties": logConfig})
}
