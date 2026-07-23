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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	authv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/authentication/v1alpha1"
	commonsv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/commons/v1alpha1"
	s3v1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/s3/v1alpha1"
	opgoconfig "github.com/zncdatadev/operator-go/pkg/config"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	"github.com/zncdatadev/operator-go/pkg/sidecar"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	shsv1alpha1 "github.com/zncdatadev/spark-k8s-operator/api/v1alpha1"
)

const (
	testNamespace    = "test-ns"
	defaultRoleGroup = "default"
	minioName        = "minio"
	bucketName       = "spark-history"
	oidcClassName    = "oidc"
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
				Host: minioName,
				Port: 9000,
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

func testBuildContext(cr *shsv1alpha1.SparkHistoryServer) *reconciler.RoleGroupBuildContext {
	return &reconciler.RoleGroupBuildContext{
		ClusterName:      cr.Name,
		ClusterNamespace: cr.Namespace,
		ClusterSpec:      cr.GetSpec(),
		RoleName:         shsv1alpha1.RoleNode,
		RoleGroupName:    defaultRoleGroup,
		RoleGroupSpec:    cr.GetSpec().Roles[shsv1alpha1.RoleNode].RoleGroups[defaultRoleGroup],
		MergedConfig:     &opgoconfig.MergedConfig{},
		ResourceName:     reconciler.RoleGroupResourceName(cr.Name, shsv1alpha1.RoleNode, defaultRoleGroup),
		SidecarManager:   sidecar.NewSidecarManager(),
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
		buildCtx := testBuildContext(cr)

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())

		By("naming the StatefulSet <cluster>-node-<group> with container 'node'")
		Expect(resources.StatefulSet.Name).To(Equal("sparkhistory-node-default"))
		containers := resources.StatefulSet.Spec.Template.Spec.Containers
		Expect(containers).To(HaveLen(1))
		Expect(containers[0].Name).To(Equal("node"))

		By("labeling pods with the e2e-selected app name")
		Expect(resources.StatefulSet.Spec.Template.Labels).To(
			HaveKeyWithValue("app.kubernetes.io/name", "sparkhistoryserver"))

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

		By("starting the history server with the copied config and exported credentials")
		Expect(containers[0].Args).To(HaveLen(1))
		script := containers[0].Args[0]
		Expect(script).To(ContainSubstring("cp -RL /kubedoop/mount/config/* /kubedoop/config"))
		Expect(script).To(ContainSubstring(`export AWS_ACCESS_KEY_ID="$(cat /kubedoop/secret/s3-credentials/ACCESS_KEY)"`))
		Expect(script).To(ContainSubstring("exec /kubedoop/spark/sbin/start-history-server.sh --properties-file /kubedoop/config/spark-defaults.conf"))

		By("rendering spark-defaults.conf with the S3 event-log location")
		Expect(resources.ConfigMap.Name).To(Equal("sparkhistory-node-default"))
		sparkDefaults := resources.ConfigMap.Data["spark-defaults.conf"]
		Expect(sparkDefaults).To(ContainSubstring("spark.history.fs.logDirectory=s3a://spark-history/events"))
		Expect(sparkDefaults).To(ContainSubstring("spark.hadoop.fs.s3a.endpoint=http"))
		Expect(sparkDefaults).To(ContainSubstring("spark.hadoop.fs.s3a.path.style.access=true"))
		Expect(sparkDefaults).To(ContainSubstring("spark.hadoop.fs.s3a.connection.ssl.enabled=false"))

		By("rendering the log4j2 config under the e2e-asserted key")
		Expect(resources.ConfigMap.Data).To(HaveKey("log4j2.properties"))

		By("building the metrics Service with the e2e-asserted shape")
		metrics := resources.MetricsService
		Expect(metrics.Name).To(Equal("sparkhistory-node-default-metrics"))
		Expect(metrics.Spec.ClusterIP).To(Equal("None"))
		Expect(metrics.Labels).To(HaveKeyWithValue("prometheus.io/scrape", "true"))
		Expect(metrics.Annotations).To(HaveKeyWithValue("prometheus.io/port", "18081"))
		Expect(metrics.Annotations).To(HaveKeyWithValue("prometheus.io/scheme", "http"))
		Expect(metrics.Spec.Ports).To(HaveLen(1))
		Expect(metrics.Spec.Ports[0].Name).To(Equal("http"))
		Expect(metrics.Spec.Ports[0].Port).To(Equal(int32(18081)))
		Expect(metrics.Spec.Ports[0].TargetPort.String()).To(Equal("http"))
		Expect(metrics.Spec.Selector).To(HaveKeyWithValue("app.kubernetes.io/component", "node"))
		Expect(metrics.Spec.Selector).To(HaveKeyWithValue("app.kubernetes.io/instance", "sparkhistory"))

		By("keeping the client Service ClusterIP without an OIDC port")
		Expect(resources.Service.Name).To(Equal("sparkhistory-node-default"))
		Expect(resources.Service.Spec.Type).To(BeEquivalentTo(""))
		for _, p := range resources.Service.Spec.Ports {
			Expect(p.Name).NotTo(Equal(OidcPortName))
		}

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

	It("maps listenerClass to the Service type", func() {
		client := newFakeClient(scheme, minioObjects()...)
		cr.Spec.ClusterConfig.ListenerClass = "external-unstable"

		resources, err := handler.BuildResources(context.Background(), client, cr, testBuildContext(cr))
		Expect(err).NotTo(HaveOccurred())
		Expect(resources.Service.Spec.Type).To(BeEquivalentTo("NodePort"))
	})

	It("keeps user configOverrides over the product-computed spark-defaults", func() {
		client := newFakeClient(scheme, minioObjects()...)
		buildCtx := testBuildContext(cr)
		buildCtx.MergedConfig.ConfigFiles = map[string]map[string]string{
			"spark-defaults.conf": {"spark.history.fs.logDirectory": "s3a://user-bucket/logs"},
		}

		resources, err := handler.BuildResources(context.Background(), client, cr, buildCtx)
		Expect(err).NotTo(HaveOccurred())
		Expect(resources.ConfigMap.Data["spark-defaults.conf"]).To(
			ContainSubstring("spark.history.fs.logDirectory=s3a://user-bucket/logs"))
	})

	It("fails loudly when the referenced S3 bucket is missing", func() {
		client := newFakeClient(scheme)
		_, err := handler.BuildResources(context.Background(), client, cr, testBuildContext(cr))
		Expect(err).To(MatchError(ContainSubstring(`S3Bucket "spark-history"`)))
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

	It("rejects a role-level cleaner with multiple role groups", func() {
		groups := map[string]*shsv1alpha1.RoleGroupSpec{"a": {}, "b": {}}
		_, err := cleanerEnabled(role(ptr.To(true), groups), "a")
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

var _ = Describe("resolveImage", func() {
	It("returns the custom image verbatim", func() {
		Expect(resolveImage(&shsv1alpha1.ImageSpec{Custom: "my.repo/spark:x"})).To(Equal("my.repo/spark:x"))
	})

	It("assembles repo, product version and kubedoop version", func() {
		image := resolveImage(&shsv1alpha1.ImageSpec{
			Repo:            "quay.io/zncdatadev",
			ProductVersion:  "3.5.5",
			KubedoopVersion: "0.0.0-dev",
		})
		Expect(image).To(Equal("quay.io/zncdatadev/spark-k8s:3.5.5-kubedoop0.0.0-dev"))
	})

	It("defaults the product version when unset", func() {
		Expect(resolveImage(nil)).To(
			ContainSubstring("/spark-k8s:" + shsv1alpha1.DefaultProductVersion + "-kubedoop"))
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
				ClientCredentialsSecret: "oidc-credentials",
			},
		}

		Expect(authScheme(scheme)).To(Succeed())
		client := newFakeClient(scheme, append(minioObjects(), keycloakAuthClass())...)

		resources, err := handler.BuildResources(context.Background(), client, cr, testBuildContext(cr))
		Expect(err).NotTo(HaveOccurred())

		By("exposing the oidc port on the client Service")
		var oidcPort *int32
		for _, p := range resources.Service.Spec.Ports {
			if p.Name == OidcPortName {
				port := p.Port
				oidcPort = &port
			}
		}
		Expect(oidcPort).NotTo(BeNil())
		Expect(*oidcPort).To(Equal(int32(4180)))

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

		_, err := handler.BuildResources(context.Background(), client, cr, testBuildContext(cr))
		Expect(err).To(MatchError(ContainSubstring("does not define an OIDC provider")))
	})
})
