// Package rights enumerates the fine-grained rights this service declares to the holistic
// rights standard. Each constant is the Linux group backing one permission in
// permissions/aigentic.json — keep the two in sync. Enforcement uses auth.User.Can, i.e.
// the standard rule: isAdmin || group ∈ groups.
package rights

const (
	// GroupRun backs permissions/aigentic.json → run:execute. Members (and admins) may
	// submit a request to any aigentic processor and receive its output.
	GroupRun = "hp_aigentic_run"

	// GroupAPI backs permissions/aigentic.json → cost:api. Additionally required to invoke
	// the metered Anthropic API — the only fachlich axis worth gating ("spends real money").
	// It also gates the choose router, which may route to the paid API and whose runtime
	// leaf choice cannot be re-gated through the in-process spawn (which bypasses the HTTP
	// guard). hp_aigentic_run alone covers the free engines (ollama, claude-cli).
	GroupAPI = "hp_aigentic_api"
)
