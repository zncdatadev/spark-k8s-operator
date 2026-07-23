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

	"github.com/zncdatadev/operator-go/pkg/s3"
	"github.com/zncdatadev/operator-go/pkg/security"
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
	// Path-style access is intentionally always on: most self-hosted S3 backends (MinIO)
	// require it, and the CRD's pathStyle default (false) predates any consumer of the
	// virtual-host style — flipping it must be coordinated with the e2e fixtures.
	props["spark.hadoop.fs.s3a.path.style.access"] = trueValue
	return props
}

// CredentialsProvisioner returns the CSI credentials volume provisioner (mounted at
// /kubedoop/secret/s3-credentials, the e2e-asserted path), or nil for anonymous access.
func (c *S3LogConfig) CredentialsProvisioner() *security.SecretProvisioner {
	return c.Bucket.CredentialsProvisioner(s3.DefaultCredentialsVolumeName)
}

// CredentialsExportScript returns the shell fragment exporting the mounted credentials as
// AWS SDK env vars, or "" for anonymous access.
func (c *S3LogConfig) CredentialsExportScript() string {
	if c.Bucket.Credentials == nil {
		return ""
	}
	return s3.CredentialsExportScript(s3.CredentialsMountPath(s3.DefaultCredentialsVolumeName))
}
