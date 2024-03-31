package controller

import (
	"github.com/zncdata-labs/spark-k8s-operator/internal/common"
)

const (
	SparkHistoryHTTPPortName   string = "http"
	SparkHistoryHTTPPortNumber int32  = 18080
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

// CreateRoleGroupLoggingConfigMapName create role group logging config-map name
func CreateRoleGroupLoggingConfigMapName(instanceName string, groupName string) string {
	return common.NewResourceNameGenerator(instanceName, "", groupName).GenerateResourceName("log4j")
}
