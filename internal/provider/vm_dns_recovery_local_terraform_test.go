package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Exercise Terraform's real taint and state handling against a loopback-only panel.
func TestVmDNSLocalTerraformCreationRecovery(t *testing.T) {
	if _, err := exec.LookPath("terraform"); err != nil {
		t.Skip("Terraform CLI is not installed")
	}
	var mu sync.Mutex
	dns, failDNS, creates, deletes := "192.0.2.53", true, 0, 0
	deleteNoop, deleted := true, false
	var gateway *string
	failStart, starts := true, 0
	vm := map[string]any{"id": 7, "name": "test-vm", "status": "STOPPED", "cpuCores": 2,
		"ram": 4, "diskSize": 20, "diskType": "SSD", "localNetworkId": 42, "imageId": 1,
		"privateIp": "192.0.2.10"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		answer := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": v}) }
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/panel-main/api/v2/vms":
			creates++
			answer(vm)
		case r.Method == http.MethodGet && (r.URL.Path == "/panel-main/api/v2/vms/7" || r.URL.Path == "/panel-main/api/v2/vms/7/status"):
			// Older panels hide ERROR VMs on the ordinary endpoint while status can still see them.
			if deleted || (r.URL.Path == "/panel-main/api/v2/vms/7" && vm["status"] == "ERROR") {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":601,"message":"not found"}]}`))
				return
			}
			answer(vm)
		case r.Method == http.MethodGet && r.URL.Path == "/panel-main/api/v2/vms/7/dns":
			if failDNS {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":627,"message":"unavailable"}]}`))
				return
			}
			answer(map[string]any{"nameserver": dns, "configured": true, "pending": false, "reconcileRequired": false, "requiresGuestConfiguration": false})
		case r.Method == http.MethodPut && r.URL.Path == "/panel-main/api/v2/vms/7/dns":
			var change struct {
				Nameserver string `json:"nameserver"`
			}
			if err := json.NewDecoder(r.Body).Decode(&change); err != nil {
				t.Errorf("decode DNS change: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			changed := change.Nameserver != dns
			if r.URL.Query().Get("dryRun") != "true" {
				dns = change.Nameserver
			}
			answer(map[string]any{"nameserver": change.Nameserver, "changed": changed, "requiresGuestConfiguration": false})
		case r.Method == http.MethodGet && r.URL.Path == "/panel-main/api/v2/vms/7/gateway":
			answer(map[string]any{"interfaceId": 91, "networkId": 42, "gateway": gateway,
				"present": gateway != nil, "pending": false, "reconcileRequired": false,
				"otherGatewayActive": false, "requiresGuestConfiguration": false})
		case r.Method == http.MethodPut && r.URL.Path == "/panel-main/api/v2/vms/7/gateway":
			var change struct {
				Gateway         *string `json:"gateway"`
				ExpectedGateway *string `json:"expectedGateway"`
				Clear           bool    `json:"clear"`
			}
			if err := json.NewDecoder(r.Body).Decode(&change); err != nil {
				t.Errorf("decode gateway: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			next := gateway
			if change.Clear && gateway != nil && change.ExpectedGateway != nil && *gateway == *change.ExpectedGateway {
				next = nil
			}
			if !change.Clear {
				next = change.Gateway
			}
			changed := (gateway == nil) != (next == nil) || (gateway != nil && next != nil && *gateway != *next)
			if r.URL.Query().Get("dryRun") != "true" {
				gateway = next
			}
			answer(map[string]any{"interfaceId": 91, "networkId": 42, "gateway": next, "changed": changed,
				"previousGatewayOnAnotherInterface": false, "requiresGuestConfiguration": false})
		case r.Method == http.MethodPost && r.URL.Path == "/panel-main/api/v2/vms/7/stop":
			vm["status"] = "STOPPED"
			answer(nil)
		case r.Method == http.MethodPost && r.URL.Path == "/panel-main/api/v2/vms/7/start":
			starts++
			if failStart {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":714,"message":"start refused"}]}`))
				return
			}
			vm["status"] = "RUNNING"
			answer(nil)
		case r.Method == http.MethodDelete && r.URL.Path == "/panel-main/api/v2/vms/7":
			deletes++
			if !deleteNoop {
				deleted = true
			}
			answer(nil)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	bin := filepath.Join(dir, "terraform-provider-prodata")
	build := exec.Command("go", "build", "-o", bin, "../..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build provider: %v\n%s", err, output)
	}
	cliConfig := filepath.Join(dir, "terraformrc")
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(cliConfig, fmt.Sprintf("provider_installation {\n  dev_overrides {\n    \"prodata/prodata\" = %q\n  }\n  direct {}\n}\n", dir))
	config := fmt.Sprintf(`terraform {
  required_providers {
    prodata = { source = "prodata/prodata" }
  }
}
provider "prodata" {
  api_base_url = %q
  api_key_id = "fake-key"
  api_secret_key = "fake-secret"
  region = "TEST"
  project_tag = "test"
}
resource "prodata_vm" "test" {
  name = "test-vm"
  image_id = 1
  cpu_cores = 2
  ram = 4
  disk_size = 20
  disk_type = "SSD"
  local_network_id = 42
  password = "fake-password"
  dns = "192.0.2.54"
}
`, server.URL)
	write(filepath.Join(dir, "main.tf"), config)
	run := func(wantError string, args ...string) string {
		t.Helper()
		cmd := exec.Command("terraform", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "TF_CLI_CONFIG_FILE="+cliConfig, "TF_IN_AUTOMATION=1")
		output, err := cmd.CombinedOutput()
		if wantError == "" && err != nil {
			t.Fatalf("terraform %v: %v\n%s", args, err, output)
		}
		if wantError != "" && (err == nil || !strings.Contains(string(output), wantError)) {
			t.Fatalf("terraform %v: want error %q, got %v\n%s", args, wantError, err, output)
		}
		return string(output)
	}
	run("Unable to Confirm VM DNS", "apply", "-auto-approve", "-input=false", "-no-color")
	state := run("", "state", "pull")
	if !strings.Contains(state, `"status": "tainted"`) || !strings.Contains(state, `"dns_confirmation_pending": true`) {
		t.Fatalf("created VM must be tainted and protected in state: %s", state)
	}
	run("VM DNS creation needs recovery", "apply", "-refresh=false", "-auto-approve", "-input=false", "-no-color")
	mu.Lock()
	if creates != 1 || deletes != 0 {
		t.Errorf("unsafe automatic replacement: creates=%d deletes=%d", creates, deletes)
	}
	failDNS = false
	mu.Unlock()
	run("", "untaint", "-no-color", "prodata_vm.test")
	run("", "apply", "-refresh=false", "-auto-approve", "-input=false", "-no-color")
	mu.Lock()
	if dns != "192.0.2.54" || creates != 1 || deletes != 0 {
		t.Errorf("DNS recovery failed: dns=%s creates=%d deletes=%d", dns, creates, deletes)
	}
	mu.Unlock()
	state = run("", "state", "pull")
	if !strings.Contains(state, `"dns_confirmation_pending": false`) {
		t.Fatalf("confirmation remains pending: %s", state)
	}

	// Refresh of a failed VM must not mistake the ordinary endpoint's 601 for deletion.
	mu.Lock()
	vm["status"] = "ERROR"
	mu.Unlock()
	run("", "apply", "-refresh-only", "-auto-approve", "-input=false", "-no-color")
	state = run("", "state", "pull")
	if !strings.Contains(state, `"type": "prodata_vm"`) || !strings.Contains(state, `"status": "ERROR"`) {
		t.Fatalf("failed VM was not retained on refresh: %s", state)
	}

	// A gateway write followed by a refused restart must survive Terraform tainting it.
	mu.Lock()
	vm["status"] = "RUNNING"
	mu.Unlock()
	write(filepath.Join(dir, "main.tf"), config+`
resource "prodata_vm_gateway" "test" {
  vm_id = prodata_vm.test.id
  network_id = 42
  gateway = "192.0.2.1"
}
`)
	run("VM gateway changed but the VM did not restart", "apply", "-refresh=false", "-auto-approve", "-input=false", "-no-color")
	state = run("", "state", "pull")
	if !strings.Contains(state, `"restart_pending": true`) || !strings.Contains(state, `"status": "tainted"`) {
		t.Fatalf("gateway restart recovery must be retained: %s", state)
	}
	run("VM restart needs recovery", "apply", "-refresh=false", "-auto-approve", "-input=false", "-no-color")
	mu.Lock()
	if gateway == nil || *gateway != "192.0.2.1" || deletes != 0 {
		t.Errorf("failed apply removed gateway or VM")
	}
	failStart = false
	mu.Unlock()
	run("", "untaint", "-no-color", "prodata_vm_gateway.test")
	run("", "apply", "-refresh=false", "-auto-approve", "-input=false", "-no-color")
	mu.Lock()
	if vm["status"] != "RUNNING" || starts < 2 {
		t.Errorf("gateway restart recovery did not start the VM")
	}
	mu.Unlock()
	state = run("", "state", "pull")
	if strings.Contains(state, `"restart_pending": true`) {
		t.Fatalf("restart remains pending: %s", state)
	}

	// The same recovery must survive an in-place DNS update, where Terraform does not taint.
	mu.Lock()
	failStart = true
	mu.Unlock()
	write(filepath.Join(dir, "main.tf"), strings.Replace(config, `dns = "192.0.2.54"`, `dns = "192.0.2.55"`, 1)+`
resource "prodata_vm_gateway" "test" {
  vm_id = prodata_vm.test.id
  network_id = 42
  gateway = "192.0.2.1"
}
`)
	run("VM updated but not restarted", "apply", "-refresh=false", "-auto-approve", "-input=false", "-no-color")
	state = run("", "state", "pull")
	if !strings.Contains(state, `"restart_pending": true`) {
		t.Fatalf("DNS restart recovery was lost: %s", state)
	}
	mu.Lock()
	failStart = false
	mu.Unlock()
	run("", "apply", "-refresh=false", "-auto-approve", "-input=false", "-no-color")
	mu.Lock()
	if vm["status"] != "RUNNING" || dns != "192.0.2.55" || creates != 1 || deletes != 0 {
		t.Errorf("DNS restart recovery failed: status=%s dns=%s creates=%d deletes=%d", vm["status"], dns, creates, deletes)
	}
	mu.Unlock()

	// A failed restart after deleting a route must not lose the recovery marker. Restoring
	// the resource with refresh disabled must also restore the route actually removed.
	updatedConfig := strings.Replace(config, `dns = "192.0.2.54"`, `dns = "192.0.2.55"`, 1)
	mu.Lock()
	failStart = true
	mu.Unlock()
	write(filepath.Join(dir, "main.tf"), updatedConfig)
	run("VM gateway changed but the VM did not restart", "apply", "-refresh=false", "-auto-approve", "-input=false", "-no-color")
	state = run("", "state", "pull")
	if !strings.Contains(state, `"restart_pending": true`) {
		t.Fatalf("gateway delete lost recovery state: %s", state)
	}
	run("VM restart needs recovery", "apply", "-refresh=false", "-auto-approve", "-input=false", "-no-color")
	mu.Lock()
	failStart = false
	mu.Unlock()
	write(filepath.Join(dir, "main.tf"), updatedConfig+`
resource "prodata_vm_gateway" "test" {
  vm_id = prodata_vm.test.id
  network_id = 42
  gateway = "192.0.2.1"
}
`)
	run("", "apply", "-refresh=false", "-auto-approve", "-input=false", "-no-color")
	mu.Lock()
	if vm["status"] != "RUNNING" || gateway == nil || *gateway != "192.0.2.1" {
		t.Errorf("gateway delete recovery failed: status=%s gateway=%v", vm["status"], gateway)
	}
	mu.Unlock()
	// A panel that acknowledges deletion while still returning the VM must not erase state.
	run("VM deletion was not confirmed", "destroy", "-auto-approve", "-input=false", "-no-color")
	state = run("", "state", "pull")
	if !strings.Contains(state, `"type": "prodata_vm"`) {
		t.Fatalf("unconfirmed VM deletion lost Terraform state: %s", state)
	}
	mu.Lock()
	deleteNoop = false
	mu.Unlock()
	run("", "destroy", "-auto-approve", "-input=false", "-no-color")
}
