package resources

import (
	"context"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// moveConfig is the configuration of vmState with the network changed to vmNetNew.
func moveConfig() vmValues { return vmConfig().with("local_network_id", vmNum(vmNetNew)) }

// stateIn is vmState with the VM in the status the state last saw it in.
func stateIn(status string) vmValues { return vmState().with("status", vmStr(status)) }

// vmNetNewIP is the address the panel of the tests gives in vmNetNew when none is asked for.
const vmNetNewIP = "192.168.20.50"

func (h *updateHarness) wantPanelCalls(want ...string) {
	h.t.Helper()
	wantCalls(h.t, h.panel.calls(), want)
}

func (h *updateHarness) wantStatus(want string) {
	h.t.Helper()
	if got := h.panel.current(); got != want {
		h.t.Errorf("the VM is %s at the end, want %s", got, want)
	}
}

// wantInNetwork asserts the network and the address the panel has the VM in.
func (h *updateHarness) wantInNetwork(id int64, ip string) {
	h.t.Helper()
	network, address, _, _, _, _ := h.panel.vm()
	if network != id || address != ip {
		h.t.Errorf("the panel has the VM in network %d at %q, want network %d at %q", network, address, id, ip)
	}
}

func wantBodies(t *testing.T, got []moveBody, want ...moveBody) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("requests to primary-local-network = %+v, want %+v", got, want)
	}
}

func TestVmUpdate_MovesARunningVM(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")

	res := h.apply(vmState(), moveConfig())

	res.wantOK(t)
	res.wantNetwork(t, vmNetNew, vmNetNewIP)
	h.wantInNetwork(vmNetNew, vmNetNewIP)
	h.wantPanelCalls("check", "stop", "move", "start")
	if got := h.panel.statusWhen("move"); got != "STOPPED" {
		t.Errorf("the VM was %s when it was moved, want STOPPED", got)
	}
	h.wantStatus("RUNNING")
	if got := res.state.Status.ValueString(); got != "RUNNING" {
		t.Errorf("status in the state = %q, want RUNNING", got)
	}
	wantBodies(t, h.panel.moveBodies(),
		moveBody{dryRun: true, networkID: vmNetNew},
		moveBody{networkID: vmNetNew})
}

func TestVmUpdate_MovesARunningVMToTheAddressAsked(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")

	res := h.apply(vmState(), moveConfig().with("private_ip", vmStr("192.168.20.9")))

	res.wantOK(t)
	res.wantNetwork(t, vmNetNew, "192.168.20.9")
	h.wantPanelCalls("check", "stop", "move", "start")
	wantBodies(t, h.panel.moveBodies(),
		moveBody{dryRun: true, networkID: vmNetNew, ip: "192.168.20.9"},
		moveBody{networkID: vmNetNew, ip: "192.168.20.9"})
}

func TestVmUpdate_AStoppedVMIsMovedAndStaysStopped(t *testing.T) {
	h := newUpdateHarness(t, "STOPPED")

	res := h.apply(stateIn("STOPPED"), moveConfig())

	res.wantOK(t)
	res.wantNetwork(t, vmNetNew, vmNetNewIP)
	h.wantPanelCalls("check", "move")
	h.wantStatus("STOPPED")
	if got := res.state.Status.ValueString(); got != "STOPPED" {
		t.Errorf("status in the state = %q, want STOPPED", got)
	}
}

// The plan is made from the state, and the VM may have been started or stopped since: what Update
// does is decided by the status the panel reports, and the state says what is true after it.
func TestVmUpdate_TheStatusOfTheVMDecidesNotTheStatusInTheState(t *testing.T) {
	cases := []struct {
		name      string
		inState   string
		onPanel   string
		wantCalls []string
	}{
		{"stopped since", "RUNNING", "STOPPED", []string{"check", "move"}},
		{"started since", "STOPPED", "RUNNING", []string{"check", "stop", "move", "start"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newUpdateHarness(t, tc.onPanel)

			res := h.apply(stateIn(tc.inState), moveConfig())

			res.wantOK(t)
			res.wantNetwork(t, vmNetNew, vmNetNewIP)
			h.wantPanelCalls(tc.wantCalls...)
			h.wantStatus(tc.onPanel)
			if got := res.state.Status.ValueString(); got != tc.onPanel {
				t.Errorf("status in the state = %q, want %q", got, tc.onPanel)
			}
		})
	}
}

func TestVmUpdate_ResizeAndMoveShareOneStop(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	config := moveConfig().with("cpu_cores", vmNum(4)).with("ram", vmNum(8)).with("disk_size", vmNum(40))

	res := h.apply(vmState(), config)

	res.wantOK(t)
	res.wantNetwork(t, vmNetNew, vmNetNewIP)
	h.wantPanelCalls("check", "stop", "resources", "disk", "move", "start")
	for _, op := range []string{"resources", "disk", "move"} {
		if got := h.panel.statusWhen(op); got != "STOPPED" {
			t.Errorf("the VM was %s at %s, want STOPPED", got, op)
		}
	}
	_, _, _, cpu, ram, disk := h.panel.vm()
	if cpu != 4 || ram != 8 || disk != 40 {
		t.Errorf("the panel has the VM at %d cores, %d GB of RAM and %d GB of disk, want 4, 8 and 40", cpu, ram, disk)
	}
	if res.state.CPUCores.ValueInt64() != 4 || res.state.RAM.ValueInt64() != 8 || res.state.DiskSize.ValueInt64() != 40 {
		t.Errorf("the state has %v cores, %v GB of RAM and %v GB of disk, want 4, 8 and 40",
			res.state.CPUCores, res.state.RAM, res.state.DiskSize)
	}
}

func TestVmUpdate_TheRenameComesAfterTheCheckAndBeforeTheStop(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")

	res := h.apply(vmState(), moveConfig().with("name", vmStr("web-2")))

	res.wantOK(t)
	h.wantPanelCalls("check", "rename", "stop", "move", "start")
	if got := h.panel.statusWhen("rename"); got != "RUNNING" {
		t.Errorf("the VM was %s when it was renamed, want RUNNING: the rename does not need it stopped", got)
	}
	if _, _, name, _, _, _ := h.panel.vm(); name != "web-2" {
		t.Errorf("the panel has the VM named %q, want web-2", name)
	}
	if got := res.state.Name.ValueString(); got != "web-2" {
		t.Errorf("name in the state = %q, want web-2", got)
	}
}

