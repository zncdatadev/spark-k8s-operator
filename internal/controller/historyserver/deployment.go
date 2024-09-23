package historyserver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	authv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/authentication/v1alpha1"
	"github.com/zncdatadev/operator-go/pkg/builder"
	resourceClient "github.com/zncdatadev/operator-go/pkg/client"
	"github.com/zncdatadev/operator-go/pkg/constants"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	"github.com/zncdatadev/operator-go/pkg/util"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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
echo ""
` + path.Join(constants.KubedoopRoot, "spark/sbin/start-history-server.sh") + ` --properties-file ` + path.Join(constants.KubedoopConfigDir, SparkConfigDefauleFileName) + `
`
	return util.IndentTab4Spaces(args)
}

func (b *DeploymentBuilder) getMainContainerEnvVars() []corev1.EnvVar {
	jvmOpts := []string{
		"-Dlog4j.configurationFile=" + path.Join(constants.KubedoopConfigDir, "log4j2.properties"),
		"-javaagent:" + path.Join(constants.KubedoopJmxDir, "jmx_prometheus_javaagent.jar=8090:"+path.Join(constants.KubedoopJmxDir, "config.yaml")),
	}

	envVars := []corev1.EnvVar{
		{
			Name:  "SPARK_NO_DAEMONIZE",
			Value: "true",
		},
		{
			Name:  "SPARK_HISTORY_OPTS",
			Value: strings.Join(jvmOpts, " "),
		},
	}

	return envVars
}

func (b *DeploymentBuilder) getMainContainer(s3LogConfig *S3Logconfig) *builder.Container {
	containerBuilder := builder.NewContainer(SparkHistoryContainerName, b.GetImage())
	containerBuilder.SetCommand([]string{"/bin/bash", "-c"})
	containerBuilder.SetArgs([]string{b.getMainContainerCmdArgs(s3LogConfig)})
	containerBuilder.AddPorts(b.Ports)
	containerBuilder.AddEnvVars(b.getMainContainerEnvVars())
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

func (b *DeploymentBuilder) getOidcContainer(ctx context.Context) (*corev1.Container, error) {
	authClass := &authv1alpha1.AuthenticationClass{}
	if err := b.Client.GetWithOwnerNamespace(ctx, b.ClusteerConfig.Authentication.AuthenticationClass, authClass); err != nil {
		return nil, err
	}

	if authClass.Spec.AuthenticationProvider.OIDC == nil {
		return nil, nil
	}

	oidcProvider := authClass.Spec.AuthenticationProvider.OIDC

	scopes := []string{"openid", "email", "profile"}

	if b.ClusteerConfig.Authentication.Oidc.ExtraScopes != nil {
		scopes = append(scopes, b.ClusteerConfig.Authentication.Oidc.ExtraScopes...)
	}

	issuer := url.URL{
		Scheme: "http",
		Host:   oidcProvider.Hostname,
		Path:   oidcProvider.RootPath,
	}

	if oidcProvider.Port != 0 && oidcProvider.Port != 80 {
		issuer.Host += ":" + strconv.Itoa(oidcProvider.Port)
	}

	provisioner := oidcProvider.Provisioner
	// TODO: fix support keycloak-oidc
	if provisioner == "keycloak" {
		provisioner = "keycloak-oidc"
	}

	clientCredentialsSecretName := b.ClusteerConfig.Authentication.Oidc.ClientCredentialsSecret

	uid := b.Client.GetOwnerReference().GetUID()

	hash := sha256.Sum256([]byte(uid))
	hashStr := hex.EncodeToString(hash[:])
	tokenBytes := []byte(hashStr[:16])

	cookieSecret := base64.StdEncoding.EncodeToString([]byte(base64.StdEncoding.EncodeToString(tokenBytes)))

	var sparkHistoryPorts int32

	for _, port := range b.Ports {
		if port.Name == "http" {
			sparkHistoryPorts = port.ContainerPort
			break
		}
	}

	oidcContainer := &corev1.Container{
		Name:  "oidc",
		Image: "quay.io/oauth2-proxy/oauth2-proxy:latest",
		Env: []corev1.EnvVar{
			{
				Name:  "OAUTH2_PROXY_COOKIE_SECRET",
				Value: cookieSecret,
			},
			{
				Name: "OAUTH2_PROXY_CLIENT_ID",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: clientCredentialsSecretName,
						},
						Key: "CLIENT_ID",
					},
				},
			},
			{
				Name: "OAUTH2_PROXY_CLIENT_SECRET",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: clientCredentialsSecretName,
						},
						Key: "CLIENT_SECRET",
					},
				},
			},
			{
				Name:  "OAUTH2_PROXY_OIDC_ISSUER_URL",
				Value: issuer.String(),
			},
			{
				Name:  "OAUTH2_PROXY_SCOPE",
				Value: strings.Join(scopes, " "),
			},
			{
				Name:  "OAUTH2_PROXY_PROVIDER",
				Value: provisioner,
			},
			{
				Name:  "OAUTH2_PROXY_UPSTREAMS",
				Value: "http://localhost:" + strconv.Itoa(int(sparkHistoryPorts)),
			},
			{
				Name:  "OAUTH2_PROXY_HTTP_ADDRESS",
				Value: "0.0.0.0:4180",
			},
			{
				Name:  "OAUTH2_PROXY_REDIRECT_URL",
				Value: "http://localhost:4180/oauth2/callback",
			},
			{
				Name:  "OAUTH2_PROXY_CODE_CHALLENGE_METHOD",
				Value: "S256",
			},
			{
				Name:  "OAUTH2_PROXY_EMAIL_DOMAINS",
				Value: "*",
			},
		},
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1000m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		},
		Ports: OidcPorts,
	}

	return oidcContainer, nil
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

	if b.ClusteerConfig.Authentication != nil && b.ClusteerConfig.Authentication.Oidc != nil {
		oidcContainer, err := b.getOidcContainer(ctx)
		if err != nil {
			return nil, err
		}
		b.AddContainer(oidcContainer)
	}

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
