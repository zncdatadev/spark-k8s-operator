/*
Copyright 2023 zncdatadev.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package historyserver

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/zncdatadev/operator-go/pkg/builder"
	opgoconfig "github.com/zncdatadev/operator-go/pkg/config"
	"github.com/zncdatadev/operator-go/pkg/constant"
	"github.com/zncdatadev/operator-go/pkg/productlogging"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	shsv1alpha1 "github.com/zncdatadev/spark-k8s-operator/api/v1alpha1"
	"github.com/zncdatadev/spark-k8s-operator/internal/util/version"
)

// Compile-time proof that SparkHistoryServer wires framework-owned vector.yaml generation.
var _ reconciler.VectorAggregatorProvider = (*shsv1alpha1.SparkHistoryServer)(nil)

// SparkHistoryRoleGroupHandler builds the history server role group resources. It embeds the
// SDK's BaseRoleGroupHandler so the framework owns resource orchestration — ConfigMap (merged
// config plus the log4j2/vector files), Services, the StatefulSet (with sidecars, security
// context and overrides applied by the framework) and the role PDB. The override below adds
// the spark-specific pieces: spark-defaults.conf content (S3 event-log location, cleaner),
// the history server start script, the oauth2-proxy sidecar and the metrics Service.
type SparkHistoryRoleGroupHandler struct {
	*reconciler.BaseRoleGroupHandler[*shsv1alpha1.SparkHistoryServer]
}

var _ reconciler.RoleGroupHandler[*shsv1alpha1.SparkHistoryServer] = &SparkHistoryRoleGroupHandler{}

// NewSparkHistoryRoleGroupHandler creates the handler and configures the framework defaults.
func NewSparkHistoryRoleGroupHandler(scheme *runtime.Scheme) *SparkHistoryRoleGroupHandler {
	base := reconciler.NewBaseRoleGroupHandler[*shsv1alpha1.SparkHistoryServer]("", scheme)

	// The container name must match the per-container logging key (logging.containers.node)
	// and is asserted by the e2e suites.
	base.MainContainerName = shsv1alpha1.RoleNode
	base.ExtraLabels["app.kubernetes.io/name"] = "sparkhistoryserver"

	// Declarative logging: the framework renders the log4j2 config into the ConfigMap as
	// "log4j2.properties" (in-container /kubedoop/config/log4j2.properties) with the rolling
	// file pinned to "spark.log4j2.xml" — both names are e2e/vector contract.
	base.LoggingContainers = []productlogging.ContainerLogging{
		{
			Container:   shsv1alpha1.RoleNode,
			Framework:   productlogging.LoggingFrameworkLog4j2,
			FileName:    LogConfigFileName,
			LogFileName: LogFileName,
			Pattern:     ConsoleConversionPattern,
		},
	}

	// spark-defaults.conf renders as Java properties (Spark loads it via java.util.Properties,
	// so key=value is equivalent to the conventional whitespace separator), sorted for
	// deterministic output.
	base.ConfigGenerator = opgoconfig.NewMultiFormatConfigGenerator()
	base.ConfigGenerator.RegisterDefaultFormats()
	base.ConfigGenerator.RegisterFormat(".conf", opgoconfig.NewPropertiesAdapter())

	base.SetRoleContainerPorts(shsv1alpha1.RoleNode, []corev1.ContainerPort{
		{
			Name:          HttpPortName,
			ContainerPort: HttpPort,
			Protocol:      corev1.ProtocolTCP,
		},
		{
			Name:          MetricsPortName,
			ContainerPort: MetricsPort,
			Protocol:      corev1.ProtocolTCP,
		},
	})
	base.SetRoleServicePorts(shsv1alpha1.RoleNode, []corev1.ServicePort{
		{
			Name:       HttpPortName,
			Port:       HttpPort,
			TargetPort: intstr.FromString(HttpPortName),
			Protocol:   corev1.ProtocolTCP,
		},
		{
			Name:       MetricsPortName,
			Port:       MetricsPort,
			TargetPort: intstr.FromString(MetricsPortName),
			Protocol:   corev1.ProtocolTCP,
		},
	})

	return &SparkHistoryRoleGroupHandler{BaseRoleGroupHandler: base}
}

// BuildResources delegates the bulk to the framework, then applies the spark-specific pieces.
func (h *SparkHistoryRoleGroupHandler) BuildResources(
	ctx context.Context,
	k8sClient ctrlclient.Client,
	cr *shsv1alpha1.SparkHistoryServer,
	buildCtx *reconciler.RoleGroupBuildContext,
) (*reconciler.RoleGroupResources, error) {
	clusterConfig := cr.Spec.ClusterConfig
	if clusterConfig == nil || clusterConfig.LogFileDirectory == nil {
		return nil, fmt.Errorf("spec.clusterConfig.logFileDirectory is required")
	}

	// Resolve the CR-driven image before delegating to the framework: the base BuildResources
	// propagates it to the StatefulSet and the registered sidecars (Vector). Both fields are
	// assigned unconditionally — the handler is a singleton across CRs, so a CR omitting
	// pullPolicy must not inherit the previous CR's value.
	h.Image = resolveImage(cr.Spec.Image)
	h.ImagePullPolicy = corev1.PullIfNotPresent
	if cr.Spec.Image != nil && cr.Spec.Image.PullPolicy != "" {
		h.ImagePullPolicy = cr.Spec.Image.PullPolicy
	}

	// S3 event-log location: resolve the bucket/connection chain, deliver credentials via the
	// secret-operator CSI volume, and contribute the spark-defaults properties.
	s3LogConfig, err := resolveS3LogConfig(ctx, k8sClient, cr.GetNamespace(), clusterConfig.LogFileDirectory.S3)
	if err != nil {
		return nil, err
	}
	if provisioner := s3LogConfig.CredentialsProvisioner(); provisioner != nil {
		buildCtx.VolumeProviders = append(buildCtx.VolumeProviders, provisioner)
	}

	// Contribute the product-computed configuration as the lowest-precedence layer: keys the
	// user already set via configOverrides are left untouched, so CRD overrides always win.
	sparkDefaults := s3LogConfig.SparkDefaults()
	cleaner, err := cleanerEnabled(cr.Spec.Node, buildCtx.RoleGroupName)
	if err != nil {
		return nil, err
	}
	if cleaner {
		sparkDefaults["spark.history.fs.cleaner.enabled"] = trueValue
	}
	ensureConfigProperties(buildCtx, SparkDefaultsFileName, sparkDefaults)

	// OIDC authentication: front the UI with an oauth2-proxy native sidecar.
	oidcProvider, err := resolveOIDCProvider(ctx, k8sClient, cr.GetNamespace(), clusterConfig.Authentication)
	if err != nil {
		return nil, err
	}
	if oidcProvider != nil {
		registerOIDCSidecar(buildCtx.SidecarManager, oidcProvider, clusterConfig.Authentication.Oidc, string(cr.GetUID()))
	}

	resources, err := h.BaseRoleGroupHandler.BuildResources(ctx, k8sClient, cr, buildCtx)
	if err != nil {
		return nil, err
	}

	h.customizeStatefulSet(resources, s3LogConfig)
	h.customizeService(resources, clusterConfig, oidcProvider != nil)

	// Prometheus-scrapable metrics Service ("<resource>-metrics"); its shape — headless,
	// prometheus.io annotations, port/targetPort named "http" — is asserted by the e2e suite.
	resources.MetricsService = builder.NewMetricsServiceBuilder(
		buildCtx.ResourceName,
		buildCtx.ClusterNamespace,
		MetricsPort,
		resources.StatefulSet.Labels,
	).
		WithSelector(h.SelectorLabels(buildCtx)).
		WithPortName(HttpPortName).
		WithTargetPortName(HttpPortName).
		Build()

	return resources, nil
}

// customizeStatefulSet applies the history server start script, environment and probes on
// top of the framework-built StatefulSet.
func (h *SparkHistoryRoleGroupHandler) customizeStatefulSet(resources *reconciler.RoleGroupResources, s3LogConfig *S3LogConfig) {
	sts := resources.StatefulSet
	if sts == nil {
		return
	}

	containers := sts.Spec.Template.Spec.Containers
	for i := range containers {
		if containers[i].Name != shsv1alpha1.RoleNode {
			continue
		}
		main := &containers[i]

		// No -x: xtrace would echo the expanded AWS credential exports into the container log.
		main.Command = []string{"/bin/bash", "-euo", "pipefail", "-c"}
		main.Args = []string{h.mainContainerScript(s3LogConfig)}

		// Product defaults first, framework-applied envOverrides last so overrides win.
		main.Env = append(h.mainContainerEnv(), main.Env...)

		probe := func() *corev1.Probe {
			return &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromString(HttpPortName)},
				},
				InitialDelaySeconds: 10,
				TimeoutSeconds:      5,
				PeriodSeconds:       10,
			}
		}
		main.ReadinessProbe = probe()
		main.LivenessProbe = probe()
		break
	}
}

// customizeService maps the CRD listenerClass onto the client Service type and exposes the
// oauth2-proxy port when OIDC authentication is enabled (the e2e OIDC flow connects to the
// role group Service on port 4180).
func (h *SparkHistoryRoleGroupHandler) customizeService(resources *reconciler.RoleGroupResources, clusterConfig *shsv1alpha1.ClusterConfigSpec, oidcEnabled bool) {
	service := resources.Service
	if service == nil {
		return
	}

	switch clusterConfig.ListenerClass {
	case "external-unstable":
		service.Spec.Type = corev1.ServiceTypeNodePort
	case "external-stable":
		service.Spec.Type = corev1.ServiceTypeLoadBalancer
	default:
		// cluster-internal: ClusterIP (the Kubernetes default).
	}

	if oidcEnabled {
		// Numeric target port: the proxy is a native sidecar (init container), so a named
		// targetPort would depend on init-container port-name resolution.
		service.Spec.Ports = append(service.Spec.Ports, corev1.ServicePort{
			Name:       OidcPortName,
			Port:       OidcPort,
			TargetPort: intstr.FromInt(OidcPort),
			Protocol:   corev1.ProtocolTCP,
		})
	}
}

// mainContainerScript renders the history server start script. The config ConfigMap is
// mounted read-only, so it is copied to a writable directory first; the S3 credentials are
// exported as AWS SDK env vars; the history server is exec'd so it receives SIGTERM directly.
func (h *SparkHistoryRoleGroupHandler) mainContainerScript(s3LogConfig *S3LogConfig) string {
	steps := []string{
		"mkdir -p " + constant.KubedoopConfigDir,
		"cp -RL " + path.Join(constant.KubedoopConfigDirMount, "*") + " " + constant.KubedoopConfigDir,
	}

	if exports := s3LogConfig.CredentialsExportScript(); exports != "" {
		steps = append(steps, exports)
	}

	steps = append(steps,
		"exec "+path.Join(constant.KubedoopRoot, "spark/sbin/start-history-server.sh")+
			" --properties-file "+path.Join(constant.KubedoopConfigDir, SparkDefaultsFileName),
	)

	return strings.Join(steps, "\n")
}

// mainContainerEnv renders the history server container environment: foreground mode, the
// extra-jars classpath, and SPARK_HISTORY_OPTS carrying the log4j2 config plus the JMX
// prometheus javaagent serving the metrics port.
func (h *SparkHistoryRoleGroupHandler) mainContainerEnv() []corev1.EnvVar {
	historyOpts := []string{
		"-Dlog4j.configurationFile=" + path.Join(constant.KubedoopConfigDir, LogConfigFileName),
		fmt.Sprintf("-javaagent:%s=%d:%s",
			path.Join(constant.KubedoopJmxDir, "jmx_prometheus_javaagent.jar"),
			MetricsPort,
			path.Join(constant.KubedoopJmxDir, "config.yaml")),
	}

	return []corev1.EnvVar{
		{
			Name:  "SPARK_NO_DAEMONIZE",
			Value: trueValue,
		},
		{
			Name:  "SPARK_DAEMON_CLASSPATH",
			Value: path.Join(constant.KubedoopRoot, "spark/extra-jars/*"),
		},
		{
			Name:  "SPARK_HISTORY_OPTS",
			Value: strings.Join(historyOpts, " "),
		},
	}
}

// cleanerEnabled resolves the effective cleaner flag for a role group (role group config
// wins over role config) and validates the singleton constraints: the event-log cleaner must
// run at most once per cluster, so a role-level cleaner is rejected with multiple role
// groups, and an effective cleaner is rejected on a role group with more than one replica.
func cleanerEnabled(role *shsv1alpha1.RoleSpec, roleGroupName string) (bool, error) {
	if role == nil {
		return false, nil
	}

	roleCleaner := role.Config != nil && role.Config.Cleaner != nil && *role.Config.Cleaner
	if roleCleaner && len(role.RoleGroups) > 1 {
		return false, fmt.Errorf("role-level cleaner is not allowed with multiple role groups: enable it on exactly one role group instead")
	}

	roleGroup := role.RoleGroups[roleGroupName]
	if roleGroup == nil {
		return false, nil
	}

	effective := roleCleaner
	if roleGroup.Config != nil && roleGroup.Config.Cleaner != nil {
		effective = *roleGroup.Config.Cleaner
	}
	if !effective {
		return false, nil
	}

	replicas := int32(1)
	if roleGroup.Replicas != nil {
		replicas = *roleGroup.Replicas
	}
	if replicas > 1 {
		return false, fmt.Errorf("cleaner is enabled for role group %q but replicas is %d: the cleaner must run at most once, use one replica", roleGroupName, replicas)
	}

	return true, nil
}

// ensureConfigProperties merges product-computed properties into the merged config file as
// the lowest-precedence layer: only keys absent from the user's configOverrides are set.
func ensureConfigProperties(buildCtx *reconciler.RoleGroupBuildContext, fileName string, properties map[string]string) {
	if buildCtx.MergedConfig == nil {
		return
	}
	if buildCtx.MergedConfig.ConfigFiles == nil {
		buildCtx.MergedConfig.ConfigFiles = map[string]map[string]string{}
	}
	file := buildCtx.MergedConfig.ConfigFiles[fileName]
	if file == nil {
		file = map[string]string{}
		buildCtx.MergedConfig.ConfigFiles[fileName] = file
	}
	for k, v := range properties {
		if _, exists := file[k]; !exists {
			file[k] = v
		}
	}
}

// resolveImage resolves the history server container image from the CR spec with the
// platform defaults (repo, product version) and the operator build version as the kubedoop
// version.
func resolveImage(image *shsv1alpha1.ImageSpec) string {
	if image == nil {
		image = &shsv1alpha1.ImageSpec{}
	}
	if image.Custom != "" {
		return image.Custom
	}

	repo := image.Repo
	if repo == "" {
		repo = shsv1alpha1.DefaultRepository
	}
	productVersion := image.ProductVersion
	if productVersion == "" {
		productVersion = shsv1alpha1.DefaultProductVersion
	}
	kubedoopVersion := image.KubedoopVersion
	if kubedoopVersion == "" {
		kubedoopVersion = version.BuildVersion
	}

	return fmt.Sprintf("%s/%s:%s-kubedoop%s", repo, shsv1alpha1.DefaultProductName, productVersion, kubedoopVersion)
}
