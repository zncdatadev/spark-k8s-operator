package controller

import (
	"context"
	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ExtractorParams Params of extract resource function for role group
type ExtractorParams struct {
	instance      client.Object
	ctx           context.Context
	roleGroupName string
	roleGroup     any
	scheme        *runtime.Scheme
}

// ReconcileParams Params of reconcile resource
type ReconcileParams struct {
	instance client.Object
	client.Client
	roleGroups         map[string]any
	ctx                context.Context
	scheme             *runtime.Scheme
	log                logr.Logger
	roleGroupExtractor func(params ExtractorParams) (client.Object, error)
}

// Extract resource from k8s cluster for role group ,and after collect them
func (r *ReconcileParams) extractResources() ([]client.Object, error) {
	var resources []client.Object
	if r.roleGroups != nil {
		for roleGroupName, roleGroup := range r.roleGroups {
			rsc, err := r.roleGroupExtractor(ExtractorParams{
				instance:      r.instance,
				ctx:           r.ctx,
				roleGroupName: roleGroupName,
				roleGroup:     roleGroup,
				scheme:        r.scheme,
			})
			if err != nil {
				return nil, err
			}
			resources = append(resources, rsc)
		}
	}
	return resources, nil
}

// create or update resource
func (r *ReconcileParams) createOrUpdateResource() error {
	resources, err := r.extractResources()
	if err != nil {
		return err
	}

	for _, rsc := range resources {
		if rsc == nil {
			continue
		}

		if errs := CreateOrUpdate(r.ctx, r, rsc); errs != nil {
			r.log.Error(errs, "Failed to create or update Resource", "resource", rsc)
			return errs
		}
	}
	return nil
}

// ResourceRequest Params of fetch resource  from k8s cluster
type ResourceRequest struct {
	Ctx context.Context
	client.Client
	Obj       client.Object
	Namespace string
	Log       logr.Logger
}

func (r *ResourceRequest) fetchResource() error {
	obj := r.Obj
	name := obj.GetName()
	kind := obj.GetObjectKind()
	if err := r.Get(r.Ctx, client.ObjectKey{Namespace: r.Namespace, Name: name}, obj); err != nil {
		opt := []any{"ns", r.Namespace, "name", name, "kind", kind}
		if apierrors.IsNotFound(err) {
			r.Log.Error(err, "Fetch resource NotFound", opt...)
		} else {
			r.Log.Error(err, "Fetch resource occur some unknown err", opt...)
		}
		return err
	}
	return nil
}
