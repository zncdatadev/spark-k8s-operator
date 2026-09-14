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
	"maps"
	"net/url"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	authv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/authentication/v1alpha1"
	commonsv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/commons/v1alpha1"
	s3v1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/s3/v1alpha1"
	opgoconfig "github.com/zncdatadev/operator-go/pkg/config"
	"github.com/zncdatadev/operator-go/pkg/constant"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	opgos3 "github.com/zncdatadev/operator-go/pkg/s3"
	"github.com/zncdatadev/operator-go/pkg/sidecar"
	"github.com/zncdatadev/operator-go/pkg/vector"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	shsv1alpha1 "github.com/zncdatadev/spark-k8s-operator/api/v1alpha1"
)

const (
	testNamespace    = "test-ns"
	defaultRoleGroup = "default"
	defaultResource  = "sparkhistory-node-default"
	frameworkManager = "operator-go"
	minioName        = "minio"
	bucketName       = "spark-history"
	oidcClassName    = "oidc"
	oidcCredentials  = "oidc-credentials"
	cliWrapper       = "/cli-wrapper"
	roleGroupCLIArg  = "--group"
)

// authScheme registers the authentication.kubedoop.dev types on the scheme.
func authScheme(scheme *runtime.Scheme) error {
	return authv1alpha1.AddToScheme(scheme)
}

// keycloakAuthClass returns an AuthenticationClass fixture matching the e2e OIDC setup.
func keycloakAuthClass() *authv1alpha1.AuthenticationClass {
	return &authv1alpha1.AuthenticationClass{
		ObjectMeta: metav1.ObjectMeta{Name: oidcClassName, Namespace: testNamespace},
		Spec: authv1alpha1.AuthenticationClassSpec{
			AuthenticationProvider: &authv1alpha1.AuthenticationProvider{
				OIDC: &authv1alpha1.OIDCProvider{
					Hostname:     "keycloak.test-ns.svc.cluster.local",
					Port:         8080,
					RootPath:     "/realms/kubedoop",
					ProviderHint: "keycloak",
					Scopes:       []string{"openid", "email", "profile"},
				},
			},
		},
	}
}

func newScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	// corev1 is required beyond the CRDs: the handler creates the oauth2-proxy cookie
	// Secret and sets an owner reference on it.
	Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	Expect(shsv1alpha1.AddToScheme(scheme)).To(Succeed())
	Expect(s3v1alpha1.AddToScheme(scheme)).To(Succeed())
	return scheme
}

func newFakeClient(scheme *runtime.Scheme, objs ...ctrlclient.Object) ctrlclient.Client {
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func minioObjects() []ctrlclient.Object {
	return []ctrlclient.Object{
		&s3v1alpha1.S3Connection{
			ObjectMeta: metav1.ObjectMeta{Name: minioName, Namespace: testNamespace},
			Spec: s3v1alpha1.S3ConnectionSpec{
				Host:      minioName,
				Port:      9000,
				Region:    "us-east-1",
				PathStyle: true,
				Credentials: &commonsv1alpha1.Credentials{
					SecretClass: "s3-credentials",
				},
			},
		},
		&s3v1alpha1.S3Bucket{
			ObjectMeta: metav1.ObjectMeta{Name: bucketName, Namespace: testNamespace},
			Spec: s3v1alpha1.S3BucketSpec{
				BucketName: bucketName,
				Connection: &s3v1alpha1.S3BucketConnectionSpec{Reference: minioName},
			},
		},
	}
}

func testCR() *shsv1alpha1.SparkHistoryServer {
	return &shsv1alpha1.SparkHistoryServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sparkhistory",
			Namespace: testNamespace,
			UID:       types.UID("test-uid"),
		},
		Spec: shsv1alpha1.SparkHistoryServerSpec{
			ClusterConfig: &shsv1alpha1.ClusterConfigSpec{
				ListenerClass: "cluster-internal",
				LogFileDirectory: &shsv1alpha1.LogFileDirectorySpec{
					S3: &shsv1alpha1.S3Spec{
						Bucket: &shsv1alpha1.BucketSpec{Reference: bucketName},
						Prefix: "events",
					},
				},
			},
			Node: &shsv1alpha1.RoleSpec{
				RoleGroups: map[string]*shsv1alpha1.RoleGroupSpec{
					defaultRoleGroup: {Replicas: ptr.To(int32(1))},
				},
			},
		},
	}
}

func legacyOwnedStatefulSet(cr *shsv1alpha1.SparkHistoryServer) *appsv1.StatefulSet {
	labels := map[string]string{
		constant.LabelKubernetesName:      AppName,
		constant.LabelKubernetesInstance:  cr.Name,
		constant.LabelKubernetesComponent: shsv1alpha1.RoleNode,
		constant.LabelKubernetesRoleGroup: defaultRoleGroup,
		constant.LabelKubernetesManagedBy: shsv1alpha1.GroupVersion.Group,
	}
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      defaultResource,
			Namespace: cr.Namespace,
			Labels:    maps.Clone(labels),
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(
				cr, shsv1alpha1.GroupVersion.WithKind("SparkHistoryServer"),
			)},
		},
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: maps.Clone(labels)},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: maps.Clone(labels)}},
		},
	}
}

func testBuildContext(
	handler *SparkHistoryRoleGroupHandler,
	client ctrlclient.Client,
	cr *shsv1alpha1.SparkHistoryServer,
) *reconciler.RoleGroupBuildContext {
	catalog, err := handler.DeclareRoles(context.Background(), client, cr)
	Expect(err).NotTo(HaveOccurred())
	declaration := catalog[shsv1alpha1.RoleNode]

	contribution, err := handler.ResolveRoleGroup(context.Background(), client, cr, nil)
	Expect(err).NotTo(HaveOccurred())
	declaration.ListenerClass = contribution.ListenerClass

	return &reconciler.RoleGroupBuildContext{
		ClusterName:      cr.Name,
		ClusterNamespace: cr.Namespace,
		// The framework hands the handler a cloned, materialized map (never nil) so a
		// handler can write to it; mirror that here or the fixture is not the contract.
		ClusterLabels: map[string]string{},
		ClusterSpec:   cr.GetSpec(),
		RoleName:      shsv1alpha1.RoleNode,
		RoleGroupName: defaultRoleGroup,
		RoleGroupSpec: cr.GetSpec().Roles[shsv1alpha1.RoleNode].RoleGroups[defaultRoleGroup],
		MergedConfig: &opgoconfig.MergedConfig{
			PodOverrides: contribution.PodOverrides,
		},
		ResourceName:       reconciler.RoleGroupResourceName(cr.Name, shsv1alpha1.RoleNode, defaultRoleGroup),
		ServiceAccountName: "sparkhistoryserver-" + cr.Name,
		SidecarManager:     sidecar.NewSidecarManager(),
		Declaration:        declaration,
		ResolvedImage: reconciler.ResolvedImage{
			Reference:      "quay.io/zncdatadev/spark-k8s:3.5.5-kubedoop0.0.0-test",
			PullPolicy:     corev1.PullIfNotPresent,
			ProductVersion: shsv1alpha1.DefaultProductVersion,
		},
		ProductName: shsv1alpha1.DefaultProductName,
	}
}

