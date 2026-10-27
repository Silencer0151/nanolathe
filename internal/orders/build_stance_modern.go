package orders

// Nanolathe Modern policy: DESIGN_UNITS_ORDERS_COB "Modern build-stance retry".
//
// A stock builder's StartBuilding body is usually a single
// `start-script RequestState(0)`, and a start-script that finds all eight
// thread slots taken is skipped without a trace [04 §4.3]. Between two jobs
// the previous StopBuilding's close animation still holds three of them, so a
// movement callback arranged in the same unit visit can take the last one and
// the stance is never raised. Retail's wait has no deadline, so the builder
// keeps its work record forever (issue #171). Modern arms a long deadline
// beside the gate and arranges StartBuilding again whenever it expires; a
// script already opening simply re-requests the state it is entering.

// modernBuildStanceRetryTicks is the interval between StartBuilding
// arrangements while a ground work record waits for its stance. It is longer
// than any stock builder's ordinary close-then-open handshake, so a retry only
// fires for a request the script lost.
const modernBuildStanceRetryTicks uint32 = 450

// BuildStanceWait keeps one retry deadline running while the stance stays
// low. A wake by the script-touched marker alone keeps the pending deadline,
// so frequent unrelated port writes cannot postpone the retry. The deadline is
// released once the stance is ready; every later phase arms its own. The
// policy draws no randomness and touches no resource.
func (*ModernRules) BuildStanceWait(req BuildStanceWaitRequest) {
	n := req.Record
	if n == nil || req.Builder == nil {
		return
	}
	if req.Ready {
		n.Deadline = -1
		return
	}
	switch {
	case req.Satisfied&gateDeadline != 0:
		EmitStartBuilding(req.Builder, n, req.WorkX, req.WorkZ)
		n.Deadline = int32(req.Tick + modernBuildStanceRetryTicks)
	case req.Satisfied&gateBuildStance == 0 || n.Deadline == -1:
		n.Deadline = int32(req.Tick + modernBuildStanceRetryTicks)
	}
	n.DynamicGate |= gateDeadline
}
