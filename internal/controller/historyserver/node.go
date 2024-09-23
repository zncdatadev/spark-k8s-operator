package historyserver

import (
	"context"

	"github.com/zncdatadev/operator-go/pkg/builder"
	resourceClient "github.com/zncdatadev/operator-go/pkg/client"
	"github.com/zncdatadev/operator-go/pkg/constants"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	"github.com/zncdatadev/operator-go/pkg/util"
	corev1 "k8s.io/api/core/v1"

	shsv1alpha1 "github.com/zncdatadev/spark-k8s-operator/api/v1alpha1"
)

var (
	SparkHistoryPorts = []corev1.ContainerPort{
		{
			Name:          "http",
			ContainerPort: 18080,
		},
	}
	OidcPorts = []corev1.ContainerPort{
		{
			Name:          "oidc",
			ContainerPort: 4180,
		},
	}
)

var _ reconciler.Reconciler = &NodeRoleReconciler{}

type NodeRoleReconciler struct {
	reconciler.BaseRoleReconciler[*shsv1alpha1.RoleSpec]
	ClusterConfig *shsv1alpha1.ClusterConfigSpec
	Image         *util.Image
}

func NewNodeRoleReconciler(
	client *resourceClient.Client,
	clusterStopped bool,
	clusterConfig *shsv1alpha1.ClusterConfigSpec,
	roleInfo reconciler.RoleInfo,
	image *util.Image,
	spec *shsv1alpha1.RoleSpec,
) *NodeRoleReconciler {
	return &NodeRoleReconciler{
		BaseRoleReconciler: *reconciler.NewBaseRoleReconciler(
			client,
			clusterStopped,
			roleInfo,
			spec,
		),
		ClusterConfig: clusterConfig,
		Image:         image,
	}
}

func (r *NodeRoleReconciler) RegisterResources(ctx context.Context) error {
	for name, roleGroup := range r.Spec.RoleGroups {
		mergedRolgGroup := roleGroup.DeepCopy()
		r.MergeRoleGroupSpec(mergedRolgGroup)

		info := reconciler.RoleGroupInfo{
			RoleInfo:      r.RoleInfo,
			RoleGroupName: name,
		}

		reconcilers, err := r.GetImageResourceWithRoleGroup(ctx, info, mergedRolgGroup)

		if err != nil {
			return err
		}

		for _, reconciler := range reconcilers {
			r.AddResource(reconciler)
		}
	}
	return nil
}

func (r *NodeRoleReconciler) GetImageResourceWithRoleGroup(ctx context.Context, info reconciler.RoleGroupInfo, spec *shsv1alpha1.RoleGroupSpec) ([]reconciler.Reconciler, error) {

	cm := NewConfigMapReconciler(
		r.Client,
		r.ClusterConfig,
		info,
		spec,
	)

	deployment, err := NewDeploymentReconciler(
		r.Client,
		info,
		r.ClusterConfig,
		SparkHistoryPorts,
		r.Image,
		r.ClusterStopped,
		spec,
	)
	if err != nil {
		return nil, err
	}

	svc := reconciler.NewServiceReconciler(
		r.Client,
		info.GetFullName(),
		append(SparkHistoryPorts, OidcPorts...),
		func(sbo *builder.ServiceBuilderOption) {
			sbo.Labels = info.GetLabels()
			sbo.Annotations = info.GetAnnotations()
			sbo.ClusterName = r.ClusterInfo.ClusterName
			sbo.RoleName = r.RoleInfo.RoleName
			sbo.RoleGroupName = info.RoleGroupName
			sbo.ListenerClass = constants.ListenerClass(r.ClusterConfig.ListenerClass)
		},
	)
	return []reconciler.Reconciler{cm, deployment, svc}, nil
}