var _ = Describe("BuildResources", func() {
	var handler *SparkHistoryRoleGroupHandler
	var cr *shsv1alpha1.SparkHistoryServer
	var scheme *runtime.Scheme

	BeforeEach(func() {
		scheme = newScheme()
		handler = NewSparkHistoryRoleGroupHandler(scheme)
		cr = testCR()
	})

	It("builds the e2e-contracted resource set", func() {
		client := newFakeClient(scheme, minioObjects()...)
		buildCtx := testBuildContext(handler, client, cr)

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())

		By("naming the StatefulSet <cluster>-node-<group> with container 'node'")
		Expect(resources.StatefulSet.Name).To(Equal(defaultResource))
		containers := resources.StatefulSet.Spec.Template.Spec.Containers
		Expect(containers).To(HaveLen(1))
		Expect(containers[0].Name).To(Equal("node"))

		By("labeling pods with the e2e-selected app name")
		Expect(resources.StatefulSet.Spec.Template.Labels).To(
			HaveKeyWithValue("app.kubernetes.io/name", "sparkhistoryserver"))
		for key, value := range resources.StatefulSet.Spec.Selector.MatchLabels {
			Expect(resources.StatefulSet.Spec.Template.Labels).To(HaveKeyWithValue(key, value))
		}

		By("exposing the http and metrics container ports")
		portNames := map[string]int32{}
		for _, p := range containers[0].Ports {
			portNames[p.Name] = p.ContainerPort
		}
		Expect(portNames).To(HaveKeyWithValue("http", int32(18080)))
		Expect(portNames).To(HaveKeyWithValue("metrics", int32(18081)))

		By("mounting the S3 credentials CSI volume at the e2e-asserted path")
		var credMount string
		for _, m := range containers[0].VolumeMounts {
			if m.Name == "s3-credentials" {
				credMount = m.MountPath
			}
		}
		Expect(credMount).To(Equal("/kubedoop/secret/s3-credentials"))
		Expect(containers[0].VolumeMounts).To(ContainElements(
			And(
				HaveField("Name", reconciler.ConfigVolumeName),
				HaveField("ReadOnly", true),
			),
			And(
				HaveField("Name", opgos3.DefaultCredentialsVolumeName),
				HaveField("ReadOnly", true),
			),
		))
		var credentialsVolume *corev1.Volume
		for i := range resources.StatefulSet.Spec.Template.Spec.Volumes {
			volume := &resources.StatefulSet.Spec.Template.Spec.Volumes[i]
			if volume.Name == opgos3.DefaultCredentialsVolumeName {
				credentialsVolume = volume
			}
		}
		Expect(credentialsVolume).NotTo(BeNil())
		Expect(credentialsVolume.Ephemeral).NotTo(BeNil())
		Expect(credentialsVolume.Ephemeral.VolumeClaimTemplate).NotTo(BeNil())
		Expect(credentialsVolume.Ephemeral.VolumeClaimTemplate.Spec.Resources.Requests).
			To(HaveKeyWithValue(corev1.ResourceStorage, resource.MustParse("10Mi")))

		By("declaring the history server command before user CLI overrides")
		Expect(containers[0].Command).To(HaveLen(6))
		Expect(containers[0].Args).To(BeEmpty())
		script := containers[0].Command[4]
		Expect(script).To(ContainSubstring("cp -RL /kubedoop/mount/config/* /kubedoop/config"))
		Expect(script).To(ContainSubstring(`export AWS_ACCESS_KEY_ID="$(cat /kubedoop/secret/s3-credentials/ACCESS_KEY)"`))
		Expect(script).To(ContainSubstring("exec /kubedoop/spark/sbin/start-history-server.sh --properties-file /kubedoop/config/spark-defaults.conf"))
		Expect(script).To(ContainSubstring(`"$@"`))
		Expect(containers[0].Command[5]).To(Equal("spark-history-server"))
		Expect(containers[0].ReadinessProbe.TCPSocket.Port.IntValue()).To(Equal(HttpPort))
		Expect(containers[0].LivenessProbe.TCPSocket.Port.IntValue()).To(Equal(HttpPort))

		By("rendering spark-defaults.conf with the S3 event-log location")
		Expect(resources.ConfigMap.Name).To(Equal(defaultResource))
		sparkDefaults := resources.ConfigMap.Data["spark-defaults.conf"]
		Expect(sparkDefaults).To(ContainSubstring("spark.history.fs.logDirectory=s3a://spark-history/events"))
		Expect(sparkDefaults).To(ContainSubstring("spark.hadoop.fs.s3a.endpoint=http"))
		Expect(sparkDefaults).To(ContainSubstring("spark.hadoop.fs.s3a.endpoint.region=us-east-1"))
		Expect(sparkDefaults).To(ContainSubstring("spark.hadoop.fs.s3a.path.style.access=true"))
		Expect(sparkDefaults).To(ContainSubstring("spark.hadoop.fs.s3a.connection.ssl.enabled=false"))

		By("rendering the log4j2 config under the e2e-asserted key")
		Expect(resources.ConfigMap.Data).To(HaveKey("log4j2.properties"))
		log4j2 := resources.ConfigMap.Data["log4j2.properties"]
		Expect(log4j2).To(ContainSubstring("appender.console.target=SYSTEM_ERR"))
		Expect(log4j2).To(ContainSubstring("appender.console.filter.threshold.level=INFO"))

		By("building the metrics Service with the e2e-asserted shape")
		metrics := resources.MetricsService
		Expect(metrics.Name).To(Equal(defaultResource + "-metrics"))
		Expect(metrics.Spec.ClusterIP).To(Equal("None"))
		Expect(metrics.Labels).To(HaveKeyWithValue("prometheus.io/scrape", "true"))
		Expect(metrics.Annotations).To(HaveKeyWithValue("prometheus.io/port", "18081"))
		Expect(metrics.Annotations).To(HaveKeyWithValue("prometheus.io/scheme", "http"))
		Expect(metrics.Spec.Ports).To(HaveLen(1))
		Expect(metrics.Spec.Ports[0].Name).To(Equal("http"))
		Expect(metrics.Spec.Ports[0].Port).To(Equal(int32(18081)))
		Expect(metrics.Spec.Ports[0].TargetPort.String()).To(Equal(MetricsPortName))
		Expect(metrics.Spec.Selector).To(HaveKeyWithValue("app.kubernetes.io/component", "node"))
		Expect(metrics.Spec.Selector).To(HaveKeyWithValue("app.kubernetes.io/instance", "sparkhistory"))
		Expect(metrics.Spec.Selector).To(
			HaveKeyWithValue(constant.LabelKubernetesManagedBy, frameworkManager))

		By("keeping the client Service ClusterIP without an OIDC port")
		Expect(resources.Service.Name).To(Equal(defaultResource))
		Expect(resources.Service.Spec.Type).To(Equal(corev1.ServiceTypeClusterIP))
		for _, p := range resources.Service.Spec.Ports {
			Expect(p.Name).NotTo(Equal(OidcPortName))
		}

		By("preserving the v0.12 immutable StatefulSet identity for upgrade and rollback")
		Expect(resources.HeadlessService).To(BeNil())
		Expect(resources.StatefulSet.Spec.ServiceName).To(Equal(defaultResource))
		Expect(resources.StatefulSet.Spec.PodManagementPolicy).To(Equal(appsv1.OrderedReadyPodManagement))
		Expect(resources.StatefulSet.Spec.Selector.MatchLabels).To(Equal(map[string]string{
			"app.kubernetes.io/name":          "sparkhistoryserver",
			"app.kubernetes.io/instance":      "sparkhistory",
			"app.kubernetes.io/component":     "node",
			"app.kubernetes.io/role-group":    defaultRoleGroup,
			constant.LabelKubernetesManagedBy: frameworkManager,
		}))
		Expect(resources.Service.Spec.Selector).To(Equal(resources.StatefulSet.Spec.Selector.MatchLabels))
		Expect(metrics.Spec.Selector).To(Equal(resources.StatefulSet.Spec.Selector.MatchLabels))
		Expect(resources.StatefulSet.Spec.Template.Spec.ServiceAccountName).To(
			Equal("sparkhistoryserver-sparkhistory"))

		By("keeping framework ownership on object metadata while selectors remain legacy")
		for _, object := range []metav1.Object{
			resources.ConfigMap, resources.Service, resources.StatefulSet, metrics,
		} {
			Expect(object.GetLabels()).To(
				HaveKeyWithValue(constant.LabelKubernetesManagedBy, frameworkManager))
			Expect(object.GetLabels()).To(HaveKeyWithValue("app.kubernetes.io/name", AppName))
		}

		By("preserving the legacy security and log-volume shape")
		securityContext := containers[0].SecurityContext
		Expect(securityContext.RunAsUser).To(Equal(ptr.To(int64(0))))
		Expect(securityContext.RunAsGroup).To(Equal(ptr.To(int64(0))))
		Expect(securityContext.RunAsNonRoot).To(BeNil())
		Expect(securityContext.AllowPrivilegeEscalation).To(Equal(ptr.To(false)))
		Expect(buildCtx.Declaration.LogVolumeSize).To(Equal("30Mi"))

		By("setting the SPARK_* environment defaults")
		envByName := map[string]string{}
		for _, e := range containers[0].Env {
			envByName[e.Name] = e.Value
		}
		Expect(envByName).To(HaveKeyWithValue("SPARK_NO_DAEMONIZE", "true"))
		Expect(envByName).To(HaveKeyWithValue("SPARK_DAEMON_CLASSPATH", "/kubedoop/spark/extra-jars/*"))
		Expect(envByName["SPARK_HISTORY_OPTS"]).To(ContainSubstring("-Dlog4j.configurationFile=/kubedoop/config/log4j2.properties"))
		Expect(envByName["SPARK_HISTORY_OPTS"]).To(ContainSubstring("-javaagent:/kubedoop/jmx/jmx_prometheus_javaagent.jar=18081:/kubedoop/jmx/config.yaml"))
	})

	It("preserves v0.12 config and S3 volume defaults for an adopted workload", func() {
		client := newFakeClient(scheme, append(minioObjects(), legacyOwnedStatefulSet(cr))...)
		buildCtx := testBuildContext(handler, client, cr)

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())

		main := resources.StatefulSet.Spec.Template.Spec.Containers[0]
		Expect(main.VolumeMounts).To(ContainElements(
			And(
				HaveField("Name", reconciler.ConfigVolumeName),
				HaveField("ReadOnly", false),
			),
			And(
				HaveField("Name", opgos3.DefaultCredentialsVolumeName),
				HaveField("ReadOnly", false),
			),
		))
		var credentialsVolume *corev1.Volume
		for i := range resources.StatefulSet.Spec.Template.Spec.Volumes {
			volume := &resources.StatefulSet.Spec.Template.Spec.Volumes[i]
			if volume.Name == opgos3.DefaultCredentialsVolumeName {
				credentialsVolume = volume
			}
		}
		Expect(credentialsVolume).NotTo(BeNil())
		Expect(credentialsVolume.Ephemeral).NotTo(BeNil())
		Expect(credentialsVolume.Ephemeral.VolumeClaimTemplate).NotTo(BeNil())
		Expect(credentialsVolume.Ephemeral.VolumeClaimTemplate.Spec.Resources.Requests).
			To(HaveKeyWithValue(corev1.ResourceStorage, resource.MustParse("1Mi")))
		Expect(resources.ConfigMap.Data[SparkDefaultsFileName]).NotTo(
			ContainSubstring("spark.hadoop.fs.s3a.endpoint.region"))
	})

	It("keeps an explicit S3 region override while adopting a v0.12 workload", func() {
		client := newFakeClient(scheme, append(minioObjects(), legacyOwnedStatefulSet(cr))...)
		buildCtx := testBuildContext(handler, client, cr)
		buildCtx.MergedConfig.ConfigFiles = map[string]map[string]string{
			SparkDefaultsFileName: {"spark.hadoop.fs.s3a.endpoint.region": "ap-southeast-1"},
		}

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		Expect(resources.ConfigMap.Data[SparkDefaultsFileName]).To(
			ContainSubstring("spark.hadoop.fs.s3a.endpoint.region=ap-southeast-1"))
	})

	It("keeps explicit mount modes above adopted v0.12 defaults", func() {
		client := newFakeClient(scheme, append(minioObjects(), legacyOwnedStatefulSet(cr))...)
		buildCtx := testBuildContext(handler, client, cr)
		overriddenStorage := resource.MustParse("2Mi")
		buildCtx.MergedConfig.PodOverrides = &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{
				Name: opgos3.DefaultCredentialsVolumeName,
				VolumeSource: corev1.VolumeSource{Ephemeral: &corev1.EphemeralVolumeSource{
					VolumeClaimTemplate: &corev1.PersistentVolumeClaimTemplate{Spec: corev1.PersistentVolumeClaimSpec{
						Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
							corev1.ResourceStorage: overriddenStorage,
						}},
					}},
				}},
			}},
			Containers: []corev1.Container{{
				Name: shsv1alpha1.RoleNode,
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      reconciler.ConfigVolumeName,
						MountPath: constant.KubedoopConfigDirMount,
						ReadOnly:  true,
					},
					{
						Name:      opgos3.DefaultCredentialsVolumeName,
						MountPath: opgos3.CredentialsMountPath(opgos3.DefaultCredentialsVolumeName),
						ReadOnly:  true,
					},
				},
			}},
		}}

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		Expect(resources.StatefulSet.Spec.Template.Spec.Containers[0].VolumeMounts).To(ContainElements(
			And(
				HaveField("Name", reconciler.ConfigVolumeName),
				HaveField("ReadOnly", true),
			),
			And(
				HaveField("Name", opgos3.DefaultCredentialsVolumeName),
				HaveField("ReadOnly", true),
			),
		))
		Expect(resources.StatefulSet.Spec.Template.Spec.Volumes).To(ContainElement(And(
			HaveField("Name", opgos3.DefaultCredentialsVolumeName),
			HaveField("Ephemeral.VolumeClaimTemplate.Spec.Resources.Requests",
				HaveKeyWithValue(corev1.ResourceStorage, overriddenStorage)),
		)))
	})

	It("preserves the legacy metrics target while adopting an unauthenticated workload", func() {
		client := newFakeClient(scheme, append(minioObjects(), legacyOwnedStatefulSet(cr))...)

		resources, err := handler.BuildResources(
			context.Background(), client, cr, testBuildContext(handler, client, cr))
		Expect(err).NotTo(HaveOccurred())
		Expect(resources.MetricsService.Spec.Ports).To(HaveLen(1))
		Expect(resources.MetricsService.Spec.Ports[0].TargetPort.String()).To(Equal(HttpPortName))
	})

	It("maps listenerClass to the Service type", func() {
		client := newFakeClient(scheme, minioObjects()...)
		cr.Spec.ClusterConfig.ListenerClass = "external-unstable"

		resources, err := handler.BuildResources(
			context.Background(), client, cr, testBuildContext(handler, client, cr))
		Expect(err).NotTo(HaveOccurred())
		Expect(resources.Service.Spec.Type).To(BeEquivalentTo("NodePort"))
	})

	It("keeps user configOverrides over the product-computed spark-defaults", func() {
		client := newFakeClient(scheme, minioObjects()...)
		buildCtx := testBuildContext(handler, client, cr)
		buildCtx.MergedConfig.ConfigFiles = map[string]map[string]string{
			"spark-defaults.conf": {"spark.history.fs.logDirectory": "s3a://user-bucket/logs"},
		}

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		Expect(resources.ConfigMap.Data["spark-defaults.conf"]).To(
			ContainSubstring("spark.history.fs.logDirectory=s3a://user-bucket/logs"))
	})

	It("lets configOverrides replace framework-owned logging files", func() {
		client := newFakeClient(scheme, minioObjects()...)
		buildCtx := testBuildContext(handler, client, cr)
		buildCtx.MergedConfig.ConfigFiles = map[string]map[string]string{
			LogConfigFileName: {"rootLogger.level": "DEBUG"},
		}

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		Expect(resources.ConfigMap.Data[LogConfigFileName]).To(
			ContainSubstring("rootLogger.level=DEBUG"))
		Expect(resources.ConfigMap.Data[LogConfigFileName]).NotTo(
			ContainSubstring("rootLogger.level = INFO"))
		Expect(resources.ConfigMap.Data[LogConfigFileName]).NotTo(
			ContainSubstring("appender.console.target=SYSTEM_ERR"))
		Expect(resources.ConfigMap.Data[LogConfigFileName]).NotTo(
			ContainSubstring("appender.console.filter.threshold.level=INFO"))
	})

	It("fails loudly when the referenced S3 bucket is missing", func() {
		declarationClient := newFakeClient(scheme, minioObjects()...)
		client := newFakeClient(scheme)
		_, err := handler.BuildResources(
			context.Background(), client, cr, testBuildContext(handler, declarationClient, cr))
		Expect(err).To(MatchError(ContainSubstring(`S3Bucket "spark-history"`)))
	})

	It("keeps the v0.12 CLI contract while env and pod overrides retain precedence", func() {
		client := newFakeClient(scheme, minioObjects()...)
		buildCtx := testBuildContext(handler, client, cr)
		cr.Spec.Node.OverridesSpec = &commonsv1alpha1.OverridesSpec{
			CliOverrides: []string{"--properties-file", "/custom/spark.conf"},
		}
		buildCtx.MergedConfig.CliArgs = cr.Spec.Node.CliOverrides
		buildCtx.MergedConfig.EnvVars = map[string]string{"SPARK_NO_DAEMONIZE": "false"}
		buildCtx.MergedConfig.PodOverrides = &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				EnableServiceLinks: ptr.To(false),
				Containers: []corev1.Container{{
					Name:    shsv1alpha1.RoleNode,
					Command: []string{"/custom-entrypoint"},
					ReadinessProbe: &corev1.Probe{
						InitialDelaySeconds: 99,
					},
				}},
			},
		}

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		main := resources.StatefulSet.Spec.Template.Spec.Containers[0]
		Expect(main.Command).To(Equal([]string{"/custom-entrypoint"}))
		Expect(main.Args).To(BeEmpty())
		Expect(main.ReadinessProbe.InitialDelaySeconds).To(Equal(int32(99)))
		Expect(resources.StatefulSet.Spec.Template.Spec.EnableServiceLinks).To(Equal(ptr.To(false)))

		env := map[string]string{}
		for _, variable := range main.Env {
			env[variable.Name] = variable.Value
		}
		Expect(env).To(HaveKeyWithValue("SPARK_NO_DAEMONIZE", "false"))
	})

	It("treats cliOverrides as a replacement command for upgrade compatibility", func() {
		client := newFakeClient(scheme, minioObjects()...)
		buildCtx := testBuildContext(handler, client, cr)
		cr.Spec.Node.OverridesSpec = &commonsv1alpha1.OverridesSpec{
			CliOverrides: []string{"/custom-wrapper", "--debug"},
		}
		buildCtx.MergedConfig.CliArgs = cr.Spec.Node.CliOverrides

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		main := resources.StatefulSet.Spec.Template.Spec.Containers[0]
		Expect(main.Command).To(Equal([]string{"/custom-wrapper", "--debug"}))
		Expect(main.Args).To(BeEmpty())
	})

	It("keeps the v0.12 command when only pod override args replace the startup script", func() {
		client := newFakeClient(scheme, minioObjects()...)
		buildCtx := testBuildContext(handler, client, cr)
		buildCtx.MergedConfig.PodOverrides = &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name: shsv1alpha1.RoleNode,
				Args: []string{"echo args-only-override"},
			}}},
		}

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		main := resources.StatefulSet.Spec.Template.Spec.Containers[0]
		Expect(main.Command).To(Equal([]string{bashPath, "-c"}))
		Expect(main.Args).To(Equal([]string{"echo args-only-override"}))
	})

	It("keeps the v0.12 startup-script arg when only pod override command is set", func() {
		client := newFakeClient(scheme, minioObjects()...)
		buildCtx := testBuildContext(handler, client, cr)
		buildCtx.MergedConfig.PodOverrides = &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name:    shsv1alpha1.RoleNode,
				Command: []string{"/custom-command-only-wrapper"},
			}}},
		}

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		main := resources.StatefulSet.Spec.Template.Spec.Containers[0]
		Expect(main.Command).To(Equal([]string{"/custom-command-only-wrapper"}))
		Expect(main.Args).To(HaveLen(1))
		Expect(main.Args[0]).To(ContainSubstring("start-history-server.sh"))
		Expect(main.Args[0]).To(ContainSubstring("spark-defaults.conf"))
	})

	It("keeps pod override args above a legacy CLI command override", func() {
		client := newFakeClient(scheme, minioObjects()...)
		buildCtx := testBuildContext(handler, client, cr)
		cr.Spec.Node.OverridesSpec = &commonsv1alpha1.OverridesSpec{
			CliOverrides: []string{cliWrapper},
		}
		buildCtx.MergedConfig.CliArgs = cr.Spec.Node.CliOverrides
		buildCtx.MergedConfig.PodOverrides = &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name: shsv1alpha1.RoleNode,
				Args: []string{"--from-pod-override"},
			}}},
		}

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		main := resources.StatefulSet.Spec.Template.Spec.Containers[0]
		Expect(main.Command).To(Equal([]string{cliWrapper}))
		Expect(main.Args).To(Equal([]string{"--from-pod-override"}))
	})

	It("keeps an unnamed main-container pod override above a legacy CLI command override", func() {
		client := newFakeClient(scheme, minioObjects()...)
		buildCtx := testBuildContext(handler, client, cr)
		cr.Spec.Node.OverridesSpec = &commonsv1alpha1.OverridesSpec{
			CliOverrides: []string{cliWrapper},
		}
		buildCtx.MergedConfig.CliArgs = cr.Spec.Node.CliOverrides
		buildCtx.MergedConfig.PodOverrides = &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Command: []string{"/unnamed-pod-wrapper"},
				Args:    []string{"--from-unnamed-pod-override"},
			}}},
		}

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		main := resources.StatefulSet.Spec.Template.Spec.Containers[0]
		Expect(main.Command).To(Equal([]string{"/unnamed-pod-wrapper"}))
		Expect(main.Args).To(Equal([]string{"--from-unnamed-pod-override"}))
	})

	It("lets a named legacy main-container override replace the framework config mount", func() {
		client := newFakeClient(scheme, minioObjects()...)
		buildCtx := testBuildContext(handler, client, cr)
		buildCtx.MergedConfig.PodOverrides = &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{
				{
					Name: "replacement-config",
					VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: "custom-config"},
					}},
				},
				{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			},
			Containers: []corev1.Container{{
				Name: shsv1alpha1.RoleNode,
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "replacement-config",
						MountPath: constant.KubedoopConfigDirMount,
						ReadOnly:  false,
						SubPath:   "spark-config",
					},
					{Name: "scratch", MountPath: "/scratch", ReadOnly: true, SubPath: "work"},
				},
			}},
		}}

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		main := resources.StatefulSet.Spec.Template.Spec.Containers[0]
		var configPathMounts []corev1.VolumeMount
		for _, mount := range main.VolumeMounts {
			if mount.MountPath == constant.KubedoopConfigDirMount {
				configPathMounts = append(configPathMounts, mount)
			}
		}
		Expect(configPathMounts).To(ConsistOf(And(
			HaveField("Name", "replacement-config"),
			HaveField("ReadOnly", false),
			HaveField("SubPath", "spark-config"),
		)))
		Expect(main.VolumeMounts).To(ContainElement(And(
			HaveField("Name", "scratch"),
			HaveField("MountPath", "/scratch"),
			HaveField("ReadOnly", true),
			HaveField("SubPath", "work"),
		)))
		Expect(resources.StatefulSet.Spec.Template.Spec.Volumes).To(ContainElement(
			HaveField("Name", "replacement-config")))
	})

	It("lets an unnamed legacy main-container override replace a provider mount", func() {
		client := newFakeClient(scheme, minioObjects()...)
		buildCtx := testBuildContext(handler, client, cr)
		credentialsPath := opgos3.CredentialsMountPath(opgos3.DefaultCredentialsVolumeName)
		buildCtx.MergedConfig.PodOverrides = &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{
				Name: "replacement-credentials",
				VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
					SecretName: "custom-s3-credentials",
				}},
			}},
			Containers: []corev1.Container{{VolumeMounts: []corev1.VolumeMount{{
				Name:        "replacement-credentials",
				MountPath:   credentialsPath,
				ReadOnly:    true,
				SubPathExpr: "$(POD_NAME)",
			}}}},
		}}

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		Expect(resources.StatefulSet.Spec.Template.Spec.Containers).To(HaveLen(1))
		main := resources.StatefulSet.Spec.Template.Spec.Containers[0]
		Expect(main.Name).To(Equal(shsv1alpha1.RoleNode))
		Expect(main.VolumeMounts).To(ContainElement(And(
			HaveField("Name", "replacement-credentials"),
			HaveField("MountPath", credentialsPath),
			HaveField("ReadOnly", true),
			HaveField("SubPathExpr", "$(POD_NAME)"),
		)))
		Expect(resources.StatefulSet.Spec.Template.Spec.Volumes).To(ContainElement(
			HaveField("Name", "replacement-credentials")))
	})

	DescribeTable("leaves invalid main-container mounts on the framework validation path",
		func(mount corev1.VolumeMount) {
			client := newFakeClient(scheme, minioObjects()...)
			buildCtx := testBuildContext(handler, client, cr)
			buildCtx.MergedConfig.PodOverrides = &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:         shsv1alpha1.RoleNode,
					VolumeMounts: []corev1.VolumeMount{mount},
				}},
			}}

			_, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
			Expect(err).To(HaveOccurred())
			Expect(reconciler.IsValidationError(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("references no declared volume"))
		},
		Entry("an undeclared replacement at the config mount path", corev1.VolumeMount{
			Name: "missing-config", MountPath: constant.KubedoopConfigDirMount,
		}),
		Entry("an undeclared non-conflicting mount", corev1.VolumeMount{
			Name: "missing-custom", MountPath: "/custom/missing",
		}),
	)

	It("appends role-group CLI values to the role command like v0.12", func() {
		client := newFakeClient(scheme, minioObjects()...)
		cr.Spec.Node.OverridesSpec = &commonsv1alpha1.OverridesSpec{
			CliOverrides: []string{"/role-wrapper", "--role"},
		}
		cr.Spec.Node.RoleGroups[defaultRoleGroup].OverridesSpec = &commonsv1alpha1.OverridesSpec{
			CliOverrides: []string{roleGroupCLIArg},
		}
		buildCtx := testBuildContext(handler, client, cr)
		// v0.13's generic fold contains only the group slice; the compatibility layer must not
		// mistake that for the old effective value.
		buildCtx.MergedConfig.CliArgs = []string{roleGroupCLIArg}

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		main := resources.StatefulSet.Spec.Template.Spec.Containers[0]
		Expect(main.Command).To(Equal([]string{"/role-wrapper", "--role", roleGroupCLIArg}))
		Expect(main.Args).To(BeEmpty())
	})

	It("keeps an upgraded v0.12 workload on its existing service account", func() {
		legacyStatefulSet := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{
			Name:      defaultResource,
			Namespace: cr.Namespace,
			Labels: map[string]string{
				constant.LabelKubernetesManagedBy: shsv1alpha1.GroupVersion.Group,
			},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(
				cr, shsv1alpha1.GroupVersion.WithKind("SparkHistoryServer"),
			)},
		}}
		client := newFakeClient(scheme, append(minioObjects(), legacyStatefulSet)...)
		buildCtx := testBuildContext(handler, client, cr)

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		Expect(resources.StatefulSet.Spec.Template.Spec.ServiceAccountName).To(
			Equal(defaultServiceAccountName))
		Expect(resources.StatefulSet.Annotations).To(
			HaveKeyWithValue(legacyServiceAccountAnnotation, defaultServiceAccountName))
		Expect(resources.StatefulSet.Spec.Selector.MatchLabels).To(
			HaveKeyWithValue(constant.LabelKubernetesManagedBy, shsv1alpha1.GroupVersion.Group))
	})

	It("keeps v0.12 path-style S3 access for an adopted workload", func() {
		objects := minioObjects()
		objects[0].(*s3v1alpha1.S3Connection).Spec.PathStyle = false
		legacyStatefulSet := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{
			Name:      defaultResource,
			Namespace: cr.Namespace,
			Labels: map[string]string{
				constant.LabelKubernetesManagedBy: shsv1alpha1.GroupVersion.Group,
			},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(
				cr, shsv1alpha1.GroupVersion.WithKind("SparkHistoryServer"),
			)},
		}}
		client := newFakeClient(scheme, append(objects, legacyStatefulSet)...)

		resources, err := handler.BuildResources(
			context.Background(), client, cr, testBuildContext(handler, client, cr))
		Expect(err).NotTo(HaveOccurred())
		Expect(resources.ConfigMap.Data[SparkDefaultsFileName]).To(
			ContainSubstring("spark.hadoop.fs.s3a.path.style.access=true"))
	})

	It("lets an explicit serviceAccountName override replace the live legacy identity", func() {
		legacyStatefulSet := &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      defaultResource,
				Namespace: cr.Namespace,
				Labels: map[string]string{
					constant.LabelKubernetesManagedBy: shsv1alpha1.GroupVersion.Group,
				},
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(
					cr, shsv1alpha1.GroupVersion.WithKind("SparkHistoryServer"),
				)},
			},
			Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{ServiceAccountName: defaultServiceAccountName},
			}},
		}
		client := newFakeClient(scheme, append(minioObjects(), legacyStatefulSet)...)
		buildCtx := testBuildContext(handler, client, cr)
		buildCtx.MergedConfig.PodOverrides = &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{ServiceAccountName: "custom-sa"},
		}

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		Expect(resources.StatefulSet.Spec.Template.Spec.ServiceAccountName).To(Equal("custom-sa"))
		Expect(resources.StatefulSet.Annotations).To(
			HaveKeyWithValue(legacyServiceAccountAnnotation, defaultServiceAccountName))

		By("restoring the recorded legacy baseline when the override is later removed")
		liveAfterMigration := resources.StatefulSet.DeepCopy()
		liveAfterMigration.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(
			cr, shsv1alpha1.GroupVersion.WithKind("SparkHistoryServer"),
		)}
		nextClient := newFakeClient(scheme, append(minioObjects(), liveAfterMigration)...)
		nextResources, err := handler.BuildResources(
			context.Background(), nextClient, cr, testBuildContext(handler, nextClient, cr))
		Expect(err).NotTo(HaveOccurred())
		Expect(nextResources.StatefulSet.Spec.Template.Spec.ServiceAccountName).To(
			Equal(defaultServiceAccountName))
	})

	It("returns a fresh v0.13 workload to its per-CR service account after an override is removed", func() {
		freshStatefulSet := &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      defaultResource,
				Namespace: cr.Namespace,
				Labels: map[string]string{
					constant.LabelKubernetesManagedBy: frameworkManager,
				},
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(
					cr, shsv1alpha1.GroupVersion.WithKind("SparkHistoryServer"),
				)},
			},
			Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{ServiceAccountName: "temporary-sa"},
			}},
		}
		client := newFakeClient(scheme, append(minioObjects(), freshStatefulSet)...)
		buildCtx := testBuildContext(handler, client, cr)

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		Expect(resources.StatefulSet.Spec.Template.Spec.ServiceAccountName).To(
			Equal("sparkhistoryserver-sparkhistory"))
		Expect(resources.StatefulSet.Annotations).NotTo(HaveKey(legacyServiceAccountAnnotation))
	})

	It("retains the migrated baseline while a temporary service account override is active", func() {
		migratedStatefulSet := &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      defaultResource,
				Namespace: cr.Namespace,
				Labels: map[string]string{
					constant.LabelKubernetesManagedBy: frameworkManager,
				},
				Annotations: map[string]string{
					legacyServiceAccountAnnotation: defaultServiceAccountName,
				},
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(
					cr, shsv1alpha1.GroupVersion.WithKind("SparkHistoryServer"),
				)},
			},
			Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{ServiceAccountName: defaultServiceAccountName},
			}},
		}
		client := newFakeClient(scheme, append(minioObjects(), migratedStatefulSet)...)
		buildCtx := testBuildContext(handler, client, cr)
		buildCtx.MergedConfig.PodOverrides = &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{ServiceAccountName: "temporary-sa"},
		}

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		Expect(resources.StatefulSet.Spec.Template.Spec.ServiceAccountName).To(Equal("temporary-sa"))
		Expect(resources.StatefulSet.Annotations).To(
			HaveKeyWithValue(legacyServiceAccountAnnotation, defaultServiceAccountName))
	})

	It("keeps the v0.12 limit-only memory contract", func() {
		client := newFakeClient(scheme, minioObjects()...)
		memoryLimit := resource.MustParse("2Gi")
		cr.Spec.Node.RoleGroups[defaultRoleGroup].Config = &shsv1alpha1.ConfigSpec{
			RoleGroupConfigSpec: &commonsv1alpha1.RoleGroupConfigSpec{
				Resources: &commonsv1alpha1.ResourcesSpec{
					Memory: &commonsv1alpha1.MemoryResource{Limit: &memoryLimit},
				},
			},
		}
		buildCtx := testBuildContext(handler, client, cr)

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		main := resources.StatefulSet.Spec.Template.Spec.Containers[0]
		Expect(main.Resources.Limits).To(HaveKeyWithValue(corev1.ResourceMemory, memoryLimit))
		Expect(main.Resources.Requests).NotTo(HaveKey(corev1.ResourceMemory))
	})

	It("preserves an explicit podOverrides memory request", func() {
		client := newFakeClient(scheme, minioObjects()...)
		memoryLimit := resource.MustParse("2Gi")
		memoryRequest := resource.MustParse("1Gi")
		cr.Spec.Node.RoleGroups[defaultRoleGroup].Config = &shsv1alpha1.ConfigSpec{
			RoleGroupConfigSpec: &commonsv1alpha1.RoleGroupConfigSpec{
				Resources: &commonsv1alpha1.ResourcesSpec{
					Memory: &commonsv1alpha1.MemoryResource{Limit: &memoryLimit},
				},
			},
		}
		buildCtx := testBuildContext(handler, client, cr)
		buildCtx.MergedConfig.PodOverrides = &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name: shsv1alpha1.RoleNode,
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceMemory: memoryRequest,
				}},
			}}},
		}

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		main := resources.StatefulSet.Spec.Template.Spec.Containers[0]
		Expect(main.Resources.Limits).To(HaveKeyWithValue(corev1.ResourceMemory, memoryLimit))
		Expect(main.Resources.Requests).To(HaveKeyWithValue(corev1.ResourceMemory, memoryRequest))
	})

	It("rejects an existing legacy workload whose name v0.13 would hash", func() {
		cr.Name = strings.Repeat("a", 42) // 42 + len("-node-default") = 55.
		rawName := cr.Name + "-node-default"
		legacyStatefulSet := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{
			Name:      rawName,
			Namespace: cr.Namespace,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(
				cr, shsv1alpha1.GroupVersion.WithKind("SparkHistoryServer"),
			)},
		}}
		client := newFakeClient(scheme, append(minioObjects(), legacyStatefulSet)...)
		buildCtx := testBuildContext(handler, client, cr)

		_, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).To(MatchError(ContainSubstring("requires the documented long-name migration")))
	})

	It("allows a new long-name cluster to use v0.13's canonical hash", func() {
		client := newFakeClient(scheme, minioObjects()...)
		cr.Name = strings.Repeat("a", 42) // 42 + len("-node-default") = 55.
		rawName := cr.Name + "-node-default"
		buildCtx := testBuildContext(handler, client, cr)

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		Expect(resources.StatefulSet.Name).To(Equal(buildCtx.ResourceName))
		Expect(resources.StatefulSet.Name).NotTo(Equal(rawName))
		Expect(resources.StatefulSet.Name).To(HaveLen(maxCompatibleRoleGroupResourceNameLength))
	})
})