// What Update did before the move existed: none of it asks the panel about a network.
func TestVmUpdate_WithoutAMoveThereIsNoCheck(t *testing.T) {
	cases := []struct {
		name      string
		state     vmValues
		panel     string
		config    vmValues
		wantCalls []string
		wantAfter string
	}{
		{"rename", vmState(), "RUNNING", vmConfig().with("name", vmStr("web-2")),
			[]string{"rename"}, "RUNNING"},
		{"cpu", vmState(), "RUNNING", vmConfig().with("cpu_cores", vmNum(4)),
			[]string{"stop", "resources", "start"}, "RUNNING"},
		{"disk", vmState(), "RUNNING", vmConfig().with("disk_size", vmNum(40)),
			[]string{"stop", "disk", "start"}, "RUNNING"},
		{"all of them", vmState(), "RUNNING",
			vmConfig().with("name", vmStr("web-2")).with("cpu_cores", vmNum(4)).with("ram", vmNum(8)).with("disk_size", vmNum(40)),
			[]string{"rename", "stop", "resources", "disk", "start"}, "RUNNING"},
		{"cpu of a stopped VM", stateIn("STOPPED"), "STOPPED", vmConfig().with("cpu_cores", vmNum(4)),
			[]string{"resources"}, "STOPPED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newUpdateHarness(t, tc.panel)

			res := h.apply(tc.state, tc.config)

			res.wantOK(t)
			h.wantPanelCalls(tc.wantCalls...)
			h.wantStatus(tc.wantAfter)
			h.wantInNetwork(vmNetOld, "192.168.0.5")
			res.wantNetwork(t, vmNetOld, "192.168.0.5")
		})
	}
}

// The disk is asked for what changed and for nothing else: the size alone, the type alone, or both.
func TestVmUpdate_TheDiskRequestHasOnlyWhatChanged(t *testing.T) {
	cases := []struct {
		name   string
		config vmValues
		want   map[string]any
	}{
		{"size", vmConfig().with("disk_size", vmNum(40)), map[string]any{"diskSize": float64(40)}},
		{"type", vmConfig().with("disk_type", vmStr("NVME")), map[string]any{"diskType": "NVME"}},
		{"size and type", vmConfig().with("disk_size", vmNum(40)).with("disk_type", vmStr("NVME")),
			map[string]any{"diskSize": float64(40), "diskType": "NVME"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newUpdateHarness(t, "RUNNING")

			res := h.apply(vmState(), tc.config)

			res.wantOK(t)
			h.wantPanelCalls("stop", "disk", "start")
			if got := h.panel.diskRequests(); !reflect.DeepEqual(got, []map[string]any{tc.want}) {
				t.Errorf("the disk was asked for %v, want %v", got, []map[string]any{tc.want})
			}
		})
	}
}

// Without a move, a step that fails after the stop does not leave the VM stopped; a VM that was
// stopped to begin with is left as it was.
func TestVmUpdate_AFailedStepWithoutAMoveStartsTheVMAgain(t *testing.T) {
	cases := []struct {
		name       string
		panel      string
		state      vmValues
		fail       string
		config     vmValues
		summary    string
		wantCalls  []string
		wantStatus string
	}{
		{"cpu of a running VM", "RUNNING", vmState(), "resources", vmConfig().with("cpu_cores", vmNum(4)),
			"Unable to Update VM Resources", []string{"stop", "resources", "start"}, "STARTING"},
		{"disk of a running VM", "RUNNING", vmState(), "disk", vmConfig().with("disk_size", vmNum(40)),
			"Unable to Update VM Disk", []string{"stop", "disk", "start"}, "STARTING"},
		{"cpu of a stopped VM", "STOPPED", stateIn("STOPPED"), "resources", vmConfig().with("cpu_cores", vmNum(4)),
			"Unable to Update VM Resources", []string{"resources"}, "STOPPED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newUpdateHarness(t, tc.panel)
			h.panel.failCall(tc.fail, answer{http.StatusBadRequest, 700})

			res := h.apply(tc.state, tc.config)

			res.wantFailed(t, tc.summary)
			h.wantPanelCalls(tc.wantCalls...)
			h.wantStatus(tc.wantStatus)
			if started := contains(res.errs, "The VM was started again."); started != (tc.panel == "RUNNING") {
				t.Errorf("errors = %q: the VM was started again = %v, want %v", res.errs, started, tc.panel == "RUNNING")
			}
		})
	}
}

// A VM that cannot be started again after a step failed is said so in the error of the step.
func TestVmUpdate_AFailedStepAndAVMThatDoesNotStartAreBothReportedWithoutAMove(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.failCall("resources", answer{http.StatusBadRequest, 700})
	h.panel.failCalls("start", -1, answer{http.StatusForbidden, 714})

	res := h.apply(vmState(), vmConfig().with("cpu_cores", vmNum(4)))

	res.wantFailed(t, "Unable to Update VM Resources", "The VM could not be started again")
	h.wantPanelCalls("stop", "resources", "start")
	h.wantStatus("STOPPED")
}

// A start that is answered with an error that may pass is repeated: the panel can still be busy with the
// step that failed (a step cut short by the ceiling goes on there), and the start can have been
// carried out although its reply did not come.
func TestVmUpdate_AStartThatMayPassIsRepeatedAfterAFailedStep(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.failCall("resources", answer{http.StatusBadRequest, 700})
	h.panel.failCalls("start", 3, panelRefusal)

	res := h.apply(vmState(), vmConfig().with("cpu_cores", vmNum(4)))

	res.wantFailed(t, "Unable to Update VM Resources", "The VM was started again.")
	h.wantPanelCalls("stop", "resources", "start", "start", "start", "start")
	h.wantStatus("STARTING")
}

func TestVmUpdate_AStartWhoseReplyWasLostIsNotRepeatedAfterAFailedStep(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.failCall("resources", answer{http.StatusBadRequest, 700})
	h.panel.loseNextReply("start")

	res := h.apply(vmState(), vmConfig().with("cpu_cores", vmNum(4)))

	res.wantFailed(t, "Unable to Update VM Resources", "The VM was started again.")
	h.wantPanelCalls("stop", "resources", "start")
	h.wantStatus("STARTING")
}

// The VM was seen STOPPED before the start, so a VM that reads RUNNING after a start whose answer was
// lost is up, and is taken for up at once: the doubt of startAgain is for a stop.
func TestVmUpdate_AStartWhoseReplyWasLostLeavesAVMThatReadsRunningAtOnce(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.failCall("resources", answer{http.StatusBadRequest, 700})
	h.panel.setSteps(nil, []string{"RUNNING"})
	h.panel.loseNextReply("start")

	res := h.apply(vmState(), vmConfig().with("cpu_cores", vmNum(4)))

	res.wantFailed(t, "Unable to Update VM Resources", "The VM was started again.")
	h.wantPanelCalls("stop", "resources", "start")
	h.wantStatus("RUNNING")
	if got := h.panel.statusReads(); got >= vmRestartMaxRefusals {
		t.Errorf("the VM was read %d times, want fewer than %d: a VM that was seen stopped and reads RUNNING after its start is up", got, vmRestartMaxRefusals)
	}
}

