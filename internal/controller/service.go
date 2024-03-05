package controller

import (
	"context"

	sparkv1alpha1 "github.com/zncdata-labs/spark-k8s-operator/api/v1alpha1"
	"github.com/zncdata-labs/spark-k8s-operator/internal/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ServiceReconciler struct {
	common.GeneralResourceStyleReconciler[*sparkv1alpha1.SparkHistoryServer, *sparkv1alpha1.RoleGroupSpec]
}

// NewService new a ServiceReconcile
func NewService(
	scheme *runtime.Scheme,
	instance *sparkv1alpha1.SparkHistoryServer,
	client client.Client,
	groupName string,
	mergedLabels map[string]string,
	mergedCfg *sparkv1alpha1.RoleGroupSpec,

) *ServiceReconciler {
	return &ServiceReconciler{
		GeneralResourceStyleReconciler: *common.NewGeneraResourceStyleReconciler[*sparkv1alpha1.SparkHistoryServer,
			*sparkv1alpha1.RoleGroupSpec](
			scheme,
			instance,
			client,
			groupName,
			mergedLabels,
			mergedCfg,
		),
	}
}

// Build implements the ResourceBuilder interface
func (s *ServiceReconciler) Build(_ context.Context) (client.Object, error) {
	instance := s.Instance
	roleGroupName := s.GroupName
	svcSpec := s.getServiceSpec()
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        createServiceName(instance.Name, roleGroupName),
			Namespace:   instance.Namespace,
			Labels:      s.MergedLabels,
			Annotations: svcSpec.Annotations,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port:     svcSpec.Port,
					Name:     "http",
					Protocol: "TCP",
				},
			},
			Selector: s.MergedLabels,
			Type:     svcSpec.Type,
		},
	}
	return svc, nil
}

// get service spec
func (s *ServiceReconciler) getServiceSpec() *sparkv1alpha1.ListenerSpec {
	return getServiceSpec(s.Instance)
}
