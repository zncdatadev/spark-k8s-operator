package historyserver

import (
	"context"

	commonsv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/commons/v1alpha1"
	"github.com/zncdatadev/operator-go/pkg/builder"
	resourceClient "github.com/zncdatadev/operator-go/pkg/client"
	"github.com/zncdatadev/operator-go/pkg/constants"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	oputil "github.com/zncdatadev/operator-go/pkg/util"
	corev1 "k8s.io/api/core/v1"

	shsv1alpha1 "github.com/zncdatadev/spark-k8s-operator/api/v1alpha1"
	"github.com/zncdatadev/spark-k8s-operator/internal/util"
)

var (
	SparkHistoryPorts = []corev1.ContainerPort{
		{
			Name:          util.HttpPortName,
			ContainerPort: util.HttpPort,
		},
		//  TODO: Add GRPC port
	}
	OidcPorts = []corev1.ContainerPort{
		{
			Name:          util.OidcPortName,
			ContainerPort: util.OidcPort,
		},
	}
)

var _ reconciler.Reconciler = &NodeRoleReconciler{}

type NodeRoleReconciler struct {
	reconciler.BaseRoleReconciler[*shsv1alpha1.RoleSpec]
	ClusterConfig *shsv1alpha1.ClusterConfigSpec
	Image         *oputil.Image
}

func NewNodeRoleReconciler(
	client *resourceClient.Client,
	clusterStopped bool,
	clusterConfig *shsv1alpha1.ClusterConfigSpec,
	roleInfo reconciler.RoleInfo,
	image *oputil.Image,
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
		mergedRoleGroupConfig, err := oputil.MergeObject(r.Spec.Config, roleGroup.Config)
		if err != nil {
			return err
		}

		mergedOverrides, err := oputil.MergeObject(r.Spec.OverridesSpec, roleGroup.OverridesSpec)
		if err != nil {
			return err
		}

		info := reconciler.RoleGroupInfo{
			RoleInfo:      r.RoleInfo,
			RoleGroupName: name,
		}

		reconcilers, err := r.GetImageResourceWithRoleGroup(info, roleGroup.Replicas, mergedRoleGroupConfig, mergedOverrides)

		if err != nil {
			return err
		}

		for _, reconciler := range reconcilers {
			r.AddResource(reconciler)
		}
	}
	return nil
}

func (r *NodeRoleReconciler) GetImageResourceWithRoleGroup(
	info reconciler.RoleGroupInfo,
	replicas *int32,
	config *shsv1alpha1.ConfigSpec,
	overrides *commonsv1alpha1.OverridesSpec,

) ([]reconciler.Reconciler, error) {

	options := func(o *builder.Options) {
		o.ClusterName = info.GetClusterName()
		o.RoleName = info.GetRoleName()
		o.RoleGroupName = info.GetGroupName()

		o.Labels = info.GetLabels()
		o.Annotations = info.GetAnnotations()
	}

	var commonsRoleGroupConfig *commonsv1alpha1.RoleGroupConfigSpec
	if config != nil {
		commonsRoleGroupConfig = config.RoleGroupConfigSpec
	}

	cm := NewConfigMapReconciler(
		r.Client,
		r.ClusterConfig,
		info,
		config,
		options,
	)

	sts, err := NewStatefulSetReconciler(
		r.Client,
		info,
		r.ClusterConfig,
		SparkHistoryPorts,
		r.Image,
		replicas,
		r.ClusterStopped(),
		overrides,
		commonsRoleGroupConfig,
		options,
	)
	if err != nil {
		return nil, err
	}

	svc := reconciler.NewServiceReconciler(
		r.Client,
		info.GetFullName(),
		append(SparkHistoryPorts, OidcPorts...),
		func(o *builder.ServiceBuilderOptions) {
			o.ListenerClass = constants.ListenerClass(r.ClusterConfig.ListenerClass)
			o.ClusterName = info.GetClusterName()
			o.RoleName = info.GetRoleName()
			o.RoleGroupName = info.GetGroupName()
			o.Labels = info.GetLabels()
			o.Annotations = info.GetAnnotations()
		},
	)

	metricsService := NewRoleGroupMetricsService(
		r.Client,
		&info,
	)

	return []reconciler.Reconciler{cm, sts, svc, metricsService}, nil
}
