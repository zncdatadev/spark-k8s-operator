package util

import "github.com/zncdatadev/operator-go/pkg/reconciler"

const (
	HttpPortName = "http"
	GrpcPortName = "grpc"
	OidcPortName = "oidc"
	HttpPort     = 18080
	GrpcPort     = 15002
	OidcPort     = 4180
)

// GetMetricsPort returns the metrics port for a given role
func GetMetricsPort() int32 {
	return HttpPort
}

func GetMetricsServiceName(roleGroupInfo *reconciler.RoleGroupInfo) string {
	return roleGroupInfo.GetFullName() + "-metrics"
}
