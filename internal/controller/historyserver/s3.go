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

	commonsv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/commons/v1alpha1"
	"github.com/zncdatadev/operator-go/pkg/s3"
	"github.com/zncdatadev/operator-go/pkg/security"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	shsv1alpha1 "github.com/zncdatadev/spark-k8s-operator/api/v1alpha1"
)

// S3LogConfig is the resolved S3 event-log location: the bucket (with its connection facts
// and credentials) plus the CR's log prefix.
type S3LogConfig struct {
	Bucket *s3.BucketInfo
	Prefix string
}

// resolveS3LogConfig resolves spec.clusterConfig.logFileDirectory.s3 (required by the CRD)
// through the operator-go S3 resolver.
func resolveS3LogConfig(ctx context.Context, client ctrlclient.Client, namespace string, s3Spec *shsv1alpha1.S3Spec) (*S3LogConfig, error) {
	if s3Spec == nil || s3Spec.Bucket == nil {
		return nil, fmt.Errorf("spec.clusterConfig.logFileDirectory.s3.bucket is required")
	}
	bucket, err := s3.ResolveBucket(ctx, client, namespace, s3Spec.Bucket.Inline, s3Spec.Bucket.Reference)
	if err != nil {
		return nil, err
	}
	if bucket.TLS != nil {
		if err := validateWebPKIVerification(bucket.TLS.Verification, "S3 connection"); err != nil {
			return nil, err
		}
	}
	return &S3LogConfig{Bucket: bucket, Prefix: s3Spec.Prefix}, nil
}

// SparkDefaults renders the S3-driven spark-defaults.conf properties: the event-log
// directory plus the s3a client settings, prefixed with "spark.hadoop." as Spark requires.
func (c *S3LogConfig) SparkDefaults() map[string]string {
	props := map[string]string{
		"spark.history.fs.logDirectory": c.Bucket.S3AURI(c.Prefix),
	}
	for key, value := range c.Bucket.S3AProperties() {
		props["spark.hadoop."+key] = value
	}
	return props
}

// validateWebPKIVerification accepts the verification modes the shipped containers can enforce.
// A nil verification uses the runtime's system WebPKI roots. Custom SecretClass CAs need explicit
// trust-store delivery, and verification.none needs a product-specific insecure transport flag;
// neither may be silently ignored because the workload would fail only on its first remote call.
func validateWebPKIVerification(verification *commonsv1alpha1.TLSVerificationSpec, source string) error {
	if verification == nil {
		return nil
	}
	if verification.None != nil && verification.Server != nil {
		return fmt.Errorf("%s TLS verification must set exactly one of none or server", source)
	}
	if verification.None != nil {
		return fmt.Errorf("%s TLS verification.none is not supported; use server.caCert.webPki", source)
	}
	if verification.Server == nil || verification.Server.CACert == nil {
		return fmt.Errorf("%s TLS verification.server.caCert is required", source)
	}
	caCert := verification.Server.CACert
	if caCert.SecretClass != "" && caCert.WebPki != nil {
		return fmt.Errorf("%s TLS CA must set exactly one of secretClass or webPki", source)
	}
	if caCert.SecretClass != "" {
		return fmt.Errorf("%s TLS CA secretClass %q is not supported until the product wires a trust store; use webPki", source, caCert.SecretClass)
	}
	if caCert.WebPki == nil {
		return fmt.Errorf("%s TLS CA must set webPki", source)
	}
	return nil
}

// CredentialsProvisioner returns the CSI credentials volume provisioner (mounted at
// /kubedoop/secret/s3-credentials, the e2e-asserted path), or nil for anonymous access.
func (c *S3LogConfig) CredentialsProvisioner() *security.SecretProvisioner {
	return c.Bucket.CredentialsProvisioner(s3.DefaultCredentialsVolumeName)
}

// legacyS3CredentialsProvider keeps the v0.12 Secret volume shape while the base handler
// adopts an existing StatefulSet. The delegate still owns annotations, scope validation, and
// mount paths; only Spark's historical storage request and mount mode differ from v0.13 defaults.
// Because this provider runs before the framework merges podOverrides, explicit user values keep
// their normal highest precedence.
type legacyS3CredentialsProvider struct {
	*security.SecretProvisioner
}

func (p *legacyS3CredentialsProvider) Volumes() []corev1.Volume {
	volumes := p.SecretProvisioner.Volumes()
	for i := range volumes {
		volume := &volumes[i]
		if volume.Name != s3.DefaultCredentialsVolumeName || volume.Ephemeral == nil ||
			volume.Ephemeral.VolumeClaimTemplate == nil {
			continue
		}
		volume.Ephemeral.VolumeClaimTemplate.Spec.Resources.Requests[corev1.ResourceStorage] =
			resource.MustParse("1Mi")
	}
	return volumes
}

func (p *legacyS3CredentialsProvider) VolumeMounts() []corev1.VolumeMount {
	mounts := p.SecretProvisioner.VolumeMounts()
	for i := range mounts {
		if mounts[i].Name == s3.DefaultCredentialsVolumeName &&
			mounts[i].MountPath == s3.CredentialsMountPath(s3.DefaultCredentialsVolumeName) {
			mounts[i].ReadOnly = false
		}
	}
	return mounts
}

// CredentialsExportScript returns the shell fragment exporting the mounted credentials as
// AWS SDK env vars, or "" for anonymous access.
func (c *S3LogConfig) CredentialsExportScript() string {
	if c.Bucket.Credentials == nil {
		return ""
	}
	return s3.CredentialsExportScript(s3.CredentialsMountPath(s3.DefaultCredentialsVolumeName))
}
