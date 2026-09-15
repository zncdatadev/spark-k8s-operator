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
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	commonsv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/commons/v1alpha1"
	"github.com/zncdatadev/operator-go/pkg/builder"
	opgoconfig "github.com/zncdatadev/operator-go/pkg/config"
	"github.com/zncdatadev/operator-go/pkg/constant"
	"github.com/zncdatadev/operator-go/pkg/productlogging"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	"github.com/zncdatadev/operator-go/pkg/sidecar"
	"github.com/zncdatadev/operator-go/pkg/vector"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/utils/ptr"
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

// operator-go v0.13 reserves nine characters for its "-headless" Service and hashes longer
// names. Spark deliberately keeps the v0.12 client-Service identity instead, so crossing this
// boundary would silently create a second, hashed workload during an upgrade.
const (
	maxCompatibleRoleGroupResourceNameLength = 54
	kubedoopImageUID                         = int64(1001)
	kubedoopImageGID                         = int64(1001)
	oauth2ProxyImageUID                      = int64(65532)
)

var (
	_ reconciler.RoleGroupHandler[*shsv1alpha1.SparkHistoryServer]  = &SparkHistoryRoleGroupHandler{}
	_ reconciler.RoleProvider[*shsv1alpha1.SparkHistoryServer]      = &SparkHistoryRoleGroupHandler{}
	_ reconciler.RoleGroupResolver[*shsv1alpha1.SparkHistoryServer] = &SparkHistoryRoleGroupHandler{}
)

// ImageDefaults supplies the image fields omitted by spec.image. The values are read by the
// reconciler on every pass, so an operator upgrade moves unpinned clusters to the image released
// with that operator while explicit CR fields retain precedence.
func ImageDefaults() commonsv1alpha1.ImageSpec {
	return commonsv1alpha1.ImageSpec{
		Repo:            shsv1alpha1.DefaultRepository,
		ProductVersion:  shsv1alpha1.DefaultProductVersion,
		KubedoopVersion: version.BuildVersion,
		PullPolicy:      corev1.PullIfNotPresent,
	}
}

// NewSparkHistoryRoleGroupHandler creates the handler and configures the framework defaults.
func NewSparkHistoryRoleGroupHandler(scheme *runtime.Scheme) *SparkHistoryRoleGroupHandler {
	base := reconciler.NewBaseRoleGroupHandler[*shsv1alpha1.SparkHistoryServer](scheme)

	// spark-defaults.conf renders as Java properties (Spark loads it via java.util.Properties,
	// so key=value is equivalent to the conventional whitespace separator), sorted for
	// deterministic output.
	base.ConfigGenerator = opgoconfig.NewMultiFormatConfigGenerator()
	base.ConfigGenerator.RegisterDefaultFormats()
	base.ConfigGenerator.RegisterFormat(".conf", opgoconfig.NewPropertiesAdapter())

	// The legacy operator ran the image as root. Keep that product contract through the framework
	// migration; podOverrides are still applied afterwards and can harden individual clusters.
	base.WithSecurityContext(&corev1.SecurityContext{
		RunAsUser:                ptr.To[int64](0),
		RunAsGroup:               ptr.To[int64](0),
		AllowPrivilegeEscalation: ptr.To(false),
	}, nil)

	return &SparkHistoryRoleGroupHandler{BaseRoleGroupHandler: base}
}

// DeclareRoles implements reconciler.RoleProvider. Role shape belongs here so the framework can
// apply user pod and CLI overrides after the product declaration instead of having Spark rewrite
// the assembled container after those overrides have already been merged.
func (h *SparkHistoryRoleGroupHandler) DeclareRoles(
	ctx context.Context,
	k8sClient ctrlclient.Client,
	cr *shsv1alpha1.SparkHistoryServer,
) (reconciler.RoleCatalog, error) {
	clusterConfig := cr.Spec.ClusterConfig
	if clusterConfig == nil || clusterConfig.LogFileDirectory == nil {
		return nil, fmt.Errorf("spec.clusterConfig.logFileDirectory is required")
	}

	s3LogConfig, err := resolveS3LogConfig(
		ctx, k8sClient, cr.GetNamespace(), clusterConfig.LogFileDirectory.S3)
	if err != nil {
		return nil, err
	}

	probe := func() *corev1.Probe {
		return &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(HttpPort)},
			},
			InitialDelaySeconds: 10,
			TimeoutSeconds:      5,
			PeriodSeconds:       10,
		}
	}

	// v0.12 applied INFO thresholds to both appenders even when logging was omitted. Keep those
	// defaults in the fold so an explicit role or role-group level still wins. Setting the
	// aggregator ConfigMap was also the sole switch for the Vector agent before v0.13.
	configDefaults := &commonsv1alpha1.RoleGroupConfigSpec{
		Logging: &commonsv1alpha1.LoggingSpec{
			Containers: map[string]commonsv1alpha1.LoggingConfigSpec{
				shsv1alpha1.RoleNode: {
					Console: &commonsv1alpha1.LogLevelSpec{Level: legacyLog4j2DefaultLevel},
					File:    &commonsv1alpha1.LogLevelSpec{Level: legacyLog4j2DefaultLevel},
				},
			},
		},
	}
	if clusterConfig.VectorAggregatorConfigMapName != "" {
		configDefaults.Logging.EnableVectorAgent = ptr.To(true)
	}

	return reconciler.RoleCatalog{
		shsv1alpha1.RoleNode: {
			MainContainerName: shsv1alpha1.RoleNode,
			ContainerPorts: []corev1.ContainerPort{
				{Name: HttpPortName, ContainerPort: HttpPort, Protocol: corev1.ProtocolTCP},
				{Name: MetricsPortName, ContainerPort: MetricsPort, Protocol: corev1.ProtocolTCP},
			},
			ServicePorts: []corev1.ServicePort{
				{
					Name: HttpPortName, Port: HttpPort,
					TargetPort: intstr.FromString(HttpPortName), Protocol: corev1.ProtocolTCP,
				},
				{
					Name: MetricsPortName, Port: MetricsPort,
					TargetPort: intstr.FromString(MetricsPortName), Protocol: corev1.ProtocolTCP,
				},
			},
			// The framework has no declared Args field, so the shell script and its sentinel live in
			// Command. BuildResources restores this operator's v0.12 cliOverrides-as-command contract
			// after the framework has assembled the Pod template.
			Command: []string{
				bashPath, "-euo", "pipefail", "-c",
				h.mainContainerScript(s3LogConfig), "spark-history-server",
			},
			ReadinessProbe: probe(),
			LivenessProbe:  probe(),
			LogProducers: []productlogging.ContainerLogging{
				{
					Container:   shsv1alpha1.RoleNode,
					Framework:   productlogging.LoggingFrameworkLog4j2,
					FileName:    LogConfigFileName,
					LogFileName: LogFileName,
					Pattern:     ConsoleConversionPattern,
				},
			},
			LogVolumeSize:  "30Mi",
			Env:            h.mainContainerEnv(),
			ConfigDefaults: configDefaults,
		},
	}, nil
}

