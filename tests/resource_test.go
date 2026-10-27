//go:build integration

package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func integrationResourceScenario(t *testing.T, dsc, cli, temp, repository string, downloads *atomic.Int64, run func(string, ...string) []byte) {
	t.Helper()
	list := integrationObjects(t, run(dsc, "resource", "list", "DscPkg/resources", "-o", "json"))
	if len(list) != 1 || list["DscPkg/resources"] == nil {
		t.Fatalf("resource manifest was not bootstrapped through PATH: %v", list)
	}
	input := fmt.Sprintf(`{"resource":"DscPkg.Test/One","repositories":[%q]}`, repository)
	assertState := func(operation, input, field, expected string) []byte {
		t.Helper()
		output := run(dsc, "resource", operation, "--resource", "DscPkg/resources", "--input", input, "-o", "json")
		var result map[string]json.RawMessage
		if err := json.Unmarshal(output, &result); err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(result[field], []byte(expected)) {
			t.Fatalf("%s missing %s in %s: %s", operation, expected, field, output)
		}
		if bytes.Contains(result[field], []byte(`"_name"`)) {
			t.Fatalf("%s returned Export-only _name: %s", operation, output)
		}
		if bytes.Contains(result[field], []byte(`"state"`)) {
			t.Fatalf("%s returned removed state property: %s", operation, output)
		}
		return output
	}
	assertState("get", input, "actualState", `"resource":"DscPkg.Test/One"`)
	output := assertState("test", input, "actualState", `"resource":"DscPkg.Test/One"`)
	var compliance struct {
		InDesiredState bool     `json:"inDesiredState"`
		DiffProperties []string `json:"differingProperties"`
	}
	if err := json.Unmarshal(output, &compliance); err != nil || compliance.InDesiredState ||
		len(compliance.DiffProperties) != 1 || compliance.DiffProperties[0] != "version" {
		t.Fatalf("missing resource should not be compliant: %s %v", output, err)
	}
	if output := run(cli, "resource", "export"); len(output) != 0 {
		t.Fatalf("empty inventory: %s", output)
	}
	config := fmt.Sprintf(`$schema: https://aka.ms/dsc/schemas/v3/bundled/config/document.json
resources:
  - name: One
    type: DscPkg/resources
    properties:
      resource: DscPkg.Test/One
      repositories: [%q]
  - name: Two
    type: DscPkg/resources
    properties:
      resource: DscPkg.Test/Two
      repositories: [%q]
`, repository, repository)
	configPath := filepath.Join(temp, "00-resources.yaml")
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	before := downloads.Load()
	assertConfigurationSuccess(t, run(dsc, "config", "set", "--file", configPath, "-o", "json"), 2)
	if downloads.Load()-before != 1 {
		t.Fatalf("shared resources require exactly one archive download; got %d", downloads.Load()-before)
	}
	installed := downloads.Load()
	output = assertState("test", input, "actualState", `"version":"1.0.0"`)
	if err := json.Unmarshal(output, &compliance); err != nil || !compliance.InDesiredState {
		t.Fatalf("installed resource should be compliant: %s %v", output, err)
	}
	// Invalid repository origins prove that compliant operations do not resolve remotely.
	offline := `{"resource":"DscPkg.Test/Two","version":"1.0.0","repositories":["invalid://offline"]}`
	assertState("get", offline, "actualState", `"package":"example/shared"`)
	assertState("test", offline, "actualState", `"version":"1.0.0"`)
	assertState("set", offline, "afterState", `"version":"1.0.0"`)
	run(dsc, "config", "set", "--file", configPath, "-o", "json")
	if downloads.Load() != installed {
		t.Fatal("repeat Set downloaded a package")
	}
	export := run(dsc, "resource", "export", "--resource", "DscPkg/resources", "-o", "json")
	var exported struct {
		Resources []struct {
			Name       string `json:"name"`
			Type       string `json:"type"`
			Properties struct {
				Resource string          `json:"resource"`
				Version  string          `json:"version"`
				Name     json.RawMessage `json:"_name"`
			} `json:"properties"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(export, &exported); err != nil || len(exported.Resources) != 2 {
		t.Fatalf("DSC Export must return two schema-validated instances: %s %v", export, err)
	}
	for _, resource := range exported.Resources {
		if resource.Type != "DscPkg/resources" || resource.Properties.Version != "1.0.0" ||
			resource.Name != resource.Properties.Resource+"@"+resource.Properties.Version || len(resource.Properties.Name) != 0 {
			t.Fatalf("invalid exported resource: %+v", resource)
		}
	}
	if bytes.Contains(export, []byte(`"state"`)) {
		t.Fatalf("Export returned removed state property: %s", export)
	}
	first := run(cli, "resource", "export")
	if !bytes.Equal(first, run(cli, "resource", "export")) || bytes.Count(first, []byte("\n")) != 2 {
		t.Fatalf("Export must produce deterministic JSON Lines: %s", first)
	}
	for _, line := range bytes.Split(bytes.TrimSpace(first), []byte("\n")) {
		var instance struct {
			Name     string `json:"_name"`
			Resource string `json:"resource"`
			Version  string `json:"version"`
		}
		if err := json.Unmarshal(line, &instance); err != nil || instance.Name != instance.Resource+"@"+instance.Version {
			t.Fatalf("Export instance name must be resource@version: %s %v", line, err)
		}
	}
	// Force DSC to validate every exported instance as input, including observed fields.
	assertConfigurationSuccess(t, run(dsc, "config", "test", "--input", string(export), "-o", "json"), 2)
	for _, discoveryMode := range []string{"preDeployment", "duringDeployment"} {
		t.Run("server "+discoveryMode, func(t *testing.T) {
			t.Setenv("DSCPKG_PACKAGES_DIR", filepath.Join(temp, "server-"+discoveryMode))
			integrationServerConfigurations(t, dsc, config, discoveryMode)
		})
	}
}

func assertConfigurationSuccess(t *testing.T, data []byte, count int) {
	t.Helper()
	var result struct {
		HadErrors bool              `json:"hadErrors"`
		Results   []json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(data, &result); err != nil || result.HadErrors || len(result.Results) != count {
		t.Fatalf("configuration failed: %s %v", data, err)
	}
}

func integrationServerConfigurations(t *testing.T, dsc, config, mode string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, dsc, "--ignore-settings-file", "--trace-level", "error", "--progress-format", "none", "server")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stdin.Close()
		if err := command.Wait(); err != nil {
			t.Errorf("DSC server exit: %v\n%s", err, &stderr)
		}
	}()
	encoder, decoder := json.NewEncoder(stdin), json.NewDecoder(stdout)
	id := 0
	request := func(method string, params any) map[string]json.RawMessage {
		t.Helper()
		id++
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		for {
			var response map[string]json.RawMessage
			if err := decoder.Decode(&response); err != nil {
				t.Fatalf("read server response: %v", err)
			}
			if len(response["id"]) == 0 {
				continue
			}
			if string(response["id"]) != fmt.Sprint(id) {
				t.Fatalf("server request %s failed: %s", method, mustIntegrationJSON(t, response))
			}
			return response
		}
	}
	request("initialize", map[string]any{
		"protocolVersion": "2024-11-05", "capabilities": map[string]any{},
		"clientInfo": map[string]string{"name": "dscpkg-integration", "version": "1.0.0"},
	})
	if err := encoder.Encode(map[string]string{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		t.Fatal(err)
	}
	invoke := func(operation, configuration string) {
		t.Helper()
		response := request("tools/call", map[string]any{
			"name": "invoke_dsc_config",
			"arguments": map[string]string{
				"operation": operation, "configuration": configuration,
			},
		})
		if operation == "get" && mode == "preDeployment" {
			if !bytes.Contains(response["error"], []byte("Resource not found")) {
				t.Fatalf("expected stale preDeployment server discovery: %s", mustIntegrationJSON(t, response))
			}
			t.Log("DSC 3.3.0 server retained stale preDeployment discovery between configurations")
			return
		}
		if response["error"] != nil {
			t.Fatalf("server configuration failed: %s", mustIntegrationJSON(t, response))
		}
		var result struct {
			IsError           bool `json:"isError"`
			StructuredContent struct {
				Result json.RawMessage `json:"result"`
			} `json:"structuredContent"`
		}
		if err := json.Unmarshal(response["result"], &result); err != nil || result.IsError || len(result.StructuredContent.Result) == 0 {
			t.Fatalf("server configuration failed: %s %v", mustIntegrationJSON(t, response), err)
		}
		count := 1
		if operation == "set" {
			count = 2
		}
		assertConfigurationSuccess(t, result.StructuredContent.Result, count)
	}
	invoke("set", config)
	invoke("get", fmt.Sprintf(`$schema: https://aka.ms/dsc/schemas/v3/bundled/config/document.json
directives:
  resourceDiscovery: %s
resources:
  - name: NewlyInstalled
    type: DscPkg.Test/Two
`, mode))
	if mode == "duringDeployment" {
		t.Log("DSC 3.3.0 server refreshed resources between configurations with duringDeployment")
	}
}

func mustIntegrationJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}
