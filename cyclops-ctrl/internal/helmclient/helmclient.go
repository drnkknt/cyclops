package helmclient

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/cyclops-ui/cycops-ctrl/internal/mapper"
	json "github.com/json-iterator/go"
	"gopkg.in/yaml.v2"
	helmchart "helm.sh/helm/v3/pkg/chart"

	"github.com/cyclops-ui/cycops-ctrl/internal/models"
	"github.com/cyclops-ui/cycops-ctrl/internal/models/helm"
)

func LoadHelmChart(repo, chart, version string) (*models.Template, error) {
	tgzURL, err := getTarUrl(repo, chart, version)
	if err != nil {
		return nil, err
	}

	tgzData, err := downloadFile(tgzURL)
	if err != nil {
		return nil, err
	}

	extractedFiles, err := unpackTgzInMemory(tgzData)
	if err != nil {
		return nil, err
	}

	return mapHelmChart(chart, extractedFiles)
}

func LoadHelmChartInitialValues(repo, chart, version string) (map[interface{}]interface{}, error) {
	tgzURL, err := getTarUrl(repo, chart, version)
	if err != nil {
		return nil, err
	}

	tgzData, err := downloadFile(tgzURL)
	if err != nil {
		return nil, err
	}

	extractedFiles, err := unpackTgzInMemory(tgzData)
	if err != nil {
		return nil, err
	}

	valuesBytes := []byte{}

	for name, content := range extractedFiles {
		parts := strings.Split(name, "/")

		if len(parts) == 2 && parts[1] == "values.yaml" {
			valuesBytes = content
			break
		}
	}

	var values map[interface{}]interface{}
	if err := yaml.Unmarshal(valuesBytes, &values); err != nil {
		return nil, err
	}

	return mapHelmChartInitialValues(extractedFiles)
}

func LoadDependencies(metadata helmchart.Metadata) ([]*models.Template, error) {
	deps := make([]*models.Template, 0)
	for _, dependency := range metadata.Dependencies {
		dep, err := LoadHelmChart(dependency.Repository, dependency.Name, dependency.Version)
		if err != nil {
			return nil, err
		}

		deps = append(deps, dep)
	}

	return deps, nil
}

func LoadDependenciesInitialValues(metadata helmchart.Metadata) (map[interface{}]interface{}, error) {
	initialValues := make(map[interface{}]interface{})
	for _, dependency := range metadata.Dependencies {
		depInitialValues, err := LoadHelmChartInitialValues(dependency.Repository, dependency.Name, dependency.Version)
		if err != nil {
			return nil, err
		}

		initialValues[dependency.Name] = depInitialValues
	}

	return initialValues, nil
}

func mapHelmChart(chartName string, files map[string][]byte) (*models.Template, error) {
	metadataBytes := []byte{}
	schemaBytes := []byte{}
	manifestParts := make([]string, 0)
	chartFiles := make([]*helmchart.File, 0)
	dependenciesFromChartsDir := make(map[string]map[string][]byte, 0)

	for name, content := range files {
		parts := strings.Split(name, "/")

		if len(parts) == 2 && parts[1] == "Chart.yaml" {
			metadataBytes = content
			continue
		}

		if len(parts) == 2 && parts[1] == "values.schema.json" {
			schemaBytes = content
			continue
		}

		if len(parts) > 2 && parts[1] == "templates" {
			manifestParts = append(manifestParts, string(content))
			continue
		}

		if len(parts) > 3 && parts[1] == "charts" {
			depName := parts[2]
			if _, ok := dependenciesFromChartsDir[depName]; !ok {
				dependenciesFromChartsDir[depName] = make(map[string][]byte)
			}

			dependenciesFromChartsDir[depName][path.Join(parts[2:]...)] = content
			continue
		}

		chartFiles = append(chartFiles, &helmchart.File{
			Name: path.Join(name[1:]),
			Data: content,
		})

	}

	var schema helm.Property
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		return &models.Template{}, err
	}

	var metadata helmchart.Metadata
	if err := yaml.Unmarshal(metadataBytes, &metadata); err != nil {
		return &models.Template{}, err
	}

	// region load dependencies
	dependencies := make([]*models.Template, 0)
	for depName, files := range dependenciesFromChartsDir {
		dep, err := mapHelmChart(depName, files)
		if err != nil {
			return nil, err
		}

		dependencies = append(dependencies, dep)
	}

	dependenciesFromMeta, err := LoadDependencies(metadata)
	if err != nil {
		return &models.Template{}, err
	}

	dependencies = append(dependencies, dependenciesFromMeta...)
	// endregion

	return &models.Template{
		Name:         chartName,
		Manifest:     strings.Join(manifestParts, "---\n"),
		Fields:       mapper.HelmSchemaToFields(schema, dependencies),
		Created:      "",
		Edited:       "",
		Version:      "",
		Files:        chartFiles,
		Dependencies: dependencies,
	}, nil
}