// ResolveRoleGroup maps user-facing cluster settings into the framework build before resources
// are rendered. The product pod layer restores Kubernetes' historical service-link default while
// remaining below role and role-group podOverrides.
func (h *SparkHistoryRoleGroupHandler) ResolveRoleGroup(
	_ context.Context,
	_ ctrlclient.Client,
	cr *shsv1alpha1.SparkHistoryServer,
	_ *reconciler.RoleGroupBuildContext,
) (*reconciler.Contribution, error) {
	contribution := &reconciler.Contribution{
		PodOverrides: &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{EnableServiceLinks: ptr.To(true)},
		},
	}
	if cr.Spec.ClusterConfig != nil {
		contribution.ListenerClass = cr.Spec.ClusterConfig.ListenerClass
	}
	return contribution, nil
}

// BuildResources delegates the bulk to the framework, then applies the spark-specific pieces.
func (h *SparkHistoryRoleGroupHandler) BuildResources(
	ctx context.Context,
	k8sClient ctrlclient.Client,
	cr *shsv1alpha1.SparkHistoryServer,
	buildCtx *reconciler.RoleGroupBuildContext,
) (*reconciler.RoleGroupResources, error) {
	if err := validateCompatibleResourceName(ctx, k8sClient, cr, buildCtx); err != nil {
		return nil, err
	}

	clusterConfig := cr.Spec.ClusterConfig
	if clusterConfig == nil || clusterConfig.LogFileDirectory == nil {
		return nil, fmt.Errorf("spec.clusterConfig.logFileDirectory is required")
	}

	// S3 event-log location: resolve the bucket/connection chain, deliver credentials via the
	// secret-operator CSI volume, and contribute the spark-defaults properties.
	s3LogConfig, err := resolveS3LogConfig(ctx, k8sClient, cr.GetNamespace(), clusterConfig.LogFileDirectory.S3)
	if err != nil {
		return nil, err
	}
	legacyIdentity, err := liveStatefulSetUsesLegacyIdentity(ctx, k8sClient, cr, buildCtx.ResourceName)
	if err != nil {
		return nil, fmt.Errorf("checking S3 upgrade compatibility: %w", err)
	}
	if provisioner := s3LogConfig.CredentialsProvisioner(); provisioner != nil {
		var volumeProvider reconciler.VolumeProvider = provisioner
		if legacyIdentity {
			volumeProvider = &legacyS3CredentialsProvider{SecretProvisioner: provisioner}
		}
		buildCtx.VolumeProviders = append(buildCtx.VolumeProviders, volumeProvider)
	}

	// Contribute the product-computed configuration as the lowest-precedence layer: keys the
	// user already set via configOverrides are left untouched, so CRD overrides always win.
	sparkDefaults := s3LogConfig.SparkDefaults()
	if legacyIdentity {
		// v0.12 always forced path-style S3 access, even when S3Connection.pathStyle was false
		// or omitted, and never rendered the resolved connection region. Preserve both
		// behaviors for an adopted workload; explicit configOverrides entries still win
		// because ensureConfigProperties never overwrites them.
		sparkDefaults["spark.hadoop.fs.s3a.path.style.access"] = trueValue
		delete(sparkDefaults, "spark.hadoop.fs.s3a.endpoint.region")
	}
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
		if err := validateLegacyOIDCSidecarCollision(buildCtx, legacyIdentity); err != nil {
			return nil, err
		}
		cookieSecretRef, err := ensureCookieSecret(ctx, k8sClient, cr, h.Scheme)
		if err != nil {
			return nil, err
		}
		registerOIDCSidecar(buildCtx.SidecarManager, oidcProvider, clusterConfig.Authentication.Oidc, cookieSecretRef)
	}
	legacySidecarOverrides := extractLegacySidecarPodOverrides(buildCtx)
	legacyMainMountOverrides := h.extractLegacyMainContainerMountOverrides(buildCtx)

	resources, err := h.buildBaseResourcesWithConfigOverrides(ctx, k8sClient, cr, buildCtx)
	if err != nil {
		return nil, err
	}
	if err := preserveLegacyGeneratedLog4j2(resources.ConfigMap, buildCtx); err != nil {
		return nil, err
	}
	if legacyIdentity {
		if err := h.applyLegacyConfigMountMode(resources.StatefulSet, buildCtx); err != nil {
			return nil, err
		}
	}
	if err := applyLegacyMainContainerMountOverrides(resources.StatefulSet, legacyMainMountOverrides); err != nil {
		return nil, err
	}
	applyNativeSidecarImageIdentities(resources.StatefulSet)
	if err := applyLegacySidecarPodOverrides(resources.StatefulSet, legacySidecarOverrides); err != nil {
		return nil, err
	}
	if err := preserveExistingServiceAccount(ctx, k8sClient, cr, resources, buildCtx); err != nil {
		return nil, err
	}

	if err := applyCompatibleWorkloadIdentity(ctx, k8sClient, cr, resources, buildCtx); err != nil {
		return nil, err
	}
	applyLegacyMemoryRequest(resources, buildCtx)
	applyLegacyCommandOverrides(cr, resources, buildCtx, h.mainContainerScript(s3LogConfig))

	// oauth2-proxy is a native sidecar. Once it is enabled the client Service must no longer
	// publish the unauthenticated Spark UI port, otherwise callers can bypass the proxy. Keep
	// unrelated ports (for example metrics) and replace only the UI entrypoint.
	if oidcProvider != nil && resources.Service != nil {
		servicePorts := make([]corev1.ServicePort, 0, len(resources.Service.Spec.Ports))
		for _, servicePort := range resources.Service.Spec.Ports {
			if servicePort.Name != HttpPortName {
				servicePorts = append(servicePorts, servicePort)
			}
		}
		resources.Service.Spec.Ports = append(servicePorts, corev1.ServicePort{
			Name: OidcPortName, Port: OidcPort,
			TargetPort: intstr.FromInt(OidcPort), Protocol: corev1.ProtocolTCP,
		})
	}

	// Prometheus-scrapable metrics Service ("<resource>-metrics"). The legacy controller
	// accidentally targeted the UI's http port. Preserve that shape only while adopting an
	// unauthenticated legacy workload; fresh workloads and OIDC clusters must expose JMX metrics.
	metricsTargetPortName := MetricsPortName
	if legacyIdentity && oidcProvider == nil {
		metricsTargetPortName = HttpPortName
	}
	resources.MetricsService = builder.NewMetricsServiceBuilder(
		buildCtx.ResourceName,
		buildCtx.ClusterNamespace,
		MetricsPort,
		resources.StatefulSet.Labels,
	).
		WithSelector(maps.Clone(resources.StatefulSet.Spec.Selector.MatchLabels)).
		WithPortName(HttpPortName).
		WithTargetPortName(metricsTargetPortName).
		Build()

	return resources, nil
}

