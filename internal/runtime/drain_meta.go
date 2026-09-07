package runtime

// The drain handshake between the controller and a session is carried entirely
// in provider metadata, so the key names are a contract between two processes
// rather than an implementation detail of either. They are declared here, next
// to the SetMeta/GetMeta/RemoveMeta surface that carries them, so a second
// acknowledger — the API's worker drain-ack route, which lets a session that
// runs off the controller's host acknowledge over the wire — writes the same
// keys the in-session path writes instead of a lookalike set.
const (
	// DrainAckMetaKey is set to "1" once the drain has been acknowledged.
	// The reconciler waits on this to stop a draining session.
	DrainAckMetaKey = "GC_DRAIN_ACK"

	// DrainAckSourceMetaKey records who acknowledged: the agent itself or the
	// reconciler on its behalf. The distinction survives into the stop record,
	// so an operator can tell a cooperative drain from an enforced one.
	DrainAckSourceMetaKey = "GC_DRAIN_ACK_SOURCE"

	// DrainAckSourceAgent is the DrainAckSourceMetaKey value for an
	// acknowledgement the session made for itself.
	DrainAckSourceAgent = "agent"

	// DrainReasonMetaKey and DrainGenerationMetaKey carry the reconciler's
	// pending drain instruction. An agent acknowledgement clears both: the
	// instruction has been consumed, and leaving it behind would let a later
	// read mistake a spent drain for a live one.
	DrainReasonMetaKey     = "GC_DRAIN_REASON"
	DrainGenerationMetaKey = "GC_DRAIN_GENERATION"
)