var _ = Describe("PodDisruptionBudget compatibility", func() {
	It("keeps the legacy role selector and framework-owned metadata", func() {
		handler := NewSparkHistoryRoleGroupHandler(newScheme())
		cr := testCR()
		genericSpec := cr.GetSpec()
		roleSpec := genericSpec.Roles[shsv1alpha1.RoleNode]
		roleSpec.RoleConfig = &commonsv1alpha1.RoleConfigSpec{
			PodDisruptionBudget: &commonsv1alpha1.PodDisruptionBudgetSpec{Enabled: ptr.To(true)},
		}

		pdb := handler.BuildRolePodDisruptionBudget(&reconciler.RoleBuildContext{
			ClusterName:      cr.Name,
			ClusterNamespace: cr.Namespace,
			RoleName:         shsv1alpha1.RoleNode,
			RoleSpec:         &roleSpec,
			ProductName:      shsv1alpha1.DefaultProductName,
			ProductVersion:   shsv1alpha1.DefaultProductVersion,
		})

		Expect(pdb).NotTo(BeNil())
		Expect(pdb.Spec.Selector.MatchLabels).To(Equal(map[string]string{
			"app.kubernetes.io/name":      AppName,
			"app.kubernetes.io/instance":  cr.Name,
			"app.kubernetes.io/component": shsv1alpha1.RoleNode,
		}))
		Expect(pdb.Labels).To(HaveKeyWithValue("app.kubernetes.io/name", AppName))
		Expect(pdb.Labels).To(HaveKeyWithValue(constant.LabelKubernetesManagedBy, frameworkManager))
	})
})