// applyLegacyConfigMountMode retains the v0.12 config mount's API shape for an adopted
// StatefulSet. ConfigMap volumes are read-only at runtime regardless of this field, but changing
// the Pod template during a framework-only migration causes an avoidable rollout. A user-supplied
// mount entry remains authoritative, including an explicit false hidden by JSON omitempty.
func (h *SparkHistoryRoleGroupHandler) applyLegacyConfigMountMode(
	statefulSet *appsv1.StatefulSet,
	buildCtx *reconciler.RoleGroupBuildContext,
) error {
	if statefulSet == nil || buildCtx == nil {
		return fmt.Errorf("framework returned incomplete context for legacy config mount compatibility")
	}

	mainContainerName := buildCtx.Declaration.MainContainerName
	if mainContainerName == "" {
		mainContainerName = buildCtx.ResourceName
	}
	configMountPath := h.ConfigMountPath
	if configMountPath == "" {
		configMountPath = constant.KubedoopConfigDirMount
	}

	overrides := make([]corev1.VolumeMount, 0, 1)
	if buildCtx.MergedConfig != nil && buildCtx.MergedConfig.PodOverrides != nil {
		for _, container := range buildCtx.MergedConfig.PodOverrides.Spec.Containers {
			if container.Name != "" && container.Name != mainContainerName {
				continue
			}
			for _, mount := range container.VolumeMounts {
				if mount.Name == reconciler.ConfigVolumeName &&
					path.Clean(mount.MountPath) == path.Clean(configMountPath) {
					overrides = append(overrides, mount)
				}
			}
		}
	}

	for i := range statefulSet.Spec.Template.Spec.Containers {
		container := &statefulSet.Spec.Template.Spec.Containers[i]
		if container.Name != mainContainerName {
			continue
		}
		for j := range container.VolumeMounts {
			mount := &container.VolumeMounts[j]
			if mount.Name == reconciler.ConfigVolumeName &&
				path.Clean(mount.MountPath) == path.Clean(configMountPath) {
				mount.ReadOnly = false
				applyLegacyMountBooleans(container, overrides)
				return nil
			}
		}
		return fmt.Errorf("main container %q has no framework config mount at %q", mainContainerName, configMountPath)
	}
	return fmt.Errorf("framework did not build main container %q", mainContainerName)
}

// applyNativeSidecarImageIdentities keeps framework sidecars non-root when this product preserves
// its legacy root main-container contract. Vector uses the product image's named USER kubedoop,
// so it needs uid/gid 1001 whenever there is no usable numeric pod identity. oauth2-proxy's
// pinned distroless image already declares numeric uid 65532, except an explicit pod uid 0 would
// override it and conflict with the provider's RunAsNonRoot setting. Raw container podOverrides
// are replayed afterwards and can still replace either security context wholesale.
func applyNativeSidecarImageIdentities(statefulSet *appsv1.StatefulSet) {
	if statefulSet == nil {
		return
	}

	podSecurityContext := statefulSet.Spec.Template.Spec.SecurityContext
	for i := range statefulSet.Spec.Template.Spec.InitContainers {
		container := &statefulSet.Spec.Template.Spec.InitContainers[i]
		switch container.Name {
		case vector.VectorSidecarName:
			if container.SecurityContext == nil {
				container.SecurityContext = &corev1.SecurityContext{}
			}
			if container.SecurityContext.RunAsUser == nil &&
				(podSecurityContext == nil || podSecurityContext.RunAsUser == nil ||
					*podSecurityContext.RunAsUser == 0) {
				container.SecurityContext.RunAsUser = ptr.To(kubedoopImageUID)
			}
			if container.SecurityContext.RunAsGroup == nil &&
				(podSecurityContext == nil || podSecurityContext.RunAsGroup == nil) {
				container.SecurityContext.RunAsGroup = ptr.To(kubedoopImageGID)
			}
		case sidecar.OAuth2ProxySidecarName:
			if (podSecurityContext == nil || podSecurityContext.RunAsUser == nil ||
				*podSecurityContext.RunAsUser != 0) ||
				(container.SecurityContext != nil && container.SecurityContext.RunAsUser != nil) {
				continue
			}
			if container.SecurityContext == nil {
				container.SecurityContext = &corev1.SecurityContext{}
			}
			container.SecurityContext.RunAsUser = ptr.To(oauth2ProxyImageUID)
		}
	}
}