func TestVmUpdate_AStartThatKeepsFailingAfterAFailedStepIsGivenUp(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.failCall("resources", answer{http.StatusBadRequest, 700})
	h.panel.failCalls("start", -1, panelRefusal)

	res := h.apply(vmState(), vmConfig().with("cpu_cores", vmNum(4)))

	res.wantFailed(t, "Unable to Update VM Resources", "The VM could not be started again", "was not started again")
	// The first start, then as many as a start is repeated.
	h.wantPanelCalls(slices.Concat([]string{"stop", "resources"}, callsOf("start", 1+vmRestartMaxRefusals))...)
	h.wantStatus("STOPPED")
}

// The start after the work is repeated in the same way, and a VM that comes up after all is no warning.
func TestVmUpdate_AStartThatMayPassIsRepeatedAfterTheWork(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.failCalls("start", 2, panelRefusal)

	res := h.apply(vmState(), vmConfig().with("cpu_cores", vmNum(4)))

	res.wantOK(t)
	h.wantPanelCalls("stop", "resources", "start", "start", "start")
	h.wantStatus("RUNNING")
}

func TestVmUpdate_AStartThatKeepsFailingAfterTheWorkIsAWarning(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.failCalls("start", -1, panelRefusal)

	res := h.apply(vmState(), vmConfig().with("cpu_cores", vmNum(4)))

	if res.failed() {
		t.Fatalf("Update failed: %q: the work was done, and what is left is a warning", res.errs)
	}
	if len(res.warnings) != 1 || !contains(res.warnings, "VM updated but not restarted") || !contains(res.warnings, "was not started again") {
		t.Errorf("warnings = %q, want the one that the VM was not restarted and says why", res.warnings)
	}
	h.wantPanelCalls(slices.Concat([]string{"stop", "resources"}, callsOf("start", 1+vmRestartMaxRefusals))...)
	h.wantStatus("STOPPED")
}

func TestVmUpdate_ADiskThatShrinksIsRefusedBeforeAnythingIsDone(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")

	res := h.apply(vmState(), moveConfig().with("name", vmStr("web-2")).with("disk_size", vmNum(10)))

	res.wantFailed(t, "Invalid Disk Size")
	h.wantPanelCalls()
	h.wantStatus("RUNNING")
}

// A panel record of the network that is off while the interface is in the new network already: the
// record is put right and the VM is not stopped for it.
func TestVmUpdate_ARecordThatIsOffIsPutRightWithoutStoppingTheVM(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.setNetworkRecord(vmNetOld, vmNetNew)
	h.panel.setAddress(vmNetNewIP)
	h.panel.setNoCloudInit()

	res := h.apply(vmState(), moveConfig())

	// Nothing was moved, so there is nothing to tell the guest.
	res.wantOK(t)
	res.wantNetwork(t, vmNetNew, vmNetNewIP)
	h.wantPanelCalls("check", "move")
	h.wantStatus("RUNNING")
}

func TestVmUpdate_ARecordThatIsOffIsPutRightAndTheResizeIsStillMade(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.setNetworkRecord(vmNetOld, vmNetNew)
	h.panel.setAddress(vmNetNewIP)

	res := h.apply(vmState(), moveConfig().with("cpu_cores", vmNum(4)))

	res.wantOK(t)
	res.wantNetwork(t, vmNetNew, vmNetNewIP)
	h.wantPanelCalls("check", "move", "stop", "resources", "start")
	if got := res.state.CPUCores.ValueInt64(); got != 4 {
		t.Errorf("cpu_cores in the state = %d, want 4: the resize has to be made too", got)
	}
	h.wantStatus("RUNNING")
}

// A refused check changes nothing: no rename, no stop, and the state stays as it was.
func TestVmUpdate_ARefusedCheckLeavesTheVMAlone(t *testing.T) {
	cases := []struct {
		name    string
		reply   moveReply
		summary string
		detail  string
	}{
		{"no such VM", moveFails(404, 633), "The virtual machine is not found", "region, project_tag"},
		{"member of a group", moveFails(409, 787), "This virtual machine cannot be moved to another network", "refused"},
		{"not an ordinary VM", moveFails(409, 960), "This virtual machine cannot be moved to another network", "refused"},
		{"already in the network", moveFails(409, 955), "The virtual machine already has an interface in the network", "network 20"},
		{"address taken", moveFails(409, 956), "The address is taken", "private_ip"},
		{"transitional status", moveFails(409, 959), "The virtual machine is in a transitional status", "apply again"},
		{"not stopped", moveFails(409, 963), "The virtual machine is not stopped", "apply again"},
		{"backend of a load balancer", moveFails(409, 964), "The virtual machine is a backend of a load balancer", "load balancer"},
		{"gateway cannot go", moveFails(409, 965), "The gateway of the virtual machine cannot be moved", "gateway"},
		{"main interface unclear", moveFails(409, 966), "The main network interface of the virtual machine cannot be told from the others", "contact support"},
		{"address inside the network", moveFails(409, 967), "The address cannot be changed inside the network", "replaces the VM"},
		{"security groups", moveFails(409, 968), "The security groups of the virtual machine cannot be moved", "rules"},
		{"earlier move needs the operator", moveFails(409, 969), "An earlier move of the virtual machine needs the operator", "contact support"},
		{"no such route", moveFails(404, 404), "The panel cannot move a virtual machine to another network", "Update the panel"},
		{"no such route, bare 405", moveFails(405, 0), "The panel cannot move a virtual machine to another network", "Update the panel"},
		{"the check fails", moveFails(500, 627), "Unable to move the virtual machine to another network", "refused"},
		{"the check is cut off", moveFails(0, 0), "Unable to move the virtual machine to another network", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newUpdateHarness(t, "RUNNING")
			h.panel.scriptMoves([]moveReply{tc.reply}, nil)

			res := h.apply(vmState(), moveConfig().with("name", vmStr("web-2")).with("cpu_cores", vmNum(4)))

			res.wantFailed(t, tc.summary, tc.detail)
			h.wantPanelCalls("check")
			h.wantStatus("RUNNING")
			h.wantInNetwork(vmNetOld, "192.168.0.5")
			if _, _, name, cpu, _, _ := h.panel.vm(); name != "web-1" || cpu != 2 {
				t.Errorf("the panel has the VM named %q at %d cores, want web-1 at 2: a refused move changes nothing", name, cpu)
			}
		})
	}
}

// The panel cannot tell the main interface from the others when more than one counts as a main one,
// and a gateway on a second interface is how that happens: the error says so, and where to read what to
// do about it, before it sends the user to support.
func TestVmUpdate_AnUnclearMainInterfaceNamesTheLikelyCause(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.scriptMoves([]moveReply{moveFails(409, 966)}, nil)

	res := h.apply(vmState(), moveConfig())

	res.wantFailed(t,
		"The main network interface of the virtual machine cannot be told from the others",
		"another interface of the VM that carries a default gateway",
		"Move the gateway to the main interface",
		`"Destroying the attachment" in the documentation of prodata_local_network_attachment`,
		"If that is not the cause, contact support.")
	h.wantPanelCalls("check")
	h.wantStatus("RUNNING")
}

