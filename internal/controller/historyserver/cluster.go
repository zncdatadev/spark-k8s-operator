package historyserver

import (
	"context"

	resourceClient "github.com/zncdatadev/operator-go/pkg/client"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	"github.com/zncdatadev/operator-go/pkg/util"

	shsv1alpha1 "github.com/zncdatadev/spark-k8s-operator/api/v1alpha1"
	"github.com/zncdatadev/spark-k8s-operator/internal/util/version"
)

var _ reconciler.Reconciler = &ClusterReconciler{}

const (
	RoleName = "node"
)

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
		version.BuildVersion,
		shsv1alpha1.DefaultProductVersion,
		func(options *util.ImageOptions) {
			options.Custom = r.Spec.Image.Custom
			options.Repo = r.Spec.Image.Repo
			options.PullPolicy = r.Spec.Image.PullPolicy
		},
	)

	if r.Spec.Image.KubedoopVersion != "" {
		image.KubedoopVersion = r.Spec.Image.KubedoopVersion
	}

	return image
}

func (r *ClusterReconciler) RegisterResource(ctx context.Context) error {
	roleInfo := reconciler.RoleInfo{
		ClusterInfo: r.ClusterInfo,
		RoleName:    RoleName,
	}

	node := NewNodeRoleReconciler(
		r.Client,
		r.IsStopped(),
		r.ClusterConfig,
		roleInfo,
		r.GetImage(),
		r.Spec.Node,
	)
	if err := node.RegisterResources(ctx); err != nil {
		return err
	}

	r.AddResource(node)

	return nil
}