var _ = Describe("LegacyCompatibilityExtension", func() {
	legacyRoleGroupAnchors := func(
		cr *shsv1alpha1.SparkHistoryServer,
		groupName string,
	) (*appsv1.StatefulSet, *corev1.ConfigMap) {
		labels := func() map[string]string {
			return map[string]string{
				constant.LabelKubernetesName:      AppName,
				constant.LabelKubernetesInstance:  cr.Name,
				constant.LabelKubernetesComponent: shsv1alpha1.RoleNode,
				constant.LabelKubernetesRoleGroup: groupName,
				constant.LabelKubernetesManagedBy: shsv1alpha1.GroupVersion.Group,
			}
		}
		owner := []metav1.OwnerReference{*metav1.NewControllerRef(
			cr, shsv1alpha1.GroupVersion.WithKind("SparkHistoryServer"),
		)}
		name := cr.Name + "-" + shsv1alpha1.RoleNode + "-" + groupName
		return &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name: name, Namespace: cr.Namespace, Labels: labels(), OwnerReferences: owner,
				},
				Spec: appsv1.StatefulSetSpec{
					Selector: &metav1.LabelSelector{MatchLabels: labels()},
					Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels()}},
				},
			}, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: cr.Namespace, Labels: labels(), OwnerReferences: owner,
			}}
	}

	legacyPDB := func(cr *shsv1alpha1.SparkHistoryServer) *policyv1.PodDisruptionBudget {
		return &policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{
			Name:      cr.Name + "-" + shsv1alpha1.RoleNode,
			Namespace: cr.Namespace,
			Labels: map[string]string{
				constant.LabelKubernetesName:      AppName,
				constant.LabelKubernetesInstance:  cr.Name,
				constant.LabelKubernetesComponent: shsv1alpha1.RoleNode,
				constant.LabelKubernetesManagedBy: shsv1alpha1.GroupVersion.Group,
			},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(
				cr, shsv1alpha1.GroupVersion.WithKind("SparkHistoryServer"),
			)},
		}}
	}

	It("rehydrates removed v0.12 groups so the framework cleaner can reclaim every resource", func() {
		scheme := newScheme()
		cr := testCR()
		statefulSet, configMap := legacyRoleGroupAnchors(cr, "removed")
		clientService := &corev1.Service{ObjectMeta: metav1.ObjectMeta{
			Name: statefulSet.Name, Namespace: cr.Namespace,
			Labels: configMap.Labels, OwnerReferences: statefulSet.OwnerReferences,
		}}
		metricsService := clientService.DeepCopy()
		metricsService.Name = statefulSet.Name + "-metrics"
		client := newFakeClient(scheme, statefulSet, configMap, clientService, metricsService)

		Expect(NewLegacyCompatibilityExtension().PreReconcile(
			context.Background(), client, cr)).To(Succeed())
		updatedStatefulSet := &appsv1.StatefulSet{}
		Expect(client.Get(context.Background(), ctrlclient.ObjectKeyFromObject(statefulSet),
			updatedStatefulSet)).To(Succeed())
		updatedConfigMap := &corev1.ConfigMap{}
		Expect(client.Get(context.Background(), ctrlclient.ObjectKeyFromObject(configMap),
			updatedConfigMap)).To(Succeed())
		Expect(updatedStatefulSet.Labels).To(HaveKeyWithValue(
			constant.LabelKubernetesManagedBy, shsv1alpha1.GroupVersion.Group))
		Expect(updatedConfigMap.Labels).To(HaveKeyWithValue(
			constant.LabelKubernetesManagedBy, shsv1alpha1.GroupVersion.Group))
		Expect(cr.GetStatus().GetRoleGroups()[shsv1alpha1.RoleNode]).To(
			ContainElement("removed"))
		Expect(updatedStatefulSet.Spec.Selector.MatchLabels).To(
			HaveKeyWithValue(constant.LabelKubernetesManagedBy, shsv1alpha1.GroupVersion.Group))
		Expect(updatedStatefulSet.Spec.Template.Labels).To(
			HaveKeyWithValue(constant.LabelKubernetesManagedBy, shsv1alpha1.GroupVersion.Group))

		By("carrying the orphan ledger across the cleaner's scale and deletion passes")
		cleaner := reconciler.NewRoleGroupCleaner(client, scheme).WithAPIReader(client)
		for range 3 {
			_, err := cleaner.Cleanup(
				context.Background(), cr.Namespace, cr.Name, cr.GetSpec(), cr.GetStatus(),
				cr.UID, cr.Annotations,
			)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(client.Get(context.Background(), ctrlclient.ObjectKeyFromObject(statefulSet),
			&appsv1.StatefulSet{})).To(MatchError(apierrors.IsNotFound, "IsNotFound"))
		Expect(client.Get(context.Background(), ctrlclient.ObjectKeyFromObject(configMap),
			&corev1.ConfigMap{})).To(MatchError(apierrors.IsNotFound, "IsNotFound"))
		Expect(client.Get(context.Background(), ctrlclient.ObjectKeyFromObject(clientService),
			&corev1.Service{})).To(MatchError(apierrors.IsNotFound, "IsNotFound"))
		Expect(client.Get(context.Background(), ctrlclient.ObjectKeyFromObject(metricsService),
			&corev1.Service{})).To(MatchError(apierrors.IsNotFound, "IsNotFound"))
		Expect(cr.GetStatus().GetRoleGroups()[shsv1alpha1.RoleNode]).NotTo(
			ContainElement("removed"))
	})

	It("reconstructs the orphan ledger from the final legacy metrics Service after a restart", func() {
		scheme := newScheme()
		cr := testCR()
		statefulSet, configMap := legacyRoleGroupAnchors(cr, "removed")
		metricsService := &corev1.Service{ObjectMeta: metav1.ObjectMeta{
			Name: statefulSet.Name + "-metrics", Namespace: cr.Namespace,
			Labels: configMap.Labels, OwnerReferences: statefulSet.OwnerReferences,
		}}
		client := newFakeClient(scheme, metricsService)

		Expect(NewLegacyCompatibilityExtension().PreReconcile(
			context.Background(), client, cr)).To(Succeed())
		Expect(cr.GetStatus().GetRoleGroups()[shsv1alpha1.RoleNode]).To(
			ContainElement("removed"))

		cleaner := reconciler.NewRoleGroupCleaner(client, scheme).WithAPIReader(client)
		_, err := cleaner.Cleanup(
			context.Background(), cr.Namespace, cr.Name, cr.GetSpec(), cr.GetStatus(),
			cr.UID, cr.Annotations,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(client.Get(context.Background(), ctrlclient.ObjectKeyFromObject(metricsService),
			&corev1.Service{})).To(MatchError(apierrors.IsNotFound, "IsNotFound"))
		Expect(cr.GetStatus().GetRoleGroups()[shsv1alpha1.RoleNode]).NotTo(
			ContainElement("removed"))
	})

	It("does not adopt a legacy role group that is still declared", func() {
		scheme := newScheme()
		cr := testCR()
		statefulSet, configMap := legacyRoleGroupAnchors(cr, defaultRoleGroup)
		client := newFakeClient(scheme, statefulSet, configMap)

		Expect(NewLegacyCompatibilityExtension().PreReconcile(
			context.Background(), client, cr)).To(Succeed())
		updated := &appsv1.StatefulSet{}
		Expect(client.Get(context.Background(), ctrlclient.ObjectKeyFromObject(statefulSet),
			updated)).To(Succeed())
		Expect(updated.Labels).To(HaveKeyWithValue(
			constant.LabelKubernetesManagedBy, shsv1alpha1.GroupVersion.Group))
	})

	It("fails safely instead of leaking a removed long-name v0.12 role group", func() {
		scheme := newScheme()
		cr := testCR()
		cr.Name = strings.Repeat("a", 45)
		statefulSet, configMap := legacyRoleGroupAnchors(cr, "removed")
		client := newFakeClient(scheme, statefulSet, configMap)

		err := NewLegacyCompatibilityExtension().PreReconcile(
			context.Background(), client, cr)
		Expect(err).To(MatchError(And(
			ContainSubstring("removed legacy role group resource"),
			ContainSubstring("documented long-name migration"),
		)))
	})

	It("deletes the exact owned v0.12 PDB when it is disabled", func() {
		scheme := newScheme()
		cr := testCR()
		pdb := legacyPDB(cr)
		client := newFakeClient(scheme, pdb)

		Expect(NewLegacyCompatibilityExtension().PreReconcile(
			context.Background(), client, cr)).To(Succeed())
		err := client.Get(context.Background(), ctrlclient.ObjectKeyFromObject(pdb), &policyv1.PodDisruptionBudget{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("leaves an enabled legacy PDB for the framework to adopt", func() {
		scheme := newScheme()
		cr := testCR()
		cr.Spec.Node.RoleConfig = &commonsv1alpha1.RoleConfigSpec{
			PodDisruptionBudget: &commonsv1alpha1.PodDisruptionBudgetSpec{Enabled: ptr.To(true)},
		}
		pdb := legacyPDB(cr)
		client := newFakeClient(scheme, pdb)

		Expect(NewLegacyCompatibilityExtension().PreReconcile(
			context.Background(), client, cr)).To(Succeed())
		Expect(client.Get(context.Background(), ctrlclient.ObjectKeyFromObject(pdb),
			&policyv1.PodDisruptionBudget{})).To(Succeed())
	})

	It("treats an omitted enabled field as enabled like the v0.13 PDB contract", func() {
		scheme := newScheme()
		cr := testCR()
		cr.Spec.Node.RoleConfig = &commonsv1alpha1.RoleConfigSpec{
			PodDisruptionBudget: &commonsv1alpha1.PodDisruptionBudgetSpec{},
		}
		pdb := legacyPDB(cr)
		client := newFakeClient(scheme, pdb)

		Expect(NewLegacyCompatibilityExtension().PreReconcile(
			context.Background(), client, cr)).To(Succeed())
		Expect(client.Get(context.Background(), ctrlclient.ObjectKeyFromObject(pdb),
			&policyv1.PodDisruptionBudget{})).To(Succeed())
	})

	It("does not delete a same-name PDB without the complete legacy fingerprint", func() {
		scheme := newScheme()
		cr := testCR()
		pdb := legacyPDB(cr)
		pdb.Labels[constant.LabelKubernetesManagedBy] = "another-controller"
		client := newFakeClient(scheme, pdb)

		Expect(NewLegacyCompatibilityExtension().PreReconcile(
			context.Background(), client, cr)).To(Succeed())
		Expect(client.Get(context.Background(), ctrlclient.ObjectKeyFromObject(pdb),
			&policyv1.PodDisruptionBudget{})).To(Succeed())
	})

	DescribeTable("reports stuck pods carrying the immutable legacy workload identity",
		func(waitingReason string) {
			scheme := newScheme()
			cr := testCR()
			cr.GetStatus().SetDegraded(false, commonsv1alpha1.ReasonAvailable, "No failing pods")
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "sparkhistory-node-default-0",
					Namespace: cr.Namespace,
					Labels: map[string]string{
						constant.LabelKubernetesName:      AppName,
						constant.LabelKubernetesInstance:  cr.Name,
						constant.LabelKubernetesManagedBy: shsv1alpha1.GroupVersion.Group,
					},
				},
				Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
					Name: shsv1alpha1.RoleNode,
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
						Reason: waitingReason,
					}},
				}}},
			}
			client := newFakeClient(scheme, pod)

			Expect(NewLegacyCompatibilityExtension().PostReconcile(
				context.Background(), client, cr)).To(Succeed())
			degraded := cr.GetStatus().GetCondition(commonsv1alpha1.ConditionDegraded)
			Expect(degraded.Status).To(Equal(metav1.ConditionTrue))
			Expect(degraded.Reason).To(Equal(commonsv1alpha1.ReasonPodFailure))
			Expect(degraded.Message).To(ContainSubstring(
				"sparkhistory-node-default-0 (" + waitingReason + ")"))
		},
		Entry("after an attempted pull", reasonImagePullBackOff),
		Entry("when imagePullPolicy is Never", reasonErrImageNeverPull),
	)

	It("leaves fresh v0.13 pod health to the framework", func() {
		scheme := newScheme()
		cr := testCR()
		cr.GetStatus().SetDegraded(false, commonsv1alpha1.ReasonAvailable, "No failing pods")
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "sparkhistory-node-default-0",
				Namespace: cr.Namespace,
				Labels: map[string]string{
					constant.LabelKubernetesName:      AppName,
					constant.LabelKubernetesInstance:  cr.Name,
					constant.LabelKubernetesManagedBy: frameworkManager,
				},
			},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
				Name: shsv1alpha1.RoleNode,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason: reasonImagePullBackOff,
				}},
			}}},
		}
		client := newFakeClient(scheme, pod)

		Expect(NewLegacyCompatibilityExtension().PostReconcile(
			context.Background(), client, cr)).To(Succeed())
		Expect(cr.GetStatus().GetCondition(commonsv1alpha1.ConditionDegraded).Status).To(
			Equal(metav1.ConditionFalse))
	})
})