// A refusal of the move itself, with the VM stopped: it is started again, and the error says so.
func TestVmUpdate_ARefusalOfTheMoveStartsTheVMAgain(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.scriptMoves(nil, []moveReply{moveFails(409, 956)})

	res := h.apply(vmState(), moveConfig().with("private_ip", vmStr("192.168.20.9")))

	res.wantFailed(t, "The address is taken", "The VM was started again.")
	h.wantPanelCalls("check", "stop", "move", "start")
	h.wantStatus("RUNNING")
	h.wantInNetwork(vmNetOld, "192.168.0.5")
}

// A step before the move that fails ends the chain: the move is not made, the VM is started again
// once, by the cycle, and not by the step.
func TestVmUpdate_AFailedStepBeforeTheMoveEndsTheChain(t *testing.T) {
	cases := []struct {
		name      string
		fail      string
		config    vmValues
		summary   string
		wantCalls []string
	}{
		{"cpu and ram", "resources", moveConfig().with("cpu_cores", vmNum(4)),
			"Unable to Update VM Resources", []string{"check", "stop", "resources", "start"}},
		{"disk", "disk", moveConfig().with("disk_size", vmNum(40)),
			"Unable to Update VM Disk", []string{"check", "stop", "disk", "start"}},
		{"disk after cpu", "disk", moveConfig().with("cpu_cores", vmNum(4)).with("disk_size", vmNum(40)),
			"Unable to Update VM Disk", []string{"check", "stop", "resources", "disk", "start"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newUpdateHarness(t, "RUNNING")
			h.panel.failCall(tc.fail, answer{http.StatusBadRequest, 700})

			res := h.apply(vmState(), tc.config)

			res.wantFailed(t, tc.summary, "The VM was started again.")
			h.wantPanelCalls(tc.wantCalls...)
			h.wantStatus("RUNNING")
			h.wantInNetwork(vmNetOld, "192.168.0.5")
		})
	}
}

// A VM that was stopped to begin with stays stopped when a step fails, and nothing starts it.
func TestVmUpdate_AStoppedVMWhoseResizeFailsIsLeftStopped(t *testing.T) {
	h := newUpdateHarness(t, "STOPPED")
	h.panel.failCall("resources", answer{http.StatusBadRequest, 700})

	res := h.apply(stateIn("STOPPED"), moveConfig().with("cpu_cores", vmNum(4)))

	res.wantFailed(t, "Unable to Update VM Resources")
	h.wantPanelCalls("check", "resources")
	h.wantStatus("STOPPED")
}

func TestVmUpdate_AStoppedVMThatIsRefusedTheMoveIsLeftStopped(t *testing.T) {
	h := newUpdateHarness(t, "STOPPED")
	h.panel.scriptMoves(nil, []moveReply{moveFails(409, 956)})

	res := h.apply(stateIn("STOPPED"), moveConfig().with("private_ip", vmStr("192.168.20.9")))

	res.wantFailed(t, "The address is taken")
	h.wantPanelCalls("check", "move")
	h.wantStatus("STOPPED")
}

func TestVmUpdate_AVMThatDoesNotStartAfterTheMoveIsAWarning(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.failCalls("start", -1, answer{http.StatusForbidden, 714})

	res := h.apply(vmState(), moveConfig())

	if res.failed() {
		t.Fatalf("Update failed: %q: the VM was moved, and what is left is a warning", res.errs)
	}
	if len(res.warnings) != 1 || !contains(res.warnings, "VM updated but not restarted") {
		t.Errorf("warnings = %q, want the one that the VM was not restarted", res.warnings)
	}
	res.wantNetwork(t, vmNetNew, vmNetNewIP)
	if !res.wrote {
		t.Errorf("Update did not write the state of a VM that was moved")
	}
	h.wantPanelCalls("check", "stop", "move", "start")
	h.wantStatus("STOPPED")
	if got := res.state.Status.ValueString(); got != "STOPPED" {
		t.Errorf("status in the state = %q, want STOPPED: that is where the VM is", got)
	}
}

func TestVmUpdate_ARefusedMoveAndAVMThatDoesNotStartAreBothReported(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.scriptMoves(nil, []moveReply{moveFails(409, 956)})
	h.panel.failCalls("start", -1, answer{http.StatusForbidden, 714})

	res := h.apply(vmState(), moveConfig().with("private_ip", vmStr("192.168.20.9")))

	res.wantFailed(t, "The address is taken", "The VM could not be started again")
	h.wantPanelCalls("check", "stop", "move", "start")
}

// timeouts.update that a plan would not have let through still bounds the update: the VM is not
// stopped when there is no time to move it.
func TestVmUpdate_TooLittleTimeLeavesTheVMRunning(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	planned := h.planner.plan(vmState(), moveConfig()).planned.
		with("timeouts", vmTimeouts(t, h.planner, vmNullS, vmStr("10m")))

	res := h.update(vmState(), planned)

	res.wantFailed(t, "Not enough time to move the virtual machine", "was not stopped")
	h.wantPanelCalls("check")
	h.wantStatus("RUNNING")
}

func TestVmUpdate_AStopThatTakesTheTimeLeavesTheMoveUndone(t *testing.T) {
	// At the stop there is a little more than the move needs; the VM takes longer than that little
	// to read STOPPED. It is started again at once, and the move is not made.
	h := newUpdateHarness(t, "RUNNING")
	h.panel.setSteps(slices.Concat(slices.Repeat([]string{"STOPPING"}, 1000), []string{"STOPPED"}), nil)
	update := (vmStartReserve + vmHookMinBudget + 500*time.Millisecond).String()
	planned := h.planner.plan(vmState(), moveConfig()).planned.
		with("timeouts", vmTimeouts(t, h.planner, vmNullS, vmStr(update)))

	res := h.update(vmState(), planned)

	res.wantFailed(t, "Not enough time to move the virtual machine", "was stopped for the move")
	h.wantPanelCalls("check", "stop", "start")
	h.wantStatus("RUNNING")
}

func TestVmUpdate_AVMThatSomebodyStartedIsNotMoved(t *testing.T) {
	// The stop is refused, the VM is then seen stopped and running again before the cycle gets to
	// work on it: somebody started it. The work is not done on a VM that runs.
	h := newUpdateHarness(t, "RUNNING")
	h.panel.refuseCalls("stop", 1)
	h.panel.setScript("RUNNING", "RUNNING", "STOPPED", "RUNNING")

	res := h.apply(vmState(), moveConfig())

	res.wantFailed(t, "The virtual machine was started while it was being moved", "Apply again")
	h.wantPanelCalls("check", "stop")
	h.wantInNetwork(vmNetOld, "192.168.0.5")
}

func TestVmUpdate_AVMInErrorIsNotStopped(t *testing.T) {
	h := newUpdateHarness(t, "ERROR")

	res := h.apply(vmState(), moveConfig())

	res.wantFailed(t, "Unable to Stop VM for update", "ERROR")
	h.wantPanelCalls("check")
}

