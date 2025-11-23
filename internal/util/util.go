package util

import "github.com/zncdatadev/operator-go/pkg/reconciler"

const (
	HttpPortName   = "http"
	GrpcPortName   = "grpc"
	OidcPortName   = "oidc"
	MetricPortName = "metrics"
	HttpPort       = 18080
	MetricsPort    = 18081
	GrpcPort       = 15002
	OidcPort       = 4180
)

// GetMetricsPort returns the metrics port for a given role
func GetMetricsPort() int32 {
	return MetricsPort
}

func GetMetricsServiceName(roleGroupInfo *reconciler.RoleGroupInfo) string {
	return roleGroupInfo.GetFullName() + "-metrics"
}