// validateLegacyOIDCSidecarCollision distinguishes a v0.12 custom regular container from the
// v0.13 native sidecar that happens to use the same name. The legacy operator generated only an
// "oidc" container, so an existing workload's podOverrides entry named "oauth2-proxy" necessarily
// addressed a separate user container. Absorbing it into the native sidecar would silently change
// the workload. Fresh v0.13 clusters have no such ambiguity and may patch the native name directly.
func validateLegacyOIDCSidecarCollision(
	buildCtx *reconciler.RoleGroupBuildContext,
	legacyIdentity bool,
) error {
	if !legacyIdentity || buildCtx == nil || buildCtx.MergedConfig == nil ||
		buildCtx.MergedConfig.PodOverrides == nil {
		return nil
	}

	for _, container := range buildCtx.MergedConfig.PodOverrides.Spec.Containers {
		if container.Name == sidecar.OAuth2ProxySidecarName {
			return reconciler.NewValidationError(
				"podOverrides",
				buildCtx.RoleName,
				buildCtx.RoleGroupName,
				fmt.Errorf(
					"legacy workload has a regular container named %q, which collides with the v0.13 native OIDC sidecar; "+
						"rename or migrate that custom container before upgrade (only the legacy %q container can be adopted automatically)",
					sidecar.OAuth2ProxySidecarName,
					OidcPortName,
				),
			)
		}
	}
	return nil
}

// buildBaseResourcesWithConfigOverrides preserves the legacy ability to replace framework-owned
// logging files through configOverrides. operator-go v0.13 deliberately rejects a collision with
// its generated log4j2.properties/vector.yaml, so render those user files separately, build the
// framework resources without the colliding keys, then restore the user-rendered content as the
// highest-precedence layer. A vector.yaml override does not replace the independent requirement
// for a valid enabled Vector pipeline and aggregator discovery.
func (h *SparkHistoryRoleGroupHandler) buildBaseResourcesWithConfigOverrides(
	ctx context.Context,
	k8sClient ctrlclient.Client,
	cr *shsv1alpha1.SparkHistoryServer,
	buildCtx *reconciler.RoleGroupBuildContext,
) (*reconciler.RoleGroupResources, error) {
	if buildCtx == nil || buildCtx.MergedConfig == nil || len(buildCtx.MergedConfig.ConfigFiles) == 0 {
		return h.BaseRoleGroupHandler.BuildResources(ctx, k8sClient, cr, buildCtx)
	}

	loggingOverrides := map[string]map[string]string{}
	for _, fileName := range []string{LogConfigFileName, vector.VectorConfigFileName} {
		if file, exists := buildCtx.MergedConfig.ConfigFiles[fileName]; exists {
			loggingOverrides[fileName] = file
		}
	}
	if len(loggingOverrides) == 0 {
		return h.BaseRoleGroupHandler.BuildResources(ctx, k8sClient, cr, buildCtx)
	}

	ctxCopy := *buildCtx
	mergedCopy := *buildCtx.MergedConfig
	mergedCopy.ConfigFiles = maps.Clone(buildCtx.MergedConfig.ConfigFiles)
	for fileName := range loggingOverrides {
		delete(mergedCopy.ConfigFiles, fileName)
	}
	ctxCopy.MergedConfig = &mergedCopy

	resources, err := h.BaseRoleGroupHandler.BuildResources(ctx, k8sClient, cr, &ctxCopy)
	if err != nil {
		return nil, err
	}
	if resources.ConfigMap == nil {
		return nil, fmt.Errorf("framework returned no ConfigMap for logging overrides")
	}
	rendered, err := h.ConfigGenerator.GenerateFiles(loggingOverrides)
	if err != nil {
		return nil, fmt.Errorf("rendering logging configOverrides: %w", err)
	}
	for fileName, content := range rendered {
		resources.ConfigMap.Data[fileName] = content
	}
	return resources, nil
}

// extractLegacySidecarPodOverrides removes old regular-container sidecar entries before the
// framework assembles the Pod. Leaving them in spec.containers would create an image-less phantom
// container because v0.13 injects sidecars as restartable init containers. The extracted full
// Container patches are applied after injection, retaining Kubernetes strategic-merge semantics
// for command, args, env/valueFrom, resources, lifecycle, mounts, ports, security and probes.
func extractLegacySidecarPodOverrides(
	buildCtx *reconciler.RoleGroupBuildContext,
) []corev1.Container {
	if buildCtx == nil || buildCtx.SidecarManager == nil || buildCtx.MergedConfig == nil ||
		buildCtx.MergedConfig.PodOverrides == nil {
		return nil
	}

	overrides := buildCtx.MergedConfig.PodOverrides.DeepCopy()
	regularContainers := make([]corev1.Container, 0, len(overrides.Spec.Containers))
	sidecarOverrides := make([]corev1.Container, 0, 2)
	for i := range overrides.Spec.Containers {
		override := &overrides.Spec.Containers[i]
		targetName := override.Name
		if targetName == OidcPortName {
			targetName = sidecar.OAuth2ProxySidecarName
		}
		provider, registered := buildCtx.SidecarManager.GetProvider(targetName)
		legacyVectorWasEnabled := buildCtx.Declaration.ConfigDefaults != nil &&
			vector.IsAgentEnabled(buildCtx.Declaration.ConfigDefaults.Logging)
		if targetName == vector.VectorSidecarName && !registered && legacyVectorWasEnabled {
			// A folded logging.enableVectorAgent=false explicitly disables the legacy Vector
			// container. Drop its old patch with the sidecar instead of passing an image-less
			// "vector" entry to the regular-container merge.
			continue
		}
		if !registered || (targetName != sidecar.OAuth2ProxySidecarName && targetName != vector.VectorSidecarName) {
			regularContainers = append(regularContainers, *override)
			continue
		}

		// GetProvider is also the enablement check; keep the value deliberately referenced so a
		// future manager implementation cannot turn this into name-only adoption.
		_ = provider
		migrated := override.DeepCopy()
		migrated.Name = targetName
		if targetName == vector.VectorSidecarName {
			translateLegacyVectorMounts(migrated.VolumeMounts)
		}
		sidecarOverrides = append(sidecarOverrides, *migrated)
	}
	overrides.Spec.Containers = regularContainers
	buildCtx.MergedConfig.PodOverrides = overrides
	return sidecarOverrides
}