func TestVmUpdate_A627AfterAMoveThatWentThroughIsASuccess(t *testing.T) {
	// The panel answers 627 after moves that went through as well as after those that did not: the
	// VM's network says which.
	h := newUpdateHarness(t, "RUNNING")
	h.panel.scriptMoves(nil, []moveReply{{fail: &answer{http.StatusInternalServerError, 627}, applyFirst: true}})
	h.panel.setNoCloudInit()

	res := h.apply(vmState(), moveConfig())

	if res.failed() {
		t.Fatalf("Update failed: %q: the VM is in the new network", res.errs)
	}
	if len(res.warnings) != 1 || !contains(res.warnings, "The guest of the virtual machine is not configured") {
		t.Errorf("warnings = %q, want the one that the guest is not configured", res.warnings)
	}
	res.wantNetwork(t, vmNetNew, vmNetNewIP)
	// The second move is the same request asked again, to learn what the first did.
	h.wantPanelCalls("check", "stop", "move", "move", "start")
	h.wantStatus("RUNNING")
}

// The warning after a 627 comes from what the panel says of the VM when it is asked again, and not
// from the forecast of the check, which was made before the VM was stopped: a VM with an earlier move
// that was never settled has no forecast, and the address forecast may be taken by another VM in the
// minutes the VM is stopped.
func TestVmUpdate_A627AfterAMoveWarnsOfTheGuestTheVMHas(t *testing.T) {
	cases := []struct {
		name        string
		noCloudInit bool
		dryRuns     []moveReply
		realIP      string
		wantWarning bool
		wantIP      string
	}{
		{"a guest without cloud-init", true, nil, "", true, vmNetNewIP},
		{"a guest without cloud-init and an earlier move that was never settled", true, []moveReply{{noForecast: true}}, "", true, vmNetNewIP},
		{"a guest with cloud-init", false, nil, "", false, vmNetNewIP},
		{"a guest with cloud-init and an earlier move that was never settled", false, []moveReply{{noForecast: true}}, "", false, vmNetNewIP},
		{"an address that is not the one forecast", true, nil, "192.168.20.77", true, "192.168.20.77"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newUpdateHarness(t, "RUNNING")
			h.panel.scriptMoves(tc.dryRuns, []moveReply{{fail: &answer{http.StatusInternalServerError, 627}, applyFirst: true}})
			if tc.noCloudInit {
				h.panel.setNoCloudInit()
			}
			if tc.realIP != "" {
				h.panel.setRealIP(tc.realIP)
			}

			res := h.apply(vmState(), moveConfig())

			if res.failed() {
				t.Fatalf("Update failed: %q: the VM is in the new network", res.errs)
			}
			res.wantNetwork(t, vmNetNew, tc.wantIP)
			if got := contains(res.warnings, "The guest of the virtual machine is not configured"); got != tc.wantWarning {
				t.Errorf("warnings = %q, want the one that the guest is not configured: %v", res.warnings, tc.wantWarning)
			}
			if tc.wantWarning && !contains(res.warnings, "the address "+tc.wantIP) {
				t.Errorf("warnings = %q, want the address %s that the VM has", res.warnings, tc.wantIP)
			}
			if tc.wantWarning && tc.wantIP != vmNetNewIP && contains(res.warnings, vmNetNewIP) {
				t.Errorf("warnings = %q: they name the address that was forecast, and the VM has %s", res.warnings, tc.wantIP)
			}
			if !tc.wantWarning && len(res.warnings) > 0 {
				t.Errorf("warnings = %q, want none for a guest that cloud-init configures", res.warnings)
			}
			h.wantPanelCalls("check", "stop", "move", "move", "start")
			h.wantStatus("RUNNING")
		})
	}
}

func TestVmUpdate_A627AfterNoMoveIsAnError(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.scriptMoves(nil, []moveReply{moveFails(http.StatusInternalServerError, 627)})

	res := h.apply(vmState(), moveConfig())

	res.wantFailed(t, "Unable to move the virtual machine to another network",
		"the VM is still in network 10", "repeat apply", "The VM was started again.")
	h.wantPanelCalls("check", "stop", "move", "start")
	h.wantStatus("RUNNING")
}

func TestVmUpdate_A627ThatTheVMCannotBeReadAfterIsAnError(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.scriptMoves(nil, []moveReply{moveFails(http.StatusInternalServerError, 627)})
	// The reads of the cycle (RUNNING, STOPPING, STOPPED) succeed; the one after the 627 does not.
	h.panel.failReadsLike(false, false, false, true)

	res := h.apply(vmState(), moveConfig())

	res.wantFailed(t, "whether the move happened failed too", "repeat apply", "The VM was started again.")
	h.wantPanelCalls("check", "stop", "move", "start")
}

// An answer that does not say whether the move happened is followed by the same request.
func TestVmUpdate_AMoveThatGetsNoAnswerIsAskedAgain(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.scriptMoves(nil, []moveReply{moveFails(http.StatusBadGateway, 0)})

	res := h.apply(vmState(), moveConfig())

	res.wantOK(t)
	res.wantNetwork(t, vmNetNew, vmNetNewIP)
	h.wantPanelCalls("check", "stop", "move", "move", "start")
}

func TestVmUpdate_ABusyPanelIsWaitedFor(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.scriptMoves(nil, []moveReply{moveFails(http.StatusServiceUnavailable, 961), moveFails(http.StatusServiceUnavailable, 961)})

	res := h.apply(vmState(), moveConfig())

	res.wantOK(t)
	res.wantNetwork(t, vmNetNew, vmNetNewIP)
	h.wantPanelCalls("check", "stop", "move", "move", "move", "start")
}

// The reply to the move is lost, the request is sent again and the panel says that nothing was
// changed: it was changed by the first one. The guest has to be configured all the same.
func TestVmUpdate_AMoveWhoseReplyWasLostStillWarnsOfTheGuest(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.scriptMoves(nil, []moveReply{{fail: &answer{http.StatusBadGateway, 0}, applyFirst: true}})
	h.panel.setNoCloudInit()

	res := h.apply(vmState(), moveConfig())

	if res.failed() {
		t.Fatalf("Update failed: %q", res.errs)
	}
	if len(res.warnings) != 1 || !contains(res.warnings, "The guest of the virtual machine is not configured") {
		t.Errorf("warnings = %q, want the one that the guest is not configured: the VM was moved", res.warnings)
	}
	res.wantNetwork(t, vmNetNew, vmNetNewIP)
	h.wantPanelCalls("check", "stop", "move", "move", "start")
}

func TestVmUpdate_AMoveWhoseOutcomeStaysUnknownSaysSo(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.resource.moveOptions = []client.MoveOption{client.WithMovePause(func(int) time.Duration { return time.Hour })}
	h.panel.scriptMoves(nil, []moveReply{moveFails(http.StatusBadGateway, 0)})

	res := h.apply(vmState(), moveConfig())

	res.wantFailed(t, "The outcome of the move of the virtual machine is unknown", "repeat apply", "The VM was started again.")
	h.wantPanelCalls("check", "stop", "move", "start")
	h.wantStatus("RUNNING")
}