var _ = Describe("cleanerEnabled", func() {
	role := func(roleCleaner *bool, groups map[string]*shsv1alpha1.RoleGroupSpec) *shsv1alpha1.RoleSpec {
		r := &shsv1alpha1.RoleSpec{RoleGroups: groups}
		if roleCleaner != nil {
			r.Config = &shsv1alpha1.ConfigSpec{Cleaner: roleCleaner}
		}
		return r
	}

	It("is off by default", func() {
		enabled, err := cleanerEnabled(role(nil, map[string]*shsv1alpha1.RoleGroupSpec{defaultRoleGroup: {}}), defaultRoleGroup)
		Expect(err).NotTo(HaveOccurred())
		Expect(enabled).To(BeFalse())
	})

	It("enables the cleaner from the role config for a single role group", func() {
		enabled, err := cleanerEnabled(role(ptr.To(true), map[string]*shsv1alpha1.RoleGroupSpec{defaultRoleGroup: {}}), defaultRoleGroup)
		Expect(err).NotTo(HaveOccurred())
		Expect(enabled).To(BeTrue())
	})

	It("lets the role group override the role config", func() {
		groups := map[string]*shsv1alpha1.RoleGroupSpec{
			defaultRoleGroup: {Config: &shsv1alpha1.ConfigSpec{Cleaner: ptr.To(false)}},
		}
		enabled, err := cleanerEnabled(role(ptr.To(true), groups), defaultRoleGroup)
		Expect(err).NotTo(HaveOccurred())
		Expect(enabled).To(BeFalse())
	})

	It("rejects a role-level cleaner inherited by multiple role groups", func() {
		groups := map[string]*shsv1alpha1.RoleGroupSpec{"a": {}, "b": {}}
		_, err := cleanerEnabled(role(ptr.To(true), groups), "a")
		Expect(err).To(MatchError(ContainSubstring("multiple role groups")))
	})

	It("allows role-level cleaner when all but one role group disable it", func() {
		groups := map[string]*shsv1alpha1.RoleGroupSpec{
			"a": {},
			"b": {Config: &shsv1alpha1.ConfigSpec{Cleaner: ptr.To(false)}},
		}
		enabled, err := cleanerEnabled(role(ptr.To(true), groups), "a")
		Expect(err).NotTo(HaveOccurred())
		Expect(enabled).To(BeTrue())
		enabled, err = cleanerEnabled(role(ptr.To(true), groups), "b")
		Expect(err).NotTo(HaveOccurred())
		Expect(enabled).To(BeFalse())
	})

	It("rejects cleaner enabled explicitly in multiple role groups", func() {
		groups := map[string]*shsv1alpha1.RoleGroupSpec{
			"a": {Config: &shsv1alpha1.ConfigSpec{Cleaner: ptr.To(true)}},
			"b": {Config: &shsv1alpha1.ConfigSpec{Cleaner: ptr.To(true)}},
		}
		_, err := cleanerEnabled(role(nil, groups), "a")
		Expect(err).To(MatchError(ContainSubstring("multiple role groups")))
	})

	It("rejects a cleaner on a role group with more than one replica", func() {
		groups := map[string]*shsv1alpha1.RoleGroupSpec{
			defaultRoleGroup: {
				Replicas: ptr.To(int32(2)),
				Config:   &shsv1alpha1.ConfigSpec{Cleaner: ptr.To(true)},
			},
		}
		_, err := cleanerEnabled(role(nil, groups), defaultRoleGroup)
		Expect(err).To(MatchError(ContainSubstring("replicas is 2")))
	})
})