// extractLegacyMainContainerMountOverrides preserves the v0.12 strategic-merge contract for the
// narrow case v0.13 rejects: a main-container override replaces a framework mount by mountPath.
// Only replacements backed by a declared volume are extracted. Every other mount remains in the
// framework input so its normal dangling-volume and ownership validation still applies.
func (h *SparkHistoryRoleGroupHandler) extractLegacyMainContainerMountOverrides(
	buildCtx *reconciler.RoleGroupBuildContext,
) *corev1.Container {
	if buildCtx == nil || buildCtx.MergedConfig == nil || buildCtx.MergedConfig.PodOverrides == nil {
		return nil
	}

	mainContainerName := buildCtx.Declaration.MainContainerName
	if mainContainerName == "" {
		mainContainerName = buildCtx.ResourceName
	}

	frameworkMounts := map[string]string{}
	configMountPath := h.ConfigMountPath
	if configMountPath == "" {
		configMountPath = constant.KubedoopConfigDirMount
	}
	frameworkMounts[configMountPath] = reconciler.ConfigVolumeName

	declaredVolumes := map[string]struct{}{reconciler.ConfigVolumeName: {}}
	for _, provider := range buildCtx.VolumeProviders {
		if provider == nil {
			continue
		}
		for _, volume := range provider.Volumes() {
			declaredVolumes[volume.Name] = struct{}{}
		}
		for _, mount := range provider.VolumeMounts() {
			frameworkMounts[mount.MountPath] = mount.Name
		}
	}
	for _, volume := range buildCtx.MergedConfig.PodOverrides.Spec.Volumes {
		declaredVolumes[volume.Name] = struct{}{}
	}

	overrides := buildCtx.MergedConfig.PodOverrides.DeepCopy()
	extracted := &corev1.Container{Name: mainContainerName}
	for i := range overrides.Spec.Containers {
		container := &overrides.Spec.Containers[i]
		if container.Name != "" && container.Name != mainContainerName {
			continue
		}

		kept := make([]corev1.VolumeMount, 0, len(container.VolumeMounts))
		for _, mount := range container.VolumeMounts {
			frameworkVolume, ownedPath := frameworkMounts[mount.MountPath]
			_, replacementDeclared := declaredVolumes[mount.Name]
			if !ownedPath || mount.Name == frameworkVolume || !replacementDeclared {
				kept = append(kept, mount)
				continue
			}
			extracted.VolumeMounts = append(extracted.VolumeMounts, *mount.DeepCopy())
		}
		container.VolumeMounts = kept
	}
	if len(extracted.VolumeMounts) == 0 {
		return nil
	}
	buildCtx.MergedConfig.PodOverrides = overrides
	return extracted
}

// applyLegacyMainContainerMountOverrides replays only the extracted mount patch after the v0.13
// Base builder has assembled and validated the Pod. Strategic merge keys VolumeMounts by
// mountPath, exactly like the v0.12 PodTemplate merge did.
func applyLegacyMainContainerMountOverrides(
	statefulSet *appsv1.StatefulSet,
	override *corev1.Container,
) error {
	if override == nil || len(override.VolumeMounts) == 0 {
		return nil
	}
	if statefulSet == nil {
		return fmt.Errorf("framework returned no StatefulSet for legacy main-container mount overrides")
	}

	for i := range statefulSet.Spec.Template.Spec.Containers {
		container := &statefulSet.Spec.Template.Spec.Containers[i]
		if container.Name != override.Name {
			continue
		}
		merged, err := strategicMergeContainer(container, override)
		if err != nil {
			return fmt.Errorf("applying legacy podOverrides mounts to main container %q: %w", override.Name, err)
		}
		applyLegacyMountBooleans(merged, override.VolumeMounts)
		*container = *merged
		return nil
	}
	return fmt.Errorf("legacy podOverrides target main container %q, but the framework did not build it", override.Name)
}

// applyLegacySidecarPodOverrides applies each complete legacy Container patch to the matching
// restartable init container. Probe baselines are reset to their v0.12 shape when that probe was
// explicitly overridden: this prevents a partial old readiness override from losing its HTTP
// handler and prevents a new default liveness handler from colliding with a legacy exec handler.
func applyLegacySidecarPodOverrides(
	statefulSet *appsv1.StatefulSet,
	overrides []corev1.Container,
) error {
	if statefulSet == nil || len(overrides) == 0 {
		return nil
	}

	for i := range overrides {
		override := &overrides[i]
		found := false
		for j := range statefulSet.Spec.Template.Spec.InitContainers {
			container := &statefulSet.Spec.Template.Spec.InitContainers[j]
			if container.Name != override.Name {
				continue
			}
			found = true

			base := container.DeepCopy()
			if override.ReadinessProbe != nil {
				base.ReadinessProbe = nil
				if override.Name == vector.VectorSidecarName {
					base.ReadinessProbe = legacyVectorReadinessProbe()
				}
			}
			if override.LivenessProbe != nil {
				base.LivenessProbe = nil
			}
			if override.StartupProbe != nil {
				base.StartupProbe = nil
			}
			if override.SecurityContext != nil {
				base.SecurityContext = nil
			}

			merged, err := strategicMergeContainer(base, override)
			if err != nil {
				return fmt.Errorf("applying legacy podOverrides to native sidecar %q: %w", override.Name, err)
			}
			if override.Name == sidecar.OAuth2ProxySidecarName && !slices.ContainsFunc(
				merged.Ports,
				func(port corev1.ContainerPort) bool { return port.ContainerPort == OidcPort },
			) {
				return fmt.Errorf(
					"podOverrides container %q cannot be migrated to native sidecar %q: "+
						"the merged oauth2-proxy container must expose port %d so the client Service remains routable",
					override.Name, override.Name, OidcPort,
				)
			}
			if override.Name == vector.VectorSidecarName {
				applyLegacyMountBooleans(merged, override.VolumeMounts)
			}
			// A legacy patch cannot turn a v0.13 sidecar back into a regular init container.
			merged.RestartPolicy = ptr.To(corev1.ContainerRestartPolicyAlways)
			*container = *merged
			break
		}
		if !found {
			return fmt.Errorf("legacy podOverrides target enabled sidecar %q, but the framework did not inject it", override.Name)
		}
	}
	return nil
}

