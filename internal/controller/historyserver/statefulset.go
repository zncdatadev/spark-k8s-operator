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

	authv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/authentication/v1alpha1"
	commonsv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/commons/v1alpha1"
	"github.com/zncdatadev/operator-go/pkg/builder"
	resourceClient "github.com/zncdatadev/operator-go/pkg/client"
	"github.com/zncdatadev/operator-go/pkg/constants"
	"github.com/zncdatadev/operator-go/pkg/productlogging"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	"github.com/zncdatadev/operator-go/pkg/util"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	shsv1alpha1 "github.com/zncdatadev/spark-k8s-operator/api/v1alpha1"
)

const (
	SparkConfigDefauleFileName = "spark-defaults.conf"
	SparkHistoryContainerName  = RoleName

	LogVolumeName    = builder.LogDataVolumeName
	ConfigVolumeName = "config"

	MaxLogFileSize = "10Mi"
)

var _ builder.StatefulSetBuilder = &StatefulSetBuilder{}

type StatefulSetBuilder struct {
	builder.StatefulSet
	Ports          []corev1.ContainerPort
	ClusteerConfig *shsv1alpha1.ClusterConfigSpec
	ClusterName    string
	RoleName       string
}

func NewStatefulSetBuilder(
	client *resourceClient.Client,
	name string,
	clusterConfig *shsv1alpha1.ClusterConfigSpec,
	replicas *int32,
	ports []corev1.ContainerPort,
	image *util.Image,
	overrides *commonsv1alpha1.OverridesSpec,
	roleGroupConfig *commonsv1alpha1.RoleGroupConfigSpec,
	options ...builder.Option,
) *StatefulSetBuilder {
	return &StatefulSetBuilder{
		StatefulSet: *builder.NewStatefulSetBuilder(
			client,
			name,
			replicas,
			image,
			overrides,
			roleGroupConfig,
			options...,
		),
		Ports:          ports,
		ClusteerConfig: clusterConfig,
	}
}

func (b *StatefulSetBuilder) getS3LogConfig(ctx context.Context) (*S3Logconfig, error) {
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

func (b *StatefulSetBuilder) getMainContainerCmdArgs(s3LogConfig *S3Logconfig) string {
	s3LogCmdArgs := ""

	if s3LogConfig != nil {
		s3LogCmdArgs = s3LogConfig.GetPartialCmdArgs()
	}

	args := `

mkdir -p ` + constants.KubedoopConfigDir + `
cp ` + path.Join(constants.KubedoopConfigDirMount, `*`) + " " + constants.KubedoopConfigDir + `
` + s3LogCmdArgs + `
echo ""
` + path.Join(constants.KubedoopRoot, "spark/sbin/start-history-server.sh") + ` --properties-file ` + path.Join(constants.KubedoopConfigDir, SparkConfigDefauleFileName) + `
`
	return util.IndentTab4Spaces(args)
}

func (b *StatefulSetBuilder) getMainContainerEnvVars() []corev1.EnvVar {
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
			Name:  "SPARK_DAEMON_CLASSPATH",
			Value: "/kubedoop/spark/extra-jars/*",
		},
		{
			Name:  "SPARK_HISTORY_OPTS",
			Value: strings.Join(jvmOpts, " "),
		},
	}

	return envVars
}

func (b *StatefulSetBuilder) getMainContainer(s3LogConfig *S3Logconfig) *builder.Container {
	containerBuilder := builder.NewContainer(SparkHistoryContainerName, b.GetImage())
	containerBuilder.SetCommand([]string{"/bin/bash", "-c"})
	containerBuilder.SetArgs([]string{b.getMainContainerCmdArgs(s3LogConfig)})
	containerBuilder.AddPorts(b.Ports)
	containerBuilder.AddEnvVars(b.getMainContainerEnvVars())
	containerBuilder.SetSecurityContext(0, 0, false)

	probe := &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{
				Port: intstr.FromInt(18080),
			},
		},
		InitialDelaySeconds: 10,
		TimeoutSeconds:      5,
		PeriodSeconds:       10,
		SuccessThreshold:    1,
	}
	containerBuilder.SetReadinessProbe(probe)
	containerBuilder.SetLivenessProbe(probe)

	return containerBuilder
}

