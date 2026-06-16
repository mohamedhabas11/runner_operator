package startup

import (
	"context"
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// crdInfo holds the expected CRD identity for version validation.
type crdInfo struct {
	name    string // full CRD name (e.g. "runners.runner-operator.io")
	version string // expected served version (e.g. "v1alpha1")
}

var managedCRDs = []crdInfo{
	{name: "runners.runner-operator.io", version: "v1alpha1"},
	{name: "workflows.runner-operator.io", version: "v1alpha1"},
	{name: "eventtriggers.runner-operator.io", version: "v1alpha1"},
}

// CheckCRDs validates that the expected CRDs are installed with the correct
// served versions. Missing or mismatched CRDs are logged as warnings.
func CheckCRDs(ctx context.Context, cl client.Client) error {
	logger := log.FromContext(ctx)

	list := &apiextensionsv1.CustomResourceDefinitionList{}
	if err := cl.List(ctx, list); err != nil {
		return fmt.Errorf("list CRDs: %w", err)
	}

	crdByName := make(map[string]apiextensionsv1.CustomResourceDefinition, len(list.Items))
	for _, crd := range list.Items {
		crdByName[crd.Name] = crd
	}

	for _, expected := range managedCRDs {
		crd, ok := crdByName[expected.name]
		if !ok {
			logger.WithValues("crd", expected.name).
				Info("CRD not found - is the operator installed correctly?")
			continue
		}

		versionFound := false
		for _, v := range crd.Spec.Versions {
			if v.Name == expected.version {
				versionFound = true
				if v.Served {
					break
				}
				logger.WithValues("crd", expected.name, "version", expected.version).
					Info("Expected CRD version is not served - upgrade the CRDs with 'kubectl apply -f <crd-file>'")
			}
		}
		if !versionFound {
			logger.WithValues("crd", expected.name, "expected", expected.version, "available", crdVersions(crd)).
				Info("CRD version mismatch - upgrade the CRDs with 'kubectl apply -f <crd-file>'")
		}
	}

	return nil
}

func crdVersions(crd apiextensionsv1.CustomResourceDefinition) []string {
	var vs []string
	for _, v := range crd.Spec.Versions {
		vs = append(vs, v.Name)
	}
	return vs
}