func strategicMergeContainer(base, override *corev1.Container) (*corev1.Container, error) {
	baseJSON, err := json.Marshal(base)
	if err != nil {
		return nil, err
	}
	overrideJSON, err := json.Marshal(override)
	if err != nil {
		return nil, err
	}
	mergedJSON, err := strategicpatch.StrategicMergePatch(baseJSON, overrideJSON, corev1.Container{})
	if err != nil {
		return nil, err
	}
	result := &corev1.Container{}
	if err := json.Unmarshal(mergedJSON, result); err != nil {
		return nil, err
	}
	return result, nil
}

func legacyVectorReadinessProbe() *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/health",
				Port: intstr.FromInt(vector.VectorAPIPort),
			},
		},
		InitialDelaySeconds: 5,
		PeriodSeconds:       10,
		TimeoutSeconds:      1,
		SuccessThreshold:    1,
		FailureThreshold:    3,
	}
}

// translateLegacyVectorMounts maps the one Vector mount path changed by the framework migration.
// Strategic merge keys volumeMounts by mountPath, so without this translation a partial override
// of the old config mount would be appended and silently miss the config file Vector now reads.
func translateLegacyVectorMounts(mounts []corev1.VolumeMount) {
	for i := range mounts {
		if path.Clean(mounts[i].MountPath) != path.Clean(constant.KubedoopConfigDir) {
			continue
		}
		mounts[i].MountPath = vector.VectorConfigMountPath
		if mounts[i].Name == "config" {
			mounts[i].Name = vector.VectorConfigVolumeName
		}
	}
}

// StrategicMergePatch cannot retain an explicit false from the already-decoded PodTemplate because
// VolumeMount.readOnly has omitempty. The presence of an extracted mount entry is enough to recover
// the old intent, so copy the boolean explicitly using the same exact mountPath merge key.
func applyLegacyMountBooleans(container *corev1.Container, overrides []corev1.VolumeMount) {
	for _, override := range overrides {
		for i := range container.VolumeMounts {
			if container.VolumeMounts[i].MountPath == override.MountPath {
				container.VolumeMounts[i].ReadOnly = override.ReadOnly
				break
			}
		}
	}
}

// applyLegacyCommandOverrides retains this CRD's v0.12 meaning of cliOverrides: the non-empty slice
// replaces the main container command and clears its args. operator-go v0.13 intentionally treats
// the same field as arguments, so silently adopting the generic behavior would reinterpret every
// existing custom entrypoint. Pod overrides were applied after CLI overrides in v0.12 and remain
// the highest-precedence layer here. When only podOverrides command or args is set, the v0.12
// command/args split is reconstructed before that field is applied.
func applyLegacyCommandOverrides(
	cr *shsv1alpha1.SparkHistoryServer,
	resources *reconciler.RoleGroupResources,
	buildCtx *reconciler.RoleGroupBuildContext,
	legacyStartupScript string,
) {
	if cr == nil || cr.Spec.Node == nil || resources == nil || resources.StatefulSet == nil || buildCtx == nil {
		return
	}

	// operator-go v0.12 merged override slices by appending the role-group values to the role
	// values. v0.13's generic merger deliberately uses replacement, so reconstruct the historical
	// effective command from the typed CR instead of consuming the already-replaced CliArgs.
	var cliOverrides []string
	if cr.Spec.Node.OverridesSpec != nil {
		cliOverrides = append(cliOverrides, cr.Spec.Node.CliOverrides...)
	}
	if roleGroup := cr.Spec.Node.RoleGroups[buildCtx.RoleGroupName]; roleGroup != nil {
		if roleGroup.OverridesSpec != nil {
			cliOverrides = append(cliOverrides, roleGroup.CliOverrides...)
		}
	}
	mainContainerName := buildCtx.Declaration.MainContainerName
	var podOverride *corev1.Container
	if buildCtx.MergedConfig != nil && buildCtx.MergedConfig.PodOverrides != nil {
		for i := range buildCtx.MergedConfig.PodOverrides.Spec.Containers {
			candidate := &buildCtx.MergedConfig.PodOverrides.Spec.Containers[i]
			if candidate.Name == "" || candidate.Name == mainContainerName {
				podOverride = candidate
				break
			}
		}
	}
	if len(cliOverrides) == 0 &&
		(podOverride == nil || (podOverride.Command == nil && podOverride.Args == nil)) {
		return
	}

	for i := range resources.StatefulSet.Spec.Template.Spec.Containers {
		container := &resources.StatefulSet.Spec.Template.Spec.Containers[i]
		if container.Name != mainContainerName {
			continue
		}

		if len(cliOverrides) > 0 {
			container.Command = cliOverrides
			container.Args = nil
		} else {
			// v0.12 placed the startup script in Args. Reconstruct that baseline whenever an
			// existing podOverride touches command or args, then apply the explicit fields.
			container.Command = []string{bashPath, "-c"}
			container.Args = []string{legacyStartupScript}
		}
		if podOverride != nil {
			if podOverride.Command != nil {
				container.Command = append([]string(nil), podOverride.Command...)
			}
			if podOverride.Args != nil {
				container.Args = append([]string(nil), podOverride.Args...)
			}
		}
		return
	}
}

