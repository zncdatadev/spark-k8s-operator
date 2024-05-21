package controller

import (
	"context"

	sparkv1alpha1 "github.com/zncdatadev/spark-k8s-operator/api/v1alpha1"
	"github.com/zncdatadev/spark-k8s-operator/internal/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
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

func (s *ServiceReconciler) GetServiceType(ctx context.Context) corev1.ServiceType {

	listenerInfo := common.NewListenerInfo(
		&SparkHistoryInstance{Instance: s.Instance},
		common.ResourceClient{
			Ctx:       ctx,
			Client:    s.Client,
			Namespace: s.Instance.Namespace,
		},
	)

	return listenerInfo.GetServiceType()
}

// Build implements the ResourceBuilder interface
func (s *ServiceReconciler) Build(ctx context.Context) (client.Object, error) {
	instance := s.Instance
	roleGroupName := s.GroupName
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      createServiceName(instance.Name, roleGroupName),
			Namespace: instance.Namespace,
			Labels:    s.MergedLabels,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port:       SparkHistoryHTTPPortNumber,
					Name:       SparkHistoryHTTPPortName,
					Protocol:   corev1.ProtocolTCP,
					TargetPort: intstr.IntOrString{IntVal: SparkHistoryHTTPPortNumber},
				},
			},
			Selector: s.MergedLabels,
			Type:     s.GetServiceType(ctx),
		},
	}
	return svc, nil
}
