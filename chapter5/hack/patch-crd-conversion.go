package main

import (
	"fmt"
	"os"

	"sigs.k8s.io/yaml"
)

const crdPath = "config/crd/bases/apps.clientgo-learning.io_websites.yaml"

func main() {
	data, err := os.ReadFile(crdPath)
	if err != nil {
		fatal(err)
	}

	var crd map[string]any
	if err := yaml.Unmarshal(data, &crd); err != nil {
		fatal(err)
	}

	spec, ok := crd["spec"].(map[string]any)
	if !ok {
		fatal(fmt.Errorf("CRD spec is missing or malformed"))
	}

	spec["conversion"] = map[string]any{
		"strategy": "Webhook",
		"webhook": map[string]any{
			"conversionReviewVersions": []string{"v1"},
			"clientConfig": map[string]any{
				"service": map[string]any{
					"name":      "website-conversion-webhook",
					"namespace": "clientgo-learning-system",
					"path":      "/convert",
					"port":      443,
				},
				"caBundle": "Cg==",
			},
		},
	}

	out, err := yaml.Marshal(crd)
	if err != nil {
		fatal(err)
	}
	if err := os.WriteFile(crdPath, out, 0o644); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "patch CRD conversion: %v\n", err)
	os.Exit(1)
}
