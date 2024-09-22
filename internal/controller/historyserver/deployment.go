package historyserver

import (
	"context"
	"path"
	"strings"
	"time"

	"github.com/zncdatadev/operator-go/pkg/builder"
	resourceClient "github.com/zncdatadev/operator-go/pkg/client"
	"github.com/zncdatadev/operator-go/pkg/constants"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	"github.com/zncdatadev/operator-go/pkg/util"
	corev1 "k8s.io/api/core/v1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	shsv1alpha1 "github.com/zncdatadev/spark-k8s-operator/api/v1alpha1"
)

const (
	SparkConfigDefauleFileName = "spark-defaults.conf"
	SparkHistoryContainerName  = "spark-history"
)

var _ builder.DeploymentBuilder = &DeploymentBuilder{}

type DeploymentBuilder struct {
	builder.Deployment
	Ports          []corev1.ContainerPort
	ClusteerConfig *shsv1alpha1.ClusterConfigSpec
	ClusterName    string
	RoleName       string
}

func NewDeploymentBuilder(
	client *resourceClient.Client,
	name string,
	clusterConfig *shsv1alpha1.ClusterConfigSpec,
	replicas *int32,
	ports []corev1.ContainerPort,
	image *util.Image,
	options builder.WorkloadOptions,
) *DeploymentBuilder {
	return &DeploymentBuilder{
		Deployment: *builder.NewDeployment(
			client,
			name,
			replicas,
			image,
			options,
		),
		Ports:          ports,
		ClusteerConfig: clusterConfig,
		ClusterName:    options.ClusterName,
		RoleName:       options.RoleName,
	}
}

func (b *DeploymentBuilder) getS3LogConfig(ctx context.Context) (*S3Logconfig, error) {
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

func (b *DeploymentBuilder) getMainContainerCmdArgs(s3LogConfig *S3Logconfig) string {
	s3LogCmdArgs := ""

	if s3LogConfig != nil {
		s3LogCmdArgs = s3LogConfig.GetPartialCmdArgs()
	}

	args := `
# TODO: remove this when refactor spark dockerfile
# fix jar conflict in container image
rm -rf jars/slf4j-api-1.7.36.jar

mkdir -p ` + constants.KubedoopConfigDir + `
cp ` + path.Join(constants.KubedoopConfigDirMount, `*`) + " " + constants.KubedoopConfigDir + `
` + s3LogCmdArgs + `
echo $AWS_ACCESS_KEY_ID
echo $AWS_SECRET_ACCESS_KEY
cat /kubedoop/secret/s3-credentials/*
echo ""
` + path.Join(constants.KubedoopRoot, "spark/sbin/start-history-server.sh") + ` --properties-file ` + path.Join(constants.KubedoopConfigDir, SparkConfigDefauleFileName) + `
`
	return util.IndentTab4Spaces(args)
}

func (b *DeploymentBuilder) getMainContainerEnvVars() map[string]string {
	jvmOpts := []string{
		"-Dlog4j.configurationFile=" + path.Join(constants.KubedoopConfigDir, "log4j2.properties"),
		"-javaagent:" + path.Join(constants.KubedoopJmxDir, "jmx_prometheus_javaagent.jar=8090:"+path.Join(constants.KubedoopJmxDir, "config.yaml")),
	}

	envVars := map[string]string{
		"SPARK_NO_DAEMONIZE": "true",
		"SPARK_HISTORY_OPTS": strings.Join(jvmOpts, " "),
	}

	return envVars
}

func (b *DeploymentBuilder) getMainContainer(s3LogConfig *S3Logconfig) *builder.Container {
	containerBuilder := builder.NewContainer(SparkHistoryContainerName, b.GetImage())
	containerBuilder.SetCommand([]string{"/bin/bash", "-c"})
	containerBuilder.SetArgs([]string{b.getMainContainerCmdArgs(s3LogConfig)})
	containerBuilder.AddPorts(b.Ports)
	containerBuilder.AddEnvs(b.getMainContainerEnvVars())
	containerBuilder.SetSecurityContext(0, 0, false)
	return containerBuilder
}

func (b *DeploymentBuilder) addSparkDefaultConfigVolume(containerBuilder *builder.Container) {
	volumeName := "spark-default-config"

	volume := &corev1.Volume{
		Name: volumeName,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: b.Name,
				},
			},
		},
	}

	b.AddVolume(volume)

	volumeMount := &corev1.VolumeMount{
		Name:      volumeName,
		MountPath: constants.KubedoopConfigDirMount,
	}

	containerBuilder.AddVolumeMount(volumeMount)
}

func (b *DeploymentBuilder) addS3CrenditialVolume(containerBuilder *builder.Container, s3LogConfig *S3Logconfig) {
	if s3LogConfig == nil {
		return
	}

	volume := s3LogConfig.GetVolume()
	b.AddVolume(volume)

	volumeMount := s3LogConfig.GetVolumeMount()
	containerBuilder.AddVolumeMount(volumeMount)
}

func (b *DeploymentBuilder) Build(ctx context.Context) (ctrlclient.Object, error) {
	s3LogConfig, err := b.getS3LogConfig(ctx)
	if err != nil {
		return nil, err
	}

	mainContainer := b.getMainContainer(s3LogConfig)
	b.addS3CrenditialVolume(mainContainer, s3LogConfig)
	b.addSparkDefaultConfigVolume(mainContainer)

	b.AddContainer(mainContainer.Build())
	return b.GetObject()
}

func NewDeploymentReconciler(
	client *resourceClient.Client,
	roleGroupInfo reconciler.RoleGroupInfo,
	clusterConfig *shsv1alpha1.ClusterConfigSpec,
	ports []corev1.ContainerPort,
	image *util.Image,
	stopped bool,
	spec *shsv1alpha1.RoleGroupSpec,
) (*reconciler.Deployment, error) {
	options := builder.WorkloadOptions{
		Options: builder.Options{
			ClusterName:   roleGroupInfo.ClusterName,
			RoleName:      roleGroupInfo.RoleName,
			RoleGroupName: roleGroupInfo.RoleGroupName,
			Labels:        roleGroupInfo.GetLabels(),
			Annotations:   roleGroupInfo.GetAnnotations(),
		},
		// PodOverrides:     spec.PodOverrides,
		EnvOverrides:     spec.EnvOverrides,
		CommandOverrides: spec.CommandOverrides,
	}

	if spec.Config != nil {
		if spec.Config.GracefulShutdownTimeout != nil {
			if gracefulShutdownTimeout, err := time.ParseDuration(*spec.Config.GracefulShutdownTimeout); err != nil {
				return nil, err
			} else {
				options.TerminationGracePeriod = &gracefulShutdownTimeout
			}

		}

		options.Affinity = spec.Config.Affinity
		options.Resource = spec.Config.Resources
	}

	b := NewDeploymentBuilder(
		client,
		roleGroupInfo.GetFullName(),
		clusterConfig,
		&spec.Replicas,
		ports,
		image,
		options,
	)

	return reconciler.NewDeployment(
		client,
		roleGroupInfo.GetFullName(),
		b,
		stopped,
	), nil
}