var _ = Describe("S3 connection settings", func() {
	It("honors virtual-host addressing instead of forcing path style", func() {
		config := &S3LogConfig{
			Bucket: &opgos3.BucketInfo{
				ConnectionInfo: opgos3.ConnectionInfo{
					Endpoint:  url.URL{Scheme: "https", Host: "s3.example.com"},
					Region:    "eu-west-1",
					PathStyle: false,
					TLS:       &s3v1alpha1.Tls{},
				},
				BucketName: "history",
			},
			Prefix: "events",
		}

		properties := config.SparkDefaults()
		Expect(properties).To(HaveKeyWithValue("spark.hadoop.fs.s3a.path.style.access", "false"))
		Expect(properties).To(HaveKeyWithValue("spark.hadoop.fs.s3a.endpoint.region", "eu-west-1"))
		Expect(properties).To(HaveKeyWithValue("spark.hadoop.fs.s3a.connection.ssl.enabled", trueValue))
	})

	It("accepts system WebPKI verification", func() {
		verification := &commonsv1alpha1.TLSVerificationSpec{
			Server: &commonsv1alpha1.ServerVerification{
				CACert: &commonsv1alpha1.CACert{WebPki: &commonsv1alpha1.WebPki{}},
			},
		}
		Expect(validateWebPKIVerification(verification, "S3 connection")).To(Succeed())
	})

	It("fails explicitly for verification modes the product cannot enforce", func() {
		verificationNone := &commonsv1alpha1.TLSVerificationSpec{
			None: &commonsv1alpha1.NoneVerification{},
		}
		Expect(validateWebPKIVerification(verificationNone, "S3 connection")).To(
			MatchError(ContainSubstring("verification.none is not supported")))

		privateCA := &commonsv1alpha1.TLSVerificationSpec{
			Server: &commonsv1alpha1.ServerVerification{
				CACert: &commonsv1alpha1.CACert{SecretClass: "private-ca"},
			},
		}
		Expect(validateWebPKIVerification(privateCA, "S3 connection")).To(
			MatchError(ContainSubstring("trust store")))
	})
})

var _ = Describe("ImageDefaults", func() {
	It("returns the custom image verbatim", func() {
		spec := &commonsv1alpha1.ImageSpec{Custom: "my.repo/spark:x"}
		Expect(spec.ResolveImage(shsv1alpha1.DefaultProductName, ImageDefaults())).To(Equal("my.repo/spark:x"))
	})

	It("assembles repo, product version and kubedoop version", func() {
		spec := &commonsv1alpha1.ImageSpec{
			Repo:            "quay.io/zncdatadev",
			ProductVersion:  "3.5.5",
			KubedoopVersion: "0.0.0-dev",
		}
		Expect(spec.ResolveImage(shsv1alpha1.DefaultProductName, ImageDefaults())).To(
			Equal("quay.io/zncdatadev/spark-k8s:3.5.5-kubedoop0.0.0-dev"))
	})

	It("defaults the product version when unset", func() {
		Expect((&commonsv1alpha1.ImageSpec{}).ResolveImage(
			shsv1alpha1.DefaultProductName, ImageDefaults())).To(
			ContainSubstring("/spark-k8s:" + shsv1alpha1.DefaultProductVersion + "-kubedoop"))
	})

	It("forwards pullSecretName into the generic image spec", func() {
		cr := testCR()
		cr.Spec.Image = &shsv1alpha1.ImageSpec{PullSecretName: "registry-credentials"}
		Expect(cr.GetSpec().Image.PullSecretName).To(Equal("registry-credentials"))
	})
})

