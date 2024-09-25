package historyserver

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/zncdatadev/operator-go/pkg/builder"
	"github.com/zncdatadev/operator-go/pkg/client"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	shsv1alpha1 "github.com/zncdatadev/spark-k8s-operator/api/v1alpha1"
)

var _ builder.ConfigBuilder = &ConfigMapBuilder{}

type ConfigMapBuilder struct {
	builder.ConfigMapBuilder

	ClusteerConfig *shsv1alpha1.ClusterConfigSpec
	RoleName       string
	ProductLogging *shsv1alpha1.LoggingSpec
}

func NewSparkConfigMapBuilder(
	client *client.Client,
	name string,
	clusterConfig *shsv1alpha1.ClusterConfigSpec,
	productLogging *shsv1alpha1.LoggingSpec,
	options builder.Options,
) *ConfigMapBuilder {
	return &ConfigMapBuilder{
		ConfigMapBuilder: *builder.NewConfigMapBuilder(client, name, options.Labels, options.Annotations),
		ClusteerConfig:   clusterConfig,
		RoleName:         options.RoleName,
		ProductLogging:   productLogging,
	}
}

func (b *ConfigMapBuilder) getS3LogConfig(ctx context.Context) (*S3Logconfig, error) {
	if b.ClusteerConfig.LogFileDirectory.S3 == nil {
		return nil, nil
	}
	s3Logconfig, err := NewS3Logconfig(
		ctx,
		b.GetClient(),
		b.ClusteerConfig.LogFileDirectory.S3,
	)
	if err != nil {
		return nil, err
	}
	return s3Logconfig, nil
}

func (b *ConfigMapBuilder) Build(ctx context.Context) (ctrlclient.Object, error) {

	S3Logconfig, err := b.getS3LogConfig(ctx)
	if err != nil {
		return nil, err
	}

	data := map[string]string{
		SparkConfigDefauleFileName: b.getSparkDefaules(S3Logconfig),
	}

	b.AddData(data)

	ProductLogging(
		b.ProductLogging,
		SparkHistoryContainerName,
		b,
	)

	return b.GetObject(), nil
}

func (b *ConfigMapBuilder) isCleaner() (bool, error) {
	cleaners := map[string]bool{}

	owner, ok := b.GetClient().GetOwnerReference().(*shsv1alpha1.SparkHistoryServer)
	if !ok {
		return false, fmt.Errorf("owner is not a SparkHistoryServer")
	}
	role := owner.Spec.Node
	if role.Config != nil && role.Config.Cleaner != nil {
		if *role.Config.Cleaner && len(role.RoleGroups) > 1 {
			return false, fmt.Errorf("more than one role group has cleaner enabled. Role cleaner can only be enabled for one role group. "+
				"Namespace: %s, ClusterName: %s, Cleaners %v",
				b.GetClient().GetOwnerNamespace(), b.GetClient().GetOwnerName(), cleaners,
			)
		}
	}

	for roleGroupName, roleGroup := range role.RoleGroups {
		if roleGroup.Config != nil && roleGroup.Config.Cleaner != nil {
			if *roleGroup.Config.Cleaner && roleGroup.Replicas > 1 {
				return false, fmt.Errorf(
					"role group has cleaner enabled but has more than one replica. "+
						"Namespace: %s, ClusterName: %s, RoleName: %s, RoleGroupName: %s",
					b.GetClient().GetOwnerNamespace(), b.GetClient().GetOwnerName(), b.RoleName, roleGroupName,
				)
			}
			cleaners[roleGroupName] = *roleGroup.Config.Cleaner
		}
	}

	for name, enabled := range cleaners {
		if b.RoleName == name {
			return enabled, nil
		}
	}

	return false, nil
}

func (b *ConfigMapBuilder) getSparkDefaules(s3Logconfig *S3Logconfig) string {

	config := map[string]string{}

	cleaner, err := b.isCleaner()
	if err != nil {
		return ""
	}

	if cleaner {
		config["spark.history.fs.cleaner.enabled"] = "true"
	}

	if s3Logconfig != nil {
		maps.Copy(config, s3Logconfig.GetPartialProperties())
	}

	sortedConfig := make([][]string, 0, len(config))
	for k, v := range config {
		sortedConfig = append(sortedConfig, []string{k, v})
	}
	slices.SortFunc(sortedConfig, func(i, j []string) int {
		return strings.Compare(i[0], j[0])
	})

	str := ""
	for _, kv := range sortedConfig {
		str += kv[0] + "        " + kv[1] + "\n"
	}

	return str
}

func NewConfigMapReconciler(
	client *client.Client,
	clusterConfig *shsv1alpha1.ClusterConfigSpec,
	roleGroupInfo reconciler.RoleGroupInfo,
	spec *shsv1alpha1.RoleGroupSpec,
) *reconciler.SimpleResourceReconciler[*ConfigMapBuilder] {

	var loggingSpec *shsv1alpha1.LoggingSpec
	if spec.Config != nil {
		loggingSpec = spec.Config.Logging
	}

	builder := NewSparkConfigMapBuilder(
		client,
		roleGroupInfo.GetFullName(),
		clusterConfig,
		loggingSpec,
		builder.Options{
			RoleName:    roleGroupInfo.RoleName,
			Labels:      roleGroupInfo.GetLabels(),
			Annotations: roleGroupInfo.GetAnnotations(),
		},
	)

	return reconciler.NewSimpleResourceReconciler[*ConfigMapBuilder](client, roleGroupInfo.GetFullName(), builder)

}