func mapHelmChartInitialValues(files map[string][]byte) (map[interface{}]interface{}, error) {
	metadataBytes := []byte{}
	valuesBytes := []byte{}
	dependenciesFromChartsDir := make(map[string]map[string][]byte, 0)

	for name, content := range files {
		parts := strings.Split(name, "/")

		if len(parts) == 2 && parts[1] == "Chart.yaml" {
			metadataBytes = content
			continue
		}

		if len(parts) == 2 && parts[1] == "values.yaml" {
			valuesBytes = content
			continue
		}

		if len(parts) > 3 && parts[1] == "charts" {
			depName := parts[2]
			if _, ok := dependenciesFromChartsDir[depName]; !ok {
				dependenciesFromChartsDir[depName] = make(map[string][]byte)
			}

			dependenciesFromChartsDir[depName][path.Join(parts[2:]...)] = content
			continue
		}
	}

	var values map[interface{}]interface{}
	if err := yaml.Unmarshal(valuesBytes, &values); err != nil {
		return nil, err
	}

	var metadata helmchart.Metadata
	if err := yaml.Unmarshal(metadataBytes, &metadata); err != nil {
		return nil, err
	}

	// region load dependencies
	for depName, files := range dependenciesFromChartsDir {
		dep, err := mapHelmChartInitialValues(files)
		if err != nil {
			return nil, err
		}

		values[depName] = dep
	}

	dependenciesFromMeta, err := LoadDependenciesInitialValues(metadata)
	if err != nil {
		return nil, err
	}

	for depName, depValues := range dependenciesFromMeta {
		values[depName] = depValues
	}
	// endregion

	return values, nil
}

func getTarUrl(repo, chart, version string) (string, error) {
	indexURL, err := url.JoinPath(repo, "index.yaml")
	if err != nil {
		return "", err
	}

	response, err := http.Get(indexURL)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()

	body, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return "", err
	}

	var data helm.Index
	err = yaml.Unmarshal(body, &data)
	if err != nil {
		return "", err
	}

	if _, ok := data.Entries[chart]; !ok {
		return "", errors.New(fmt.Sprintf("chart %v not found in repo %v", chart, repo))
	}

	for _, entry := range data.Entries[chart] {
		if entry.Version == version {
			if len(entry.URLs) == 0 {
				return "", errors.New(fmt.Sprintf("no URL on version %v of chart %v and repo %v", version, chart, repo))
			}

			return entry.URLs[0], nil
		}
	}

	return "", errors.New(fmt.Sprintf("version %v not found in chart %v and repo %v", version, chart, repo))
}

func downloadFile(url string) ([]byte, error) {
	response, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP request failed with status: %s", response.Status)
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}

	return body, nil
}

func unpackTgzInMemory(tgzData []byte) (map[string][]byte, error) {
	// Create a gzip reader
	gzipReader, err := gzip.NewReader(bytes.NewReader(tgzData))
	if err != nil {
		return nil, err
	}
	defer gzipReader.Close()

	tarReader := tar.NewReader(gzipReader)

	files := make(map[string][]byte)

	for {
		header, err := tarReader.Next()

		if err == io.EOF {
			break
		}

		if err != nil {
			return nil, err
		}

		var fileBuffer bytes.Buffer
		if _, err := io.Copy(&fileBuffer, tarReader); err != nil {
			return nil, err
		}

		files[header.Name] = fileBuffer.Bytes()
	}

	return files, nil
}