// An interruption while the move is in flight: its outcome is unknown, and the VM that was stopped
// for it is started all the same.
func TestVmUpdate_AnInterruptionDuringTheMoveStartsTheVMAgain(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.ctx = ctx
	h.panel.onMove = func(r *http.Request) {
		cancel()
		<-r.Context().Done()
	}

	res := h.apply(vmState(), moveConfig())

	res.wantFailed(t, "The outcome of the move of the virtual machine is unknown", "The VM was started again.")
	h.wantPanelCalls("check", "stop", "move", "start")
	h.wantStatus("RUNNING")
}

func TestVmUpdate_ANetworkThatTheReadBackDoesNotConfirmIsAnError(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.setReportedNetwork(vmNetOld)

	res := h.apply(vmState(), moveConfig())

	res.wantFailed(t, "The virtual machine is not in the network it was moved to", "network 20", "network 10")
	h.wantPanelCalls("check", "stop", "move", "start")
}

func TestVmUpdate_AGuestWithoutCloudInitIsToldWhatToDo(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.setNoCloudInit()

	res := h.apply(vmState(), moveConfig())

	if res.failed() {
		t.Fatalf("Update failed: %q", res.errs)
	}
	if len(res.warnings) != 1 {
		t.Fatalf("warnings = %q, want one", res.warnings)
	}
	for _, part := range []string{
		"The guest of the virtual machine is not configured",
		"the address " + vmNetNewIP,
		`the mask of the network "net-b"`,
		"the gateway 192.168.1.1",
		" and the gateway 192.168.1.1 inside the VM yourself",
	} {
		if !contains(res.warnings, part) {
			t.Errorf("the warning is %q, want it to have %q", res.warnings, part)
		}
	}
	res.wantNetwork(t, vmNetNew, vmNetNewIP)
}

// A VM with an unsettled earlier move gets no forecast from the panel: whether it has to move is
// not known, and the stop is what lets the move that follows settle the earlier one and go on.
func TestVmUpdate_AnUnsettledEarlierMoveIsSettledByTheMoveAfterTheStop(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.scriptMoves([]moveReply{{noForecast: true}}, nil)

	res := h.apply(vmState(), moveConfig())

	res.wantOK(t)
	res.wantNetwork(t, vmNetNew, vmNetNewIP)
	h.wantPanelCalls("check", "stop", "move", "start")
	if got := h.panel.statusWhen("move"); got != "STOPPED" {
		t.Errorf("the VM was %s when it was moved, want STOPPED", got)
	}
}

// Terraform does not bring a plan to Update that changes private_ip alone (it replaces the VM), and a
// plan made while the network was not known stops with "Provider produced inconsistent final plan" once
// the network turns out to be the VM's own (the action changes from update to replace): the run with a
// real Terraform is in the notes of the plan. Were Update called so all the same, the panel is asked,
// and says no.
func TestVmUpdate_AnAddressThatChangesAloneIsAskedOfThePanel(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.scriptMoves([]moveReply{moveFails(409, 967)}, nil)
	planned := vmState().with("private_ip", vmStr("192.168.0.9"))

	res := h.update(vmState(), planned)

	res.wantFailed(t, "The address cannot be changed inside the network")
	h.wantPanelCalls("check")
	h.wantStatus("RUNNING")
}

func TestVmUpdate_TheUpdateTimeoutBoundsTheWholeUpdate(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	planned := h.planner.plan(vmState(), vmConfig().with("name", vmStr("web-2"))).planned.
		with("timeouts", vmTimeouts(t, h.planner, vmNullS, vmStr("1ns")))

	res := h.update(vmState(), planned)

	res.wantFailed(t, "Unable to Rename Virtual Machine")
	h.wantPanelCalls()
}

// timeouts.update bounds an update without a move as well, and so does an interruption. What ends a
// step must not leave off a VM that the update stopped: the start has a context of its own, and the
// error says what became of the VM.
func TestVmUpdate_AStepCutShortWithoutAMoveStartsTheVMAgain(t *testing.T) {
	cases := []struct {
		name    string
		hold    string
		config  vmValues
		summary string
	}{
		{"cpu", "resources", vmConfig().with("cpu_cores", vmNum(4)), "Unable to Update VM Resources"},
		{"disk", "disk", vmConfig().with("disk_size", vmNum(40)), "Unable to Update VM Disk"},
	}
	for _, tc := range cases {
		for _, cut := range []string{"the update timeout", "an interruption"} {
			t.Run(tc.name+" cut by "+cut, func(t *testing.T) {
				h := newUpdateHarness(t, "RUNNING")
				h.panel.hangCalls(tc.hold)
				planned := h.planner.plan(vmState(), tc.config).planned
				if cut == "the update timeout" {
					planned = planned.with("timeouts", vmTimeouts(t, h.planner, vmNullS, vmStr("300ms")))
				} else {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					h.ctx = ctx
					h.panel.onHeld = func(string) { cancel() }
				}

				res := h.update(vmState(), planned)

				res.wantFailed(t, tc.summary, "The VM was started again.")
				h.wantPanelCalls("stop", tc.hold, "start")
				h.wantStatus("STARTING")
			})
		}
	}
}

// The wait for the VM to read STOPPED is cut short while the stop is going through: the VM is off
// because of the stop, and nothing else was done to it.
func TestVmUpdate_ACutShortWaitForTheStopStartsTheVMAgain(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.setSteps(slices.Concat(slices.Repeat([]string{"STOPPING"}, 400), []string{"STOPPED"}), nil)
	planned := h.planner.plan(vmState(), vmConfig().with("cpu_cores", vmNum(4))).planned.
		with("timeouts", vmTimeouts(t, h.planner, vmNullS, vmStr("200ms")))

	res := h.update(vmState(), planned)

	res.wantFailed(t, "Unable to Stop VM for update", "was started again, nothing was changed")
	h.wantPanelCalls("stop", "start")
	h.wantStatus("STARTING")
}

// The call of the stop itself is cut short: it may have been carried out although its answer did not
// come, so the VM is looked at, and a VM that is up is left alone.
func TestVmUpdate_ACutShortStopCallLeavesAVMThatIsUpAlone(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.hangCalls("stop")
	planned := h.planner.plan(vmState(), vmConfig().with("cpu_cores", vmNum(4))).planned.
		with("timeouts", vmTimeouts(t, h.planner, vmNullS, vmStr("200ms")))

	res := h.update(vmState(), planned)

	res.wantFailed(t, "Unable to Stop VM for update", "is up, nothing was changed")
	h.wantPanelCalls("stop")
	h.wantStatus("RUNNING")
}

