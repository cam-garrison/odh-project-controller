package controllers

import (
	"context"
	_ "embed"
	"reflect"
	"regexp"
	"strings"

	authorino "github.com/kuadrant/authorino/api/v1beta1"
	v1 "k8s.io/api/core/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
)

type reconcileFunc func(ctx context.Context, ns *v1.Namespace) error

func (r *OpenshiftServiceMeshReconciler) reconcileAuthConfig(ctx context.Context, ns *v1.Namespace) error {
	log := r.Log.WithValues("feature", "authorino", "namespace", ns.Name)

	desiredAuthConfig, err := r.createAuthConfig(ns, ns.ObjectMeta.Annotations[AnnotationGatewayExternalHost])
	if err != nil {
		log.Error(err, "Failed creating AuthConfig object")
		return err
	}
	foundAuthConfig := &authorino.AuthConfig{}
	justCreated := false

	err = r.Get(ctx, types.NamespacedName{
		Name:      desiredAuthConfig.Name,
		Namespace: ns.Name,
	}, foundAuthConfig)
	if err != nil {
		if apierrs.IsNotFound(err) {
			log.Info("Creating Authorino AuthConfig")
			// Create the AuthConfig in the Openshift cluster
			err = r.Create(ctx, desiredAuthConfig)
			if err != nil && !apierrs.IsAlreadyExists(err) {
				log.Error(err, "Unable to create Authorino AuthConfig")
				return err
			}
			justCreated = true
		} else {
			log.Error(err, "Unable to fetch the AuthConfig")
			return err
		}
	}

	// Reconcile the Authorino AuthConfig if it has been manually modified
	if !justCreated && !CompareAuthConfigs(*desiredAuthConfig, *foundAuthConfig) {
		log.Info("Reconciling Authorino AuthConfig")
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if err := r.Get(ctx, types.NamespacedName{
				Name:      desiredAuthConfig.Name,
				Namespace: ns.Name,
			}, foundAuthConfig); err != nil {
				return err
			}
			foundAuthConfig.Spec = desiredAuthConfig.Spec
			foundAuthConfig.ObjectMeta.Labels = desiredAuthConfig.ObjectMeta.Labels
			return r.Update(ctx, foundAuthConfig)
		})
		if err != nil {
			log.Error(err, "Unable to reconcile the Authorino AuthConfig")
			return err
		}
	}

	return nil
}

//go:embed template/authconfig.yaml
var authConfigTemplate []byte

func (r *OpenshiftServiceMeshReconciler) createAuthConfig(ns *v1.Namespace, hosts ...string) (*authorino.AuthConfig, error) {
	authHosts := make([]string, len(hosts))
	for i := range hosts {
		authHosts[i] = ExtractHostName(hosts[i])
	}

	authConfig := &authorino.AuthConfig{}
	if err := ConvertToStructuredResource(authConfigTemplate, authConfig); err != nil {
		return authConfig, err
	}

	authConfig.SetName(ns.Name + "-protection")
	authConfig.SetNamespace(ns.Name)
	authConfig.Spec.Hosts = authHosts

	// Reflects oauth-proxy SAR settings
	authConfig.Spec.Authorization[0].KubernetesAuthz.ResourceAttributes = &authorino.Authorization_KubernetesAuthz_ResourceAttributes{
		Namespace: authorino.StaticOrDynamicValue{Value: ns.Name},
		Group:     authorino.StaticOrDynamicValue{Value: "kubeflow.org"},
		Resource:  authorino.StaticOrDynamicValue{Value: "notebooks"},
		Verb:      authorino.StaticOrDynamicValue{Value: "get"},
	}

	return authConfig, nil
}

func CompareAuthConfigs(a1, a2 authorino.AuthConfig) bool {
	return reflect.DeepEqual(a1.ObjectMeta.Labels, a2.ObjectMeta.Labels) &&
		reflect.DeepEqual(a1.Spec, a2.Spec)
}

// ExtractHostName strips given URL in string from http(s):// prefix and subsequent path.
// This is useful when getting value from http headers (such as origin), as Authorino needs host only.
// If given string does not start with http(s) prefix it will be returned as is.
func ExtractHostName(s string) string {
	r := regexp.MustCompile(`^(https?://)`)
	withoutProtocol := r.ReplaceAllString(s, "")
	if s == withoutProtocol {
		return s
	}
	index := strings.Index(withoutProtocol, "/")
	if index == -1 {
		return withoutProtocol
	}
	return withoutProtocol[:index]
}
