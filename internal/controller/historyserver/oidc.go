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

	authv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/authentication/v1alpha1"
	"github.com/zncdatadev/operator-go/pkg/sidecar"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	shsv1alpha1 "github.com/zncdatadev/spark-k8s-operator/api/v1alpha1"
)

// resolveOIDCProvider fetches the AuthenticationClass referenced by
// spec.clusterConfig.authentication (from the CR namespace) and asserts it carries an OIDC
// provider. Returns (nil, nil) when authentication is not configured for OIDC at all.
func resolveOIDCProvider(ctx context.Context, client ctrlclient.Client, namespace string, auth *shsv1alpha1.AuthenticationSpec) (*authv1alpha1.OIDCProvider, error) {
	if auth == nil || auth.Oidc == nil {
		return nil, nil
	}

	authClass := &authv1alpha1.AuthenticationClass{}
	if err := client.Get(ctx, ctrlclient.ObjectKey{Namespace: namespace, Name: auth.AuthenticationClass}, authClass); err != nil {
		return nil, fmt.Errorf("failed to get AuthenticationClass %q: %w", auth.AuthenticationClass, err)
	}

	provider := authClass.Spec.AuthenticationProvider
	if provider == nil || provider.OIDC == nil {
		return nil, fmt.Errorf("AuthenticationClass %q does not define an OIDC provider; the Spark history server supports only OIDC authentication", auth.AuthenticationClass)
	}
	return provider.OIDC, nil
}

// registerOIDCSidecar builds the oauth2-proxy sidecar provider from the resolved OIDC
// provider and registers it on the role group's sidecar manager. cookieSeed must be stable
// per CR (its UID) so the derived session cookie secret does not churn pods.
func registerOIDCSidecar(manager *sidecar.SidecarManager, provider *authv1alpha1.OIDCProvider, oidc *shsv1alpha1.OidcSpec, cookieSeed string) {
	proxy := sidecar.NewOAuth2ProxySidecarProvider(
		provider,
		oidc.ClientCredentialsSecret,
		HttpPort,
		cookieSeed,
		sidecar.WithOAuth2ProxyPort(OidcPort),
		sidecar.WithOAuth2ProxyExtraScopes(oidc.ExtraScopes...),
	)
	// The image is set explicitly so the framework's product-image propagation
	// (SidecarManager.SetProductImage fills empty images) cannot replace it with the
	// Spark image.
	manager.Register(proxy, &sidecar.SidecarConfig{
		Enabled: true,
		Image:   sidecar.DefaultOAuth2ProxyImage,
	})
}