var _ = Describe("Role declaration compatibility", func() {
	It("uses the legacy Vector aggregator signal as an overridable logging default", func() {
		scheme := newScheme()
		handler := NewSparkHistoryRoleGroupHandler(scheme)
		cr := testCR()
		cr.Spec.ClusterConfig.VectorAggregatorConfigMapName = "vector-aggregator"
		client := newFakeClient(scheme, minioObjects()...)

		catalog, err := handler.DeclareRoles(context.Background(), client, cr)
		Expect(err).NotTo(HaveOccurred())
		defaults := catalog[shsv1alpha1.RoleNode].ConfigDefaults
		Expect(defaults.Logging.EnableVectorAgent).To(Equal(ptr.To(true)))
		Expect(defaults.Logging.Containers[shsv1alpha1.RoleNode].Console.Level).To(Equal("INFO"))
		Expect(defaults.Logging.Containers[shsv1alpha1.RoleNode].File.Level).To(Equal("INFO"))

		folded, _, err := reconciler.FoldCommonConfig(
			defaults,
			&commonsv1alpha1.RoleGroupConfigSpec{
				Logging: &commonsv1alpha1.LoggingSpec{EnableVectorAgent: ptr.To(false)},
			},
			nil,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(folded.Logging.EnableVectorAgent).To(Equal(ptr.To(false)))
	})
})

var _ = Describe("OIDC wiring", func() {
	It("registers the oauth2-proxy sidecar and exposes the service port when OIDC is enabled", func() {
		scheme := newScheme()
		handler := NewSparkHistoryRoleGroupHandler(scheme)
		cr := testCR()
		cr.Spec.ClusterConfig.Authentication = &shsv1alpha1.AuthenticationSpec{
			AuthenticationClass: oidcClassName,
			Oidc: &shsv1alpha1.OidcSpec{
				ClientCredentialsSecret: oidcCredentials,
			},
		}

		Expect(authScheme(scheme)).To(Succeed())
		client := newFakeClient(scheme, append(minioObjects(), keycloakAuthClass())...)

		resources, err := handler.BuildResources(
			context.Background(), client, cr, testBuildContext(handler, client, cr))
		Expect(err).NotTo(HaveOccurred())

		By("exposing only the authenticated UI entrypoint on the client Service")
		var oidcPort *int32
		for _, p := range resources.Service.Spec.Ports {
			Expect(p.Name).NotTo(Equal(HttpPortName))
			if p.Name == OidcPortName {
				port := p.Port
				oidcPort = &port
			}
		}
		Expect(oidcPort).NotTo(BeNil())
		Expect(*oidcPort).To(Equal(int32(4180)))

		By("routing the metrics Service to JMX instead of the unauthenticated UI")
		Expect(resources.MetricsService.Spec.Ports).To(HaveLen(1))
		Expect(resources.MetricsService.Spec.Ports[0].TargetPort.String()).To(
			Equal(MetricsPortName))
		for _, service := range []*corev1.Service{resources.Service, resources.MetricsService} {
			for _, port := range service.Spec.Ports {
				Expect(port.TargetPort.String()).NotTo(Equal(HttpPortName))
			}
		}

		By("injecting oauth2-proxy as a native sidecar with the pinned image")
		var proxy *int
		inits := resources.StatefulSet.Spec.Template.Spec.InitContainers
		for i := range inits {
			if inits[i].Name == sidecar.OAuth2ProxySidecarName {
				proxy = &i
			}
		}
		Expect(proxy).NotTo(BeNil())
		Expect(inits[*proxy].Image).To(Equal(sidecar.DefaultOAuth2ProxyImage))
		envByName := map[string]string{}
		for _, e := range inits[*proxy].Env {
			envByName[e.Name] = e.Value
		}
		Expect(envByName["OAUTH2_PROXY_OIDC_ISSUER_URL"]).To(
			Equal("http://keycloak.test-ns.svc.cluster.local:8080/realms/kubedoop"))
		Expect(envByName["OAUTH2_PROXY_PROVIDER"]).To(Equal("keycloak-oidc"))
		Expect(envByName["OAUTH2_PROXY_UPSTREAMS"]).To(Equal("http://localhost:18080"))
	})

	It("rejects a legacy regular oauth2-proxy container instead of absorbing it into the native sidecar", func() {
		for _, includeLegacyOIDCOverride := range []bool{false, true} {
			scheme := newScheme()
			handler := NewSparkHistoryRoleGroupHandler(scheme)
			cr := testCR()
			cr.Spec.ClusterConfig.Authentication = &shsv1alpha1.AuthenticationSpec{
				AuthenticationClass: oidcClassName,
				Oidc:                &shsv1alpha1.OidcSpec{ClientCredentialsSecret: oidcCredentials},
			}
			Expect(authScheme(scheme)).To(Succeed())
			objects := append(minioObjects(), keycloakAuthClass(), legacyOwnedStatefulSet(cr))
			client := newFakeClient(scheme, objects...)
			containers := []corev1.Container{{
				Name:  sidecar.OAuth2ProxySidecarName,
				Image: "registry.example/custom-oauth2-helper:v1",
			}}
			if includeLegacyOIDCOverride {
				containers = append([]corev1.Container{{Name: OidcPortName}}, containers...)
			}
			buildCtx := testBuildContext(handler, client, cr)
			buildCtx.MergedConfig.PodOverrides = &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: containers},
			}

			_, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
			Expect(err).To(HaveOccurred())
			Expect(reconciler.IsValidationError(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring(
				`legacy workload has a regular container named "oauth2-proxy"`))
			Expect(err.Error()).To(ContainSubstring(
				`only the legacy "oidc" container can be adopted automatically`))

			cookieSecret := &corev1.Secret{}
			Expect(client.Get(context.Background(), ctrlclient.ObjectKey{
				Name: cr.Name + oidcCookieSecretSuffix, Namespace: cr.Namespace,
			}, cookieSecret)).To(MatchError(apierrors.IsNotFound, "IsNotFound"))
		}
	})

	It("migrates legacy OIDC container overrides onto the native sidecar", func() {
		scheme := newScheme()
		handler := NewSparkHistoryRoleGroupHandler(scheme)
		cr := testCR()
		cr.Spec.ClusterConfig.Authentication = &shsv1alpha1.AuthenticationSpec{
			AuthenticationClass: oidcClassName,
			Oidc: &shsv1alpha1.OidcSpec{
				ClientCredentialsSecret: oidcCredentials,
			},
		}
		Expect(authScheme(scheme)).To(Succeed())
		client := newFakeClient(
			scheme,
			append(minioObjects(), keycloakAuthClass(), legacyOwnedStatefulSet(cr))...,
		)
		buildCtx := testBuildContext(handler, client, cr)
		memoryLimit := resource.MustParse("384Mi")
		buildCtx.MergedConfig.PodOverrides = &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name:  OidcPortName,
				Image: "registry.example/oauth2-proxy:v7",
				Env: []corev1.EnvVar{{
					Name:  "OAUTH2_PROXY_COOKIE_SECURE",
					Value: trueValue,
				}},
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{corev1.ResourceMemory: memoryLimit},
				},
			}}},
		}

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		for _, container := range resources.StatefulSet.Spec.Template.Spec.Containers {
			Expect(container.Name).NotTo(Equal(OidcPortName))
		}
		var proxy *corev1.Container
		for i := range resources.StatefulSet.Spec.Template.Spec.InitContainers {
			if resources.StatefulSet.Spec.Template.Spec.InitContainers[i].Name == sidecar.OAuth2ProxySidecarName {
				proxy = &resources.StatefulSet.Spec.Template.Spec.InitContainers[i]
			}
		}
		Expect(proxy).NotTo(BeNil())
		Expect(proxy.Image).To(Equal("registry.example/oauth2-proxy:v7"))
		Expect(proxy.Resources.Limits).To(HaveKeyWithValue(corev1.ResourceMemory, memoryLimit))
		Expect(proxy.Resources.Limits).To(
			HaveKeyWithValue(corev1.ResourceCPU, resource.MustParse("600m")))
		env := map[string]string{}
		for _, variable := range proxy.Env {
			env[variable.Name] = variable.Value
		}
		Expect(env).To(HaveKeyWithValue("OAUTH2_PROXY_COOKIE_SECURE", trueValue))
		Expect(proxy.RestartPolicy).To(Equal(ptr.To(corev1.ContainerRestartPolicyAlways)))
	})

	It("lets a fresh v0.13 workload patch the native oauth2-proxy name", func() {
		scheme := newScheme()
		handler := NewSparkHistoryRoleGroupHandler(scheme)
		cr := testCR()
		cr.Spec.ClusterConfig.Authentication = &shsv1alpha1.AuthenticationSpec{
			AuthenticationClass: oidcClassName,
			Oidc:                &shsv1alpha1.OidcSpec{ClientCredentialsSecret: oidcCredentials},
		}
		Expect(authScheme(scheme)).To(Succeed())
		client := newFakeClient(scheme, append(minioObjects(), keycloakAuthClass())...)
		buildCtx := testBuildContext(handler, client, cr)
		buildCtx.MergedConfig.PodOverrides = &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name:  sidecar.OAuth2ProxySidecarName,
				Image: "registry.example/oauth2-proxy:fresh",
				Env: []corev1.EnvVar{{
					Name:  "FRESH_NATIVE_PATCH",
					Value: trueValue,
				}},
			}}},
		}

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		Expect(resources.StatefulSet.Spec.Template.Spec.Containers).NotTo(
			ContainElement(HaveField("Name", sidecar.OAuth2ProxySidecarName)))
		Expect(resources.StatefulSet.Spec.Template.Spec.InitContainers).To(ContainElement(And(
			HaveField("Name", sidecar.OAuth2ProxySidecarName),
			HaveField("Image", "registry.example/oauth2-proxy:fresh"),
			HaveField("Env", ContainElement(And(
				HaveField("Name", "FRESH_NATIVE_PATCH"),
				HaveField("Value", trueValue),
			))),
		)))
	})

	It("strategically merges the full legacy OIDC container override onto the native sidecar", func() {
		scheme := newScheme()
		handler := NewSparkHistoryRoleGroupHandler(scheme)
		cr := testCR()
		cr.Spec.ClusterConfig.Authentication = &shsv1alpha1.AuthenticationSpec{
			AuthenticationClass: oidcClassName,
			Oidc:                &shsv1alpha1.OidcSpec{ClientCredentialsSecret: oidcCredentials},
		}
		Expect(authScheme(scheme)).To(Succeed())
		client := newFakeClient(scheme, append(minioObjects(), keycloakAuthClass())...)
		buildCtx := testBuildContext(handler, client, cr)
		buildCtx.MergedConfig.PodOverrides = &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name:       OidcPortName,
				Command:    []string{"/legacy-wrapper"},
				Args:       []string{"--serve"},
				WorkingDir: "/work",
				Env: []corev1.EnvVar{{
					Name: "EXTRA_FROM_CONFIG",
					ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "sidecar-extra"},
						Key:                  "value",
					}},
				}},
				Lifecycle: &corev1.Lifecycle{PreStop: &corev1.LifecycleHandler{
					Exec: &corev1.ExecAction{Command: []string{"/bin/true"}},
				}},
			}}},
		}

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		var proxy *corev1.Container
		for i := range resources.StatefulSet.Spec.Template.Spec.InitContainers {
			candidate := &resources.StatefulSet.Spec.Template.Spec.InitContainers[i]
			if candidate.Name == sidecar.OAuth2ProxySidecarName {
				proxy = candidate
			}
		}
		Expect(proxy).NotTo(BeNil())
		Expect(proxy.Command).To(Equal([]string{"/legacy-wrapper"}))
		Expect(proxy.Args).To(Equal([]string{"--serve"}))
		Expect(proxy.WorkingDir).To(Equal("/work"))
		Expect(proxy.Lifecycle.PreStop.Exec.Command).To(Equal([]string{"/bin/true"}))
		Expect(proxy.Env).To(ContainElement(And(
			HaveField("Name", "EXTRA_FROM_CONFIG"),
			HaveField("ValueFrom.ConfigMapKeyRef.Name", "sidecar-extra"),
		)))
		Expect(proxy.RestartPolicy).To(Equal(ptr.To(corev1.ContainerRestartPolicyAlways)))
	})

	It("merges an additional legacy OIDC port without disconnecting the client Service", func() {
		scheme := newScheme()
		handler := NewSparkHistoryRoleGroupHandler(scheme)
		cr := testCR()
		cr.Spec.ClusterConfig.Authentication = &shsv1alpha1.AuthenticationSpec{
			AuthenticationClass: oidcClassName,
			Oidc:                &shsv1alpha1.OidcSpec{ClientCredentialsSecret: oidcCredentials},
		}
		Expect(authScheme(scheme)).To(Succeed())
		client := newFakeClient(scheme, append(minioObjects(), keycloakAuthClass())...)
		buildCtx := testBuildContext(handler, client, cr)
		buildCtx.MergedConfig.PodOverrides = &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name:  OidcPortName,
				Ports: []corev1.ContainerPort{{Name: "custom", ContainerPort: 9999}},
			}}},
		}

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		var proxy *corev1.Container
		for i := range resources.StatefulSet.Spec.Template.Spec.InitContainers {
			candidate := &resources.StatefulSet.Spec.Template.Spec.InitContainers[i]
			if candidate.Name == sidecar.OAuth2ProxySidecarName {
				proxy = candidate
			}
		}
		Expect(proxy).NotTo(BeNil())
		Expect(proxy.Ports).To(ContainElements(
			HaveField("ContainerPort", int32(OidcPort)),
			HaveField("ContainerPort", int32(9999)),
		))
	})

	It("fails closed when authentication is configured without OIDC settings", func() {
		scheme := newScheme()
		handler := NewSparkHistoryRoleGroupHandler(scheme)
		cr := testCR()
		cr.Spec.ClusterConfig.Authentication = &shsv1alpha1.AuthenticationSpec{
			AuthenticationClass: oidcClassName,
		}
		client := newFakeClient(scheme, minioObjects()...)

		_, err := handler.BuildResources(
			context.Background(), client, cr, testBuildContext(handler, client, cr))
		Expect(err).To(MatchError(ContainSubstring(
			"spec.clusterConfig.authentication.oidc is required")))
	})

	It("fails loudly when the AuthenticationClass has no OIDC provider", func() {
		scheme := newScheme()
		handler := NewSparkHistoryRoleGroupHandler(scheme)
		cr := testCR()
		cr.Spec.ClusterConfig.Authentication = &shsv1alpha1.AuthenticationSpec{
			AuthenticationClass: "not-oidc",
			Oidc:                &shsv1alpha1.OidcSpec{ClientCredentialsSecret: "creds"},
		}

		Expect(authScheme(scheme)).To(Succeed())
		notOidc := keycloakAuthClass()
		notOidc.Name = "not-oidc"
		notOidc.Spec.AuthenticationProvider.OIDC = nil
		client := newFakeClient(scheme, append(minioObjects(), notOidc)...)

		_, err := handler.BuildResources(
			context.Background(), client, cr, testBuildContext(handler, client, cr))
		Expect(err).To(MatchError(ContainSubstring("does not define an OIDC provider")))
	})
})

