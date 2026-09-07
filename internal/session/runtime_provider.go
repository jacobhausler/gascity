package session

// RuntimeProviderMetadataKey is the session-bead metadata key recording the
// runtime backend a session was created on (see [Info.RuntimeProvider]).
//
// It is stamped once, at creation, from the agent's (or its rig's)
// runtime_provider, and is never rewritten: it describes where the box
// physically is, not what configuration currently prefers. An absent key means
// no lane-scoped selection was in effect, which is both the pre-existing
// behavior and the behavior of every session created before this field.
const RuntimeProviderMetadataKey = "runtime_provider"