// A stop that fails while the update has time left is the error of the stop and nothing more: the VM is
// as it was, and there is nothing to start.
func TestVmUpdate_AStopThatFailsWithoutAMoveLeavesTheVMAsItWas(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.failCalls("stop", -1, answer{http.StatusForbidden, 714})

	res := h.apply(vmState(), vmConfig().with("cpu_cores", vmNum(4)))

	res.wantFailed(t, "Unable to Stop VM for update")
	if contains(res.errs, "started again") || contains(res.errs, "nothing was changed") {
		t.Errorf("errors = %q: the VM was not touched, and the error should not say what became of it", res.errs)
	}
	h.wantPanelCalls("stop")
	h.wantStatus("RUNNING")
}

// A stop that failed in a way that says nothing final (the panel's 627 comes after stops that were
// carried out) may have switched the VM off, with the update still alive: the update goes no further,
// so the VM is started again, and the error says so.
func TestVmUpdate_AStopWhoseAnswerWasLostWithoutAMoveStartsTheVMAgain(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.loseNextReply("stop")

	res := h.apply(vmState(), vmConfig().with("cpu_cores", vmNum(4)))

	res.wantFailed(t, "Unable to Stop VM for update", "was started again, nothing was changed")
	h.wantPanelCalls("stop", "start")
	h.wantStatus("STARTING")
}

// The same on a panel whose view of the VM lags: it goes on reading RUNNING for a while after the stop
// that it refused, and a look at the VM must not take that for a VM that is up.
func TestVmUpdate_AStopWhoseEffectShowsLateWithoutAMoveStartsTheVMAgain(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.loseNextReplyShowingAfter("stop", 20)

	res := h.apply(vmState(), vmConfig().with("cpu_cores", vmNum(4)))

	res.wantFailed(t, "Unable to Stop VM for update", "was started again, nothing was changed")
	h.wantPanelCalls("stop", "start")
	h.wantStatus("STARTING")
}

// A stop that is refused with an answer that says nothing, and does not show in the VM for as long as a
// lag can last, did not happen: the VM is up, and nothing is done to it.
func TestVmUpdate_AStopRefusedThatNeverShowsWithoutAMoveLeavesAVMThatIsUpAlone(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.failCalls("stop", -1, panelRefusal)

	res := h.apply(vmState(), vmConfig().with("cpu_cores", vmNum(4)))

	res.wantFailed(t, "Unable to Stop VM for update", "is up, nothing was changed")
	h.wantPanelCalls("stop")
	h.wantStatus("RUNNING")
}

// The stop was accepted and the reads that wait for STOPPED fail: the VM is off because of the stop.
func TestVmUpdate_ReadsThatFailAfterTheStopWithoutAMoveStartTheVMAgain(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	// The first read finds the VM running; the three that wait for STOPPED fail.
	h.panel.failReadsLike(false, true, true, true)

	res := h.apply(vmState(), vmConfig().with("cpu_cores", vmNum(4)))

	res.wantFailed(t, "Unable to Stop VM for update", "after 3 consecutive failures", "was started again, nothing was changed")
	h.wantPanelCalls("stop", "start")
	h.wantStatus("STARTING")
}

// The same, and the VM reads RUNNING when it is looked at again: the panel took the stop, but its view
// of the VM may be behind (its mirror can write RUNNING over a stop that is taking hold, and a VM under
// the hypervisor's HA manager takes minutes to go down), so the VM is looked at for as long as a lag can
// last, and is taken for up only if it reads RUNNING all that time.
func TestVmUpdate_ReadsThatFailAfterTheStopLookAtAVMThatReadsRunningAgain(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.setSteps([]string{"RUNNING"}, nil)
	h.panel.failReadsLike(false, true, true, true)

	res := h.apply(vmState(), vmConfig().with("cpu_cores", vmNum(4)))

	res.wantFailed(t, "Unable to Stop VM for update", "after 3 consecutive failures", "is up, nothing was changed")
	h.wantPanelCalls("stop")
	h.wantStatus("RUNNING")
	// The read that finds the VM running, the ones that fail, and the patience of a refused call.
	if got, want := h.panel.statusReads(), 1+vmRestartMaxPollErrs+vmRestartMaxRefusals; got != want {
		t.Errorf("the VM was read %d times, want %d", got, want)
	}
}

// The same, and the VM reads RUNNING once more before it reads STOPPED: it is off because of the stop,
// and a read of RUNNING that is late must not be taken for a VM that is up.
func TestVmUpdate_ReadsThatFailAfterTheStopAndALagStartTheVMAgain(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.setSteps([]string{"RUNNING", "STOPPED"}, nil)
	h.panel.failReadsLike(false, true, true, true)

	res := h.apply(vmState(), vmConfig().with("cpu_cores", vmNum(4)))

	res.wantFailed(t, "Unable to Stop VM for update", "after 3 consecutive failures", "was started again, nothing was changed")
	h.wantPanelCalls("stop", "start")
	h.wantStatus("STARTING")
}

// A stop that fails in any way that says nothing final (a gateway that times out or drops the answer of
// a call the panel carried out) may have switched the VM off: it is started again, as after a 627.
func TestVmUpdate_AStopWhoseAnswerWasLostInAnyWayWithoutAMoveStartsTheVMAgain(t *testing.T) {
	for _, tc := range inconclusiveAnswers {
		t.Run(tc.name, func(t *testing.T) {
			h := newUpdateHarness(t, "RUNNING")
			h.panel.loseNextReplyAs("stop", tc.a)

			res := h.apply(vmState(), vmConfig().with("cpu_cores", vmNum(4)))

			res.wantFailed(t, "Unable to Stop VM for update", "was started again, nothing was changed")
			h.wantPanelCalls("stop", "start")
			h.wantStatus("STARTING")
		})
	}
}

// A stop that is refused in any way that says nothing final, and does not show in the VM for as long as a
// lag can last, did not happen: the VM is up, and nothing is done to it.
func TestVmUpdate_AStopRefusedInAnyWayThatNeverShowsWithoutAMoveLeavesAVMThatIsUpAlone(t *testing.T) {
	for _, tc := range inconclusiveAnswers {
		t.Run(tc.name, func(t *testing.T) {
			h := newUpdateHarness(t, "RUNNING")
			h.panel.failCalls("stop", -1, tc.a)

			res := h.apply(vmState(), vmConfig().with("cpu_cores", vmNum(4)))

			res.wantFailed(t, "Unable to Stop VM for update", "is up, nothing was changed")
			h.wantPanelCalls("stop")
			h.wantStatus("RUNNING")
		})
	}
}

