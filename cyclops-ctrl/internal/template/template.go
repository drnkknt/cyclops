package template

import (
	cyclopsv1alpha1 "github.com/cyclops-ui/cycops-ctrl/api/v1alpha1"
	"github.com/cyclops-ui/cycops-ctrl/internal/models"
	json "github.com/json-iterator/go"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func HelmTemplate(module cyclopsv1alpha1.Module, moduleTemplate models.Template) (string, error) {
	chart := &chart.Chart{
		Raw:      []*chart.File{},
		Metadata: &chart.Metadata{},
		Lock:     &chart.Lock{},
		Values:   map[string]interface{}{},
		Schema:   []byte{},
		Files:    moduleTemplate.Files,
		Templates: []*chart.File{
			{
				Name: "all.yaml",
				Data: []byte(moduleTemplate.Manifest),
			},
		},
	}

	values := make(chartutil.Values)
	if err := json.Unmarshal(module.Spec.Values.Raw, &values); err != nil {
		return "", err
	}

	top := make(chartutil.Values)
	top["Values"] = values
	top["Release"] = map[string]interface{}{
		"Name":      "",
		"Namespace": "",
	}

	out, err := engine.Render(chart, top)
	if err != nil {
		//fmt.Println(moduleTemplate.Manifest)
		return "", err
	}

	manifest := out["all.yaml"]

	for _, dependency := range moduleTemplate.Dependencies {
		data, err := json.Marshal(values[dependency.Name])
		if err != nil {
			return "", err
		}

		dependencyManifest, err := HelmTemplate(cyclopsv1alpha1.Module{
			Spec: cyclopsv1alpha1.ModuleSpec{
				Values: apiextensionsv1.JSON{
					Raw: data,
				},
			},
		}, *dependency)
		if err != nil {
			return "", err
		}

		manifest += "\n---\n"
		manifest += dependencyManifest
	}

	return manifest, err
}