func (b *StatefulSetBuilder) addSparkDefaultConfigVolume(containerBuilder *builder.Container) {
	volume := &corev1.Volume{
		Name: ConfigVolumeName,
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
		Name:      ConfigVolumeName,
		MountPath: constants.KubedoopConfigDirMount,
	}

	containerBuilder.AddVolumeMount(volumeMount)
}

func (b *StatefulSetBuilder) addS3CrenditialVolume(containerBuilder *builder.Container, s3LogConfig *S3Logconfig) {
	if s3LogConfig == nil {
		return
	}

	volume := s3LogConfig.GetVolume()
	b.AddVolume(volume)

	volumeMount := s3LogConfig.GetVolumeMount()
	containerBuilder.AddVolumeMount(volumeMount)
}

// add log volume to container
func (b *StatefulSetBuilder) addLogVolume(containerBuilder *builder.Container) {
	volume := &corev1.Volume{
		Name: LogVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{
				SizeLimit: func() *resource.Quantity {
					q := resource.MustParse(MaxLogFileSize)
					size := productlogging.CalculateLogVolumeSizeLimit([]resource.Quantity{q})
					return &size
				}(),
			},
		},
	}
	b.AddVolume(volume)

	volumeMount := &corev1.VolumeMount{
		Name:      LogVolumeName,
		MountPath: constants.KubedoopLogDir,
	}
	containerBuilder.AddVolumeMount(volumeMount)
}

func (b *StatefulSetBuilder) getOidcContainer(ctx context.Context) (*corev1.Container, error) {
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

	providerHint := oidcProvider.ProviderHint
	if providerHint == "keycloak" {
		providerHint = "keycloak-oidc"
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
				Value: providerHint,
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
				Name:  "OAUTH2_PROXY_COOKIE_SECURE", // https://github.com/oauth2-proxy/oauth2-proxy/blob/c64ec1251b8366b48c6c445bbeb307b18fcb314f/oauthproxy.go#L1091
				Value: "false",
			},
			{
				Name:  "OAUTH2_PROXY_WHITELIST_DOMAINS",
				Value: "*",
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
				corev1.ResourceCPU:    resource.MustParse("600m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		},
		Ports: OidcPorts,
	}

	return oidcContainer, nil
}

func (b *StatefulSetBuilder) Build(ctx context.Context) (ctrlclient.Object, error) {
	s3LogConfig, err := b.getS3LogConfig(ctx)
	if err != nil {
		return nil, err
	}

	mainContainer := b.getMainContainer(s3LogConfig)
	b.addS3CrenditialVolume(mainContainer, s3LogConfig)
	b.addLogVolume(mainContainer)
	b.addSparkDefaultConfigVolume(mainContainer)

	b.AddContainer(mainContainer.Build())

	if b.ClusteerConfig.Authentication != nil && b.ClusteerConfig.Authentication.Oidc != nil {
		oidcContainer, err := b.getOidcContainer(ctx)
		if err != nil {
			return nil, err
		}
		b.AddContainer(oidcContainer)
	}

	if b.ClusteerConfig != nil && b.ClusteerConfig.VectorAggregatorConfigMapName != "" {
		vectorBuilder := builder.NewVector(
			ConfigVolumeName,
			LogVolumeName,
			b.GetImage(),
		)

		b.AddContainer(vectorBuilder.GetContainer())
		b.AddVolumes(vectorBuilder.GetVolumes())
	}

	obj, err := b.GetObject()
	if err != nil {
		return nil, err
	}
	return obj, nil
}

func NewStatefulSetReconciler(
	client *resourceClient.Client,
	roleGroupInfo reconciler.RoleGroupInfo,
	clusterConfig *shsv1alpha1.ClusterConfigSpec,
	ports []corev1.ContainerPort,
	image *util.Image,
	replicas *int32,
	stopped bool,
	overrides *commonsv1alpha1.OverridesSpec,
	roleGroupConfig *commonsv1alpha1.RoleGroupConfigSpec,
	options ...builder.Option,
) (*reconciler.StatefulSet, error) {

	b := NewStatefulSetBuilder(
		client,
		roleGroupInfo.GetFullName(),
		clusterConfig,
		replicas,
		ports,
		image,
		overrides,
		roleGroupConfig,
		options...,
	)

	return reconciler.NewStatefulSet(
		client,
		b,
		stopped,
	), nil
}