// A refusal for good that is read as the context ends is still the panel's last word: the stop was not
// carried out, the VM is left as it was, and it is not looked at for as long as a lag can last.
func TestVmUpdate_AStopRefusedForGoodAsTheContextEndsLeavesTheVMAlone(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.breakCall("stop", http.StatusForbidden, 714)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.ctx = ctx
	// The context ends as the answer to the stop has been read, before the update acts on it.
	useTransport(t, func(next http.RoundTripper) http.RoundTripper {
		return afterBodyClosed{
			RoundTripper: next,
			match: func(r *http.Request) bool {
				return r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stop")
			},
			hook: cancel,
		}
	})

	res := h.apply(vmState(), vmConfig().with("cpu_cores", vmNum(4)))

	if ctx.Err() == nil {
		t.Fatal("the context did not end: the test does not test what it is for")
	}
	res.wantFailed(t, "Unable to Stop VM for update", "714")
	if contains(res.errs, "started again") || contains(res.errs, "nothing was changed") {
		t.Errorf("errors = %q: the VM was not touched, and the error should not say what became of it", res.errs)
	}
	h.wantPanelCalls("stop")
	if got := h.panel.statusReads(); got != 1 {
		t.Errorf("the VM was read %d times, want once: before the stop", got)
	}
}

// In a cycle with a move, refusals of the stop that outlast the patience while the stop was in fact
// carried out end with the VM started again, where they used to leave it off.
func TestVmUpdate_ARefusedStopThatOutlastsThePatienceStartsTheVMThatItStopped(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.loseNextReplyShowingAfter("stop", vmRestartMaxRefusals-1)

	res := h.apply(vmState(), moveConfig())

	res.wantFailed(t, "Unable to Stop VM for update", "keeps refusing to stop VM 7", "was started again, nothing was changed")
	h.wantPanelCalls(slices.Concat([]string{"check"}, callsOf("stop", vmRestartMaxRefusals), []string{"start"})...)
	h.wantInNetwork(vmNetOld, "192.168.0.5")
	if got := h.panel.current(); got == "STOPPED" {
		t.Errorf("the VM is %s at the end: nothing started it", got)
	}
}

// With the budget running out while a refused stop is waited out, the VM that the stop stopped is
// started again at once, and the error says the work was not done, not that the VM was not stopped.
func TestVmUpdate_ARefusedStopWhenTheBudgetRunsOutStartsTheVMThatItStopped(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.loseNextReplyShowingAfter("stop", vmRestartMaxRefusals-3)
	update := (vmStartReserve + vmHookMinBudget + 20*time.Millisecond).String()
	planned := h.planner.plan(vmState(), moveConfig()).planned.
		with("timeouts", vmTimeouts(t, h.planner, vmNullS, vmStr(update)))

	res := h.update(vmState(), planned)

	res.wantFailed(t, "Not enough time to move the virtual machine", "was stopped for the move", "was started again")
	if contains(res.errs, "was not stopped") {
		t.Errorf("errors = %q: the stop was carried out, and the error says it was not", res.errs)
	}
	h.wantInNetwork(vmNetOld, "192.168.0.5")
	if got := h.panel.current(); got != "RUNNING" {
		t.Errorf("the VM is %s at the end, want RUNNING", got)
	}
}

// The update has done its work when the time of the update is nearly spent: the VM is started, and
// waited for, all the same.
func TestVmUpdate_TheStartAfterTheWorkIsNotCutShortByTheUpdateTimeout(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	h.panel.setSteps(nil, slices.Concat(slices.Repeat([]string{"STARTING"}, 1200), []string{"RUNNING"}))
	planned := h.planner.plan(vmState(), vmConfig().with("cpu_cores", vmNum(4))).planned.
		with("timeouts", vmTimeouts(t, h.planner, vmNullS, vmStr("500ms")))

	res := h.update(vmState(), planned)

	res.wantOK(t)
	h.wantPanelCalls("stop", "resources", "start")
	h.wantStatus("RUNNING")
	if got := res.state.Status.ValueString(); got != "RUNNING" {
		t.Errorf("status in the state = %q, want RUNNING", got)
	}
}

// The read that ends an update works when the context of the update ended during the work: the
// state has to say what was done.
func TestVmUpdate_TheReadBackWorksAfterTheContextEnded(t *testing.T) {
	h := newUpdateHarness(t, "RUNNING")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.ctx = ctx
	// The context ends as soon as the rename is answered, before the read that closes the update.
	useTransport(t, func(next http.RoundTripper) http.RoundTripper {
		return afterBodyClosed{
			RoundTripper: next,
			match:        func(r *http.Request) bool { return r.Method == http.MethodPatch },
			hook:         cancel,
		}
	})

	res := h.apply(vmState(), vmConfig().with("name", vmStr("web-2")))

	if ctx.Err() == nil {
		t.Fatal("the context did not end: the test does not test what it is for")
	}
	res.wantOK(t)
	h.wantPanelCalls("rename")
	if got := res.state.Name.ValueString(); got != "web-2" {
		t.Errorf("name in the state = %q, want web-2", got)
	}
}

func TestReadBackContext(t *testing.T) {
	t.Run("a context that is alive is used as it is", func(t *testing.T) {
		live, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()

		got, release := readBackContext(live)
		defer release()

		if got != live {
			t.Errorf("readBackContext replaced a context that is alive")
		}
	})

	dead := map[string]func() context.Context{
		"cancelled": func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		},
		"out of time": func() context.Context {
			ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
			<-ctx.Done()
			cancel()
			return ctx
		},
	}
	for name, newContext := range dead {
		t.Run("a context that is "+name+" gets a time of its own", func(t *testing.T) {
			got, release := readBackContext(newContext())
			defer release()

			if err := got.Err(); err != nil {
				t.Fatalf("the context for the read is already done: %v", err)
			}
			deadline, ok := got.Deadline()
			if !ok {
				t.Fatalf("the context for the read has no deadline")
			}
			if left := time.Until(deadline); left > vmReadBackTime || left < vmReadBackTime-time.Minute {
				t.Errorf("the read has %v, want about %v", left, vmReadBackTime)
			}
		})
	}
}

func TestWantsMove(t *testing.T) {
	model := func(network types.Int64, ip types.String) *VmResourceModel {
		return &VmResourceModel{LocalNetworkID: network, PrivateIP: ip}
	}
	old := model(types.Int64Value(10), types.StringValue("192.168.0.5"))

	cases := []struct {
		name string
		plan *VmResourceModel
		want bool
	}{
		{"nothing changes", model(types.Int64Value(10), types.StringValue("192.168.0.5")), false},
		{"another network", model(types.Int64Value(20), types.StringUnknown()), true},
		{"another network and an address", model(types.Int64Value(20), types.StringValue("192.168.20.9")), true},
		{"a network that is not known yet", model(types.Int64Unknown(), types.StringUnknown()), true},
		{"the address is not known", model(types.Int64Value(10), types.StringUnknown()), false},
		{"the address is null", model(types.Int64Value(10), types.StringNull()), false},
		{"the address is empty", model(types.Int64Value(10), types.StringValue("")), false},
		{"another address", model(types.Int64Value(10), types.StringValue("192.168.0.9")), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := wantsMove(old, tc.plan); got != tc.want {
				t.Errorf("wantsMove = %v, want %v", got, tc.want)
			}
		})
	}
}