// preserveExistingServiceAccount avoids changing a live v0.12 workload's Kubernetes identity as
// a side effect of adopting GenericReconciler. The adopted baseline is stored as an annotation so
// an explicit v0.13 override can temporarily replace it and later be removed. A fresh v0.13
// StatefulSet has framework ownership and no baseline annotation, so removing an override returns
// it to the framework's per-CR ServiceAccount instead of pinning the last live value forever.
func preserveExistingServiceAccount(
	ctx context.Context,
	k8sClient ctrlclient.Client,
	cr *shsv1alpha1.SparkHistoryServer,
	resources *reconciler.RoleGroupResources,
	buildCtx *reconciler.RoleGroupBuildContext,
) error {
	if resources == nil || resources.StatefulSet == nil || buildCtx == nil {
		return nil
	}

	live := &appsv1.StatefulSet{}
	err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: resources.StatefulSet.Namespace,
		Name:      resources.StatefulSet.Name,
	}, live)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking existing StatefulSet service account: %w", err)
	}
	if !metav1.IsControlledBy(live, cr) {
		return nil
	}

	baseline, hasBaseline := live.Annotations[legacyServiceAccountAnnotation]
	explicitOverride := buildCtx.MergedConfig != nil && buildCtx.MergedConfig.PodOverrides != nil &&
		buildCtx.MergedConfig.PodOverrides.Spec.ServiceAccountName != ""
	if !hasBaseline && live.Labels[constant.LabelKubernetesManagedBy] == shsv1alpha1.GroupVersion.Group {
		// A legacy workload with an active override still had "default" underneath it: v0.12
		// rendered no ServiceAccount of its own. Persist that underlying baseline, not the live
		// overridden value, so removing the override later restores the v0.12 outcome.
		baseline = defaultServiceAccountName
		if !explicitOverride && live.Spec.Template.Spec.ServiceAccountName != "" {
			baseline = live.Spec.Template.Spec.ServiceAccountName
		}
		hasBaseline = true
	}
	if hasBaseline {
		if baseline == "" {
			baseline = defaultServiceAccountName
		}
		if resources.StatefulSet.Annotations == nil {
			resources.StatefulSet.Annotations = map[string]string{}
		}
		resources.StatefulSet.Annotations[legacyServiceAccountAnnotation] = baseline
	}
	if explicitOverride {
		return nil
	}
	if hasBaseline {
		resources.StatefulSet.Spec.Template.Spec.ServiceAccountName = baseline
		return nil
	}

	return nil
}

// validateCompatibleResourceName prevents a v0.12 workload from being duplicated under the
// truncated-and-hashed name introduced by operator-go v0.13. Existing 55-character base names
// require an explicit one-time migration; failing before any role-group resources are applied is
// safer than producing two StatefulSets that consume the same event-log directory.
func validateCompatibleResourceName(
	ctx context.Context,
	k8sClient ctrlclient.Client,
	cr *shsv1alpha1.SparkHistoryServer,
	buildCtx *reconciler.RoleGroupBuildContext,
) error {
	if buildCtx == nil {
		return fmt.Errorf("role group build context is required")
	}
	rawName := fmt.Sprintf("%s-%s-%s", buildCtx.ClusterName, buildCtx.RoleName, buildCtx.RoleGroupName)
	if len(rawName) <= maxCompatibleRoleGroupResourceNameLength || rawName == buildCtx.ResourceName {
		return nil
	}

	// A new v0.13 cluster has no natural-name object and can safely use the canonical hash. Only
	// an existing object controlled by this CR proves that adopting the hash would fork the
	// workload. Check every resource the old reconciler could have created before a partial pass.
	legacyObjects := []ctrlclient.Object{
		&appsv1.StatefulSet{},
		&corev1.Service{},
		&corev1.ConfigMap{},
	}
	for _, object := range legacyObjects {
		err := k8sClient.Get(ctx, types.NamespacedName{
			Namespace: buildCtx.ClusterNamespace,
			Name:      rawName,
		}, object)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("checking for legacy role group resource %q: %w", rawName, err)
		}
		if metav1.IsControlledBy(object, cr) {
			return fmt.Errorf(
				"legacy role group resource %q is %d characters; operator-go v0.13 would replace it with %q, "+
					"so this cluster requires the documented long-name migration before upgrade",
				rawName, len(rawName), buildCtx.ResourceName,
			)
		}
	}
	return nil
}

// BuildRolePodDisruptionBudget selects the stable identity labels shared by both legacy-upgraded
// and fresh framework workloads. managed-by differs between those two paths, so including it
// would make one PDB silently match no pods.
func (h *SparkHistoryRoleGroupHandler) BuildRolePodDisruptionBudget(
	buildCtx *reconciler.RoleBuildContext,
) *policyv1.PodDisruptionBudget {
	pdb := h.BaseRoleGroupHandler.BuildRolePodDisruptionBudget(buildCtx)
	if pdb != nil && pdb.Spec.Selector != nil {
		pdb.Spec.Selector.MatchLabels = map[string]string{
			constant.LabelKubernetesName:      AppName,
			constant.LabelKubernetesInstance:  buildCtx.ClusterName,
			constant.LabelKubernetesComponent: buildCtx.RoleName,
		}
		pdb.Labels[constant.LabelKubernetesName] = AppName
	}
	return pdb
}

// applyCompatibleWorkloadIdentity preserves the StatefulSet shape emitted by operator-go v0.12.
// An owned live StatefulSet whose immutable selector carries the legacy managed-by value keeps it;
// a fresh v0.13 object uses operator-go so the framework health manager can discover failing pods.
// Object metadata always keeps the v0.13 framework labels and slot markers for orphan cleanup.
func applyCompatibleWorkloadIdentity(
	ctx context.Context,
	k8sClient ctrlclient.Client,
	cr *shsv1alpha1.SparkHistoryServer,
	resources *reconciler.RoleGroupResources,
	buildCtx *reconciler.RoleGroupBuildContext,
) error {
	if resources == nil || resources.StatefulSet == nil || buildCtx == nil {
		return nil
	}

	selector := legacyRoleGroupSelector(buildCtx)
	legacyIdentity, err := liveStatefulSetUsesLegacyIdentity(ctx, k8sClient, cr, resources.StatefulSet.Name)
	if err != nil {
		return fmt.Errorf("checking existing StatefulSet workload identity: %w", err)
	}
	if !legacyIdentity {
		selector[constant.LabelKubernetesManagedBy] =
			resources.StatefulSet.Labels[constant.LabelKubernetesManagedBy]
	}

	if resources.ConfigMap != nil {
		resources.ConfigMap.Labels[constant.LabelKubernetesName] = AppName
	}
	if resources.Service != nil {
		resources.Service.Labels[constant.LabelKubernetesName] = AppName
	}
	if resources.HeadlessService != nil {
		resources.HeadlessService.Labels[constant.LabelKubernetesName] = AppName
	}
	if resources.StatefulSet != nil {
		resources.StatefulSet.Labels[constant.LabelKubernetesName] = AppName
		resources.StatefulSet.Spec.Template.Labels[constant.LabelKubernetesName] = AppName
		resources.StatefulSet.Spec.Selector.MatchLabels = maps.Clone(selector)
		maps.Copy(resources.StatefulSet.Spec.Template.Labels, selector)
		resources.StatefulSet.Spec.ServiceName = buildCtx.ResourceName
		resources.StatefulSet.Spec.PodManagementPolicy = appsv1.OrderedReadyPodManagement
	}
	if resources.Service != nil {
		resources.Service.Spec.Selector = maps.Clone(selector)
	}
	// The v0.12 workload used the client Service for StatefulSet identity. Avoid introducing an
	// unused extra Service and keep new installations rollback-compatible with that resource set.
	resources.HeadlessService = nil
	return nil
}

