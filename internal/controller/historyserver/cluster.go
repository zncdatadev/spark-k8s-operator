package historyserver

import (
	"context"

	resourceClient "github.com/zncdatadev/operator-go/pkg/client"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	"github.com/zncdatadev/operator-go/pkg/util"

	shsv1alpha1 "github.com/zncdatadev/spark-k8s-operator/api/v1alpha1"
)

var _ reconciler.Reconciler = &ClusterReconciler{}

type ClusterReconciler struct {
	reconciler.BaseCluster[*shsv1alpha1.SparkHistoryServerSpec]
	ClusterConfig *shsv1alpha1.ClusterConfigSpec
}

func NewClusterReconciler(
	client *resourceClient.Client,
	clusterInfo reconciler.ClusterInfo,
	spec *shsv1alpha1.SparkHistoryServerSpec,
) *ClusterReconciler {
	return &ClusterReconciler{
		BaseCluster: *reconciler.NewBaseCluster(
			client,
			clusterInfo,
			spec.ClusterOperation,
			spec,
		),
		ClusterConfig: spec.ClusterConfig,
	}
}

func (r *ClusterReconciler) GetImage() *util.Image {
	image := util.NewImage(
		shsv1alpha1.DefaultProductName,
		shsv1alpha1.DefaultKubedoopVersion,
		shsv1alpha1.DefaultProductVersion,
	)

	if r.Spec.Image != nil {
		image.Custom = r.Spec.Image.Custom
		image.Repo = r.Spec.Image.Repo
		image.KubedoopVersion = r.Spec.Image.KubedoopVersion
		image.ProductVersion = r.Spec.Image.ProductVersion
		image.PullPolicy = r.Spec.Image.PullPolicy
		image.PullSecretName = r.Spec.Image.PullSecretName
	}

	return image
}

func (r *ClusterReconciler) RegisterResource(ctx context.Context) error {
	roleInfo := reconciler.RoleInfo{
		ClusterInfo: r.ClusterInfo,
		RoleName:    "node",
	}

	node := NewNodeRoleReconciler(
		r.Client,
		r.IsStopped(),
		r.ClusterConfig,
		roleInfo,
		r.GetImage(),
		r.Spec.Node,
	)
	node.RegisterResources(ctx)

	r.AddResource(node)

	return nil
}