var _ = Describe("legacy Vector container overrides", func() {
	It("drops the old Vector patch when the folded logging config disables the sidecar", func() {
		buildCtx := &reconciler.RoleGroupBuildContext{
			SidecarManager: sidecar.NewSidecarManager(),
			Declaration: reconciler.RoleDeclaration{ConfigDefaults: &commonsv1alpha1.RoleGroupConfigSpec{
				Logging: &commonsv1alpha1.LoggingSpec{EnableVectorAgent: ptr.To(true)},
			}},
			MergedConfig: &opgoconfig.MergedConfig{PodOverrides: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name: vector.VectorSidecarName,
					Env:  []corev1.EnvVar{{Name: "LEGACY_VECTOR_SETTING", Value: trueValue}},
				}}},
			}},
		}

		overrides := extractLegacySidecarPodOverrides(buildCtx)
		Expect(overrides).To(BeEmpty())
		Expect(buildCtx.MergedConfig.PodOverrides.Spec.Containers).To(BeEmpty())
	})

	It("keeps an unrelated regular container named vector when no legacy aggregator was configured", func() {
		buildCtx := &reconciler.RoleGroupBuildContext{
			SidecarManager: sidecar.NewSidecarManager(),
			MergedConfig: &opgoconfig.MergedConfig{PodOverrides: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name:  vector.VectorSidecarName,
					Image: "example/custom-vector-helper:v1",
				}}},
			}},
		}

		overrides := extractLegacySidecarPodOverrides(buildCtx)
		Expect(overrides).To(BeEmpty())
		Expect(buildCtx.MergedConfig.PodOverrides.Spec.Containers).To(ConsistOf(
			HaveField("Image", "example/custom-vector-helper:v1")))
	})

	It("preserves strategic merge semantics for mounts, ports and partial readiness probes", func() {
		manager := sidecar.NewSidecarManager()
		provider := vector.NewVectorSidecarProvider("quay.io/example/spark:latest")
		manager.Register(provider, &sidecar.SidecarConfig{Enabled: true})
		buildCtx := &reconciler.RoleGroupBuildContext{
			SidecarManager: manager,
			MergedConfig: &opgoconfig.MergedConfig{PodOverrides: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name:    vector.VectorSidecarName,
					Command: []string{"/legacy-vector-wrapper"},
					VolumeMounts: []corev1.VolumeMount{{
						Name:      "config",
						MountPath: constant.KubedoopConfigDir,
						ReadOnly:  false,
					}},
					Ports: []corev1.ContainerPort{{
						Name:          "legacy-extra",
						ContainerPort: 9999,
					}},
					ReadinessProbe: &corev1.Probe{TimeoutSeconds: 7},
				}}},
			}},
		}

		overrides := extractLegacySidecarPodOverrides(buildCtx)
		Expect(buildCtx.MergedConfig.PodOverrides.Spec.Containers).To(BeEmpty())

		podSpec := corev1.PodSpec{}
		Expect(provider.Inject(&podSpec, &sidecar.SidecarConfig{Enabled: true})).To(Succeed())
		statefulSet := &appsv1.StatefulSet{
			Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: podSpec}},
		}
		Expect(applyLegacySidecarPodOverrides(statefulSet, overrides)).To(Succeed())

		var migrated *corev1.Container
		for i := range statefulSet.Spec.Template.Spec.InitContainers {
			candidate := &statefulSet.Spec.Template.Spec.InitContainers[i]
			if candidate.Name == vector.VectorSidecarName {
				migrated = candidate
			}
		}
		Expect(migrated).NotTo(BeNil())
		Expect(migrated.Command).To(Equal([]string{"/legacy-vector-wrapper"}))
		Expect(migrated.Ports).To(ContainElement(
			HaveField("ContainerPort", int32(9999))))
		Expect(migrated.VolumeMounts).To(ContainElement(And(
			HaveField("Name", vector.VectorConfigVolumeName),
			HaveField("MountPath", vector.VectorConfigMountPath),
			HaveField("ReadOnly", false),
		)))
		Expect(migrated.ReadinessProbe.HTTPGet.Path).To(Equal("/health"))
		Expect(migrated.ReadinessProbe.HTTPGet.Port.IntValue()).To(Equal(vector.VectorAPIPort))
		Expect(migrated.ReadinessProbe.TimeoutSeconds).To(Equal(int32(7)))
		Expect(migrated.RestartPolicy).To(Equal(ptr.To(corev1.ContainerRestartPolicyAlways)))
	})
})

var _ = Describe("native sidecar image identities", func() {
	It("pins the numeric kubedoop identity for the native Vector container", func() {
		scheme := newScheme()
		handler := NewSparkHistoryRoleGroupHandler(scheme)
		cr := testCR()
		client := newFakeClient(scheme, minioObjects()...)
		buildCtx := testBuildContext(handler, client, cr)
		buildCtx.SidecarManager.Register(
			vector.NewVectorSidecarProvider(
				buildCtx.ResolvedImage.Reference,
				vector.WithConfigMapName(buildCtx.ResourceName),
			),
			&sidecar.SidecarConfig{Enabled: true},
		)

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		Expect(resources.StatefulSet.Spec.Template.Spec.InitContainers).To(ContainElement(And(
			HaveField("Name", vector.VectorSidecarName),
			HaveField("SecurityContext.RunAsUser", ptr.To(kubedoopImageUID)),
			HaveField("SecurityContext.RunAsGroup", ptr.To(kubedoopImageGID)),
			HaveField("SecurityContext.RunAsNonRoot", ptr.To(true)),
			HaveField("SecurityContext.ReadOnlyRootFilesystem", ptr.To(true)),
		)))
	})

	It("keeps a numeric pod securityContext above the Vector identity default", func() {
		scheme := newScheme()
		handler := NewSparkHistoryRoleGroupHandler(scheme)
		cr := testCR()
		client := newFakeClient(scheme, minioObjects()...)
		buildCtx := testBuildContext(handler, client, cr)
		buildCtx.SidecarManager.Register(
			vector.NewVectorSidecarProvider(
				buildCtx.ResolvedImage.Reference,
				vector.WithConfigMapName(buildCtx.ResourceName),
			),
			&sidecar.SidecarConfig{Enabled: true},
		)
		buildCtx.MergedConfig.PodOverrides.Spec.SecurityContext = &corev1.PodSecurityContext{
			RunAsUser:  ptr.To(int64(2000)),
			RunAsGroup: ptr.To(int64(2001)),
		}

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		Expect(resources.StatefulSet.Spec.Template.Spec.InitContainers).To(ContainElement(And(
			HaveField("Name", vector.VectorSidecarName),
			HaveField("SecurityContext.RunAsUser", BeNil()),
			HaveField("SecurityContext.RunAsGroup", BeNil()),
		)))
		Expect(resources.StatefulSet.Spec.Template.Spec.SecurityContext.RunAsUser).To(Equal(ptr.To(int64(2000))))
		Expect(resources.StatefulSet.Spec.Template.Spec.SecurityContext.RunAsGroup).To(Equal(ptr.To(int64(2001))))
	})

	It("keeps native sidecars non-root when the pod identity is explicitly root", func() {
		scheme := newScheme()
		handler := NewSparkHistoryRoleGroupHandler(scheme)
		cr := testCR()
		cr.Spec.ClusterConfig.Authentication = &shsv1alpha1.AuthenticationSpec{
			AuthenticationClass: oidcClassName,
			Oidc: &shsv1alpha1.OidcSpec{
				ClientCredentialsSecret: oidcCredentials,
			},
		}
		Expect(authScheme(scheme)).To(Succeed())
		client := newFakeClient(scheme, append(minioObjects(), keycloakAuthClass())...)
		buildCtx := testBuildContext(handler, client, cr)
		buildCtx.MergedConfig.PodOverrides.Spec.SecurityContext = &corev1.PodSecurityContext{
			RunAsUser:  ptr.To(int64(0)),
			RunAsGroup: ptr.To(int64(0)),
		}
		buildCtx.SidecarManager.Register(
			vector.NewVectorSidecarProvider(
				buildCtx.ResolvedImage.Reference,
				vector.WithConfigMapName(buildCtx.ResourceName),
			),
			&sidecar.SidecarConfig{Enabled: true},
		)

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		Expect(resources.StatefulSet.Spec.Template.Spec.SecurityContext.RunAsUser).To(Equal(ptr.To(int64(0))))
		Expect(resources.StatefulSet.Spec.Template.Spec.InitContainers).To(ContainElements(
			And(
				HaveField("Name", vector.VectorSidecarName),
				HaveField("SecurityContext.RunAsUser", ptr.To(kubedoopImageUID)),
				HaveField("SecurityContext.RunAsNonRoot", ptr.To(true)),
			),
			And(
				HaveField("Name", sidecar.OAuth2ProxySidecarName),
				HaveField("SecurityContext.RunAsUser", ptr.To(oauth2ProxyImageUID)),
				HaveField("SecurityContext.RunAsNonRoot", ptr.To(true)),
			),
		))
	})

	It("applies a Vector container securityContext podOverride after the identity default", func() {
		scheme := newScheme()
		handler := NewSparkHistoryRoleGroupHandler(scheme)
		cr := testCR()
		client := newFakeClient(scheme, append(minioObjects(), legacyOwnedStatefulSet(cr))...)
		buildCtx := testBuildContext(handler, client, cr)
		buildCtx.SidecarManager.Register(
			vector.NewVectorSidecarProvider(
				buildCtx.ResolvedImage.Reference,
				vector.WithConfigMapName(buildCtx.ResourceName),
			),
			&sidecar.SidecarConfig{Enabled: true},
		)
		userSecurityContext := &corev1.SecurityContext{
			RunAsUser:    ptr.To(int64(3000)),
			RunAsGroup:   ptr.To(int64(3001)),
			RunAsNonRoot: ptr.To(false),
		}
		buildCtx.MergedConfig.PodOverrides.Spec.Containers = []corev1.Container{{
			Name:            vector.VectorSidecarName,
			SecurityContext: userSecurityContext,
		}}

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		Expect(resources.StatefulSet.Spec.Template.Spec.InitContainers).To(ContainElement(And(
			HaveField("Name", vector.VectorSidecarName),
			HaveField("SecurityContext", userSecurityContext),
		)))
	})
})