// liveStatefulSetUsesLegacyIdentity recognizes only an owned v0.12 workload. The immutable
// StatefulSet selector is authoritative after the first v0.13 apply, while the top-level label
// covers the initial adoption pass before v0.13 has rewritten object metadata.
func liveStatefulSetUsesLegacyIdentity(
	ctx context.Context,
	k8sClient ctrlclient.Client,
	cr *shsv1alpha1.SparkHistoryServer,
	name string,
) (bool, error) {
	live := &appsv1.StatefulSet{}
	err := k8sClient.Get(ctx, types.NamespacedName{Namespace: cr.Namespace, Name: name}, live)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !metav1.IsControlledBy(live, cr) {
		return false, nil
	}
	return (live.Spec.Selector != nil &&
		live.Spec.Selector.MatchLabels[constant.LabelKubernetesManagedBy] == shsv1alpha1.GroupVersion.Group) ||
		live.Labels[constant.LabelKubernetesManagedBy] == shsv1alpha1.GroupVersion.Group, nil
}

// applyLegacyMemoryRequest keeps the v0.12 ResourcesSpec contract: memory.limit constrains the
// container but does not implicitly reserve the same amount. operator-go v0.13 changed its base
// builder to request=limit. A request explicitly supplied through podOverrides remains the
// highest-precedence user choice and is therefore preserved.
func applyLegacyMemoryRequest(
	resources *reconciler.RoleGroupResources,
	buildCtx *reconciler.RoleGroupBuildContext,
) {
	if resources == nil || resources.StatefulSet == nil || buildCtx == nil {
		return
	}

	mainContainerName := buildCtx.Declaration.MainContainerName
	if mainContainerName == "" {
		mainContainerName = buildCtx.RoleName
	}
	if podOverrideRequestsMemory(buildCtx.MergedConfig, mainContainerName) {
		return
	}

	for index := range resources.StatefulSet.Spec.Template.Spec.Containers {
		container := &resources.StatefulSet.Spec.Template.Spec.Containers[index]
		if container.Name != mainContainerName {
			continue
		}
		delete(container.Resources.Requests, corev1.ResourceMemory)
		if len(container.Resources.Requests) == 0 {
			container.Resources.Requests = nil
		}
		return
	}
}

func podOverrideRequestsMemory(mergedConfig *opgoconfig.MergedConfig, mainContainerName string) bool {
	if mergedConfig == nil || mergedConfig.PodOverrides == nil {
		return false
	}
	for _, container := range mergedConfig.PodOverrides.Spec.Containers {
		if container.Name != "" && container.Name != mainContainerName {
			continue
		}
		if _, explicitlySet := container.Resources.Requests[corev1.ResourceMemory]; explicitlySet {
			return true
		}
	}
	return false
}

func legacyRoleGroupSelector(buildCtx *reconciler.RoleGroupBuildContext) map[string]string {
	labels := legacyRoleSelector(buildCtx.ClusterName, buildCtx.RoleName)
	labels[constant.LabelKubernetesRoleGroup] = buildCtx.RoleGroupName
	return labels
}

func legacyRoleSelector(clusterName, roleName string) map[string]string {
	return map[string]string{
		constant.LabelKubernetesName:      AppName,
		constant.LabelKubernetesInstance:  clusterName,
		constant.LabelKubernetesComponent: roleName,
		constant.LabelKubernetesManagedBy: shsv1alpha1.GroupVersion.Group,
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
			" --properties-file "+path.Join(constant.KubedoopConfigDir, SparkDefaultsFileName)+` "$@"`,
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

// cleanerEnabled resolves the effective cleaner flag for a role group (role group config wins
// over role config) and validates the whole role. Exactly zero or one role group may enable the
// event-log cleaner, and that group may have at most one replica.
func cleanerEnabled(role *shsv1alpha1.RoleSpec, roleGroupName string) (bool, error) {
	if role == nil {
		return false, nil
	}

	roleCleaner := role.Config != nil && role.Config.Cleaner != nil && *role.Config.Cleaner
	effectiveByGroup := make(map[string]bool, len(role.RoleGroups))
	enabledGroups := make([]string, 0, 1)
	for name, roleGroup := range role.RoleGroups {
		effective := roleCleaner
		if roleGroup != nil && roleGroup.Config != nil && roleGroup.Config.Cleaner != nil {
			effective = *roleGroup.Config.Cleaner
		}
		effectiveByGroup[name] = effective
		if !effective {
			continue
		}

		replicas := int32(1)
		if roleGroup != nil && roleGroup.Replicas != nil {
			replicas = *roleGroup.Replicas
		}
		if replicas > 1 {
			return false, fmt.Errorf("cleaner is enabled for role group %q but replicas is %d: the cleaner must run at most once, use one replica", name, replicas)
		}
		enabledGroups = append(enabledGroups, name)
	}
	if len(enabledGroups) > 1 {
		slices.Sort(enabledGroups)
		return false, fmt.Errorf("cleaner is enabled for multiple role groups %q: enable it on exactly one role group", enabledGroups)
	}

	return effectiveByGroup[roleGroupName], nil
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
