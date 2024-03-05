package controller

import (
	sparkv1alpha1 "github.com/zncdata-labs/spark-k8s-operator/api/v1alpha1"
	"github.com/zncdata-labs/spark-k8s-operator/internal/common"
)

func createConfigName(instanceName string, groupName string) string {
	return common.NewResourceNameGenerator(instanceName, "", groupName).GenerateResourceName("")
}

func createSecretName(instanceName string, groupName string) string {
	return common.NewResourceNameGenerator(instanceName, "", groupName).GenerateResourceName("")
}

func createPvcName(instanceName string, groupName string) string {
	return common.NewResourceNameGenerator(instanceName, "", groupName).GenerateResourceName("")
}

func createDeploymentName(instanceName string, groupName string) string {
	return common.NewResourceNameGenerator(instanceName, "", groupName).GenerateResourceName("")
}

func createServiceName(instanceName string, groupName string) string {
	return common.NewResourceNameGenerator(instanceName, "", groupName).GenerateResourceName("")
}

func createIngName(instanceName string, groupName string) string {
	return common.NewResourceNameGenerator(instanceName, "", groupName).GenerateResourceName("")
}

func getServiceSpec(instance *sparkv1alpha1.SparkHistoryServer) *sparkv1alpha1.ListenerSpec {
	spec := instance.Spec.ClusterConfig.Listener
	if spec == nil {
		spec = &sparkv1alpha1.ListenerSpec{
			Type: "ClusterIP",
			Port: 9083,
		}
	}
	return spec
}
