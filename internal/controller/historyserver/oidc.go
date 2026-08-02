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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	shsv1alpha1 "github.com/zncdatadev/spark-k8s-operator/api/v1alpha1"
)

// oidcCookieSecretSuffix names the per-cluster Secret carrying the oauth2-proxy session
// cookie secret: "<cluster>-oauth2-cookie".
const oidcCookieSecretSuffix = "-oauth2-cookie"

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

// ensureCookieSecret ensures the per-cluster session cookie Secret exists and carries the
// cookie key, returning the reference the oauth2-proxy sidecar mounts. The secret value is
// generated ONCE (a stable random key — regenerating would roll every pod and log every user
// out) and owned by the CR, so it is garbage-collected with the cluster. The framework
// deliberately refuses to derive this from CR fields: anything readable through the API is
// not a secret.
func ensureCookieSecret(ctx context.Context, client ctrlclient.Client, cr *shsv1alpha1.SparkHistoryServer, scheme *runtime.Scheme) (*corev1.SecretKeySelector, error) {
	name := cr.GetName() + oidcCookieSecretSuffix
	ref := &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: name},
		Key:                  sidecar.OIDCCookieSecretKey,
	}

	secret := &corev1.Secret{}
	err := client.Get(ctx, ctrlclient.ObjectKey{Namespace: cr.GetNamespace(), Name: name}, secret)
	if err == nil {
		if _, ok := secret.Data[sidecar.OIDCCookieSecretKey]; ok {
			return ref, nil
		}
		// The Secret exists but lost its key (manual edit): restore it without touching
		// other keys.
		value, genErr := sidecar.GenerateCookieSecret()
		if genErr != nil {
			return nil, genErr
		}
		patched := secret.DeepCopy()
		if patched.Data == nil {
			patched.Data = map[string][]byte{}
		}
		patched.Data[sidecar.OIDCCookieSecretKey] = []byte(value)
		if err := client.Patch(ctx, patched, ctrlclient.MergeFrom(secret)); err != nil {
			return nil, fmt.Errorf("failed to restore the cookie secret key on %q: %w", name, err)
		}
		return ref, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("failed to get cookie secret %q: %w", name, err)
	}

	value, err := sidecar.GenerateCookieSecret()
	if err != nil {
		return nil, err
	}
	secret = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cr.GetNamespace(),
		},
		Data: map[string][]byte{
			sidecar.OIDCCookieSecretKey: []byte(value),
		},
	}
	if err := controllerutil.SetControllerReference(cr, secret, scheme); err != nil {
		return nil, fmt.Errorf("failed to set owner reference on cookie secret %q: %w", name, err)
	}
	if err := client.Create(ctx, secret); err != nil {
		// A concurrent reconcile may have won the race; the next pass reads it.
		if apierrors.IsAlreadyExists(err) {
			return ref, nil
		}
		return nil, fmt.Errorf("failed to create cookie secret %q: %w", name, err)
	}
	return ref, nil
}

// registerOIDCSidecar builds the oauth2-proxy sidecar provider from the resolved OIDC
// provider and registers it on the role group's sidecar manager.
//
// The authorization policy is deliberately WithOAuth2ProxyAllowAllEmails: the CRD offers no
// allowed-domains field yet, and admitting every account the AuthenticationClass's identity
// provider authenticates preserves the operator's long-standing behavior. Tightening this
// per cluster is a CRD follow-up (spec.clusterConfig.authentication.oidc.allowedEmailDomains).
func registerOIDCSidecar(manager *sidecar.SidecarManager, provider *authv1alpha1.OIDCProvider, oidc *shsv1alpha1.OidcSpec, cookieSecretRef *corev1.SecretKeySelector) {
	proxy := sidecar.NewOAuth2ProxySidecarProvider(
		provider,
		oidc.ClientCredentialsSecret,
		HttpPort,
		sidecar.WithOAuth2ProxyPort(OidcPort),
		sidecar.WithOAuth2ProxyExtraScopes(oidc.ExtraScopes...),
		sidecar.WithOAuth2ProxyCookieSecretRef(cookieSecretRef),
		sidecar.WithOAuth2ProxyAllowAllEmails(),
	)
	// No explicit image: the provider owns its upstream image (OwnImageProvider), so the
	// framework's product-image propagation leaves it alone.
	manager.Register(proxy, &sidecar.SidecarConfig{Enabled: true})
}
