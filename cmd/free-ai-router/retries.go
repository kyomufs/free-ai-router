package main

import (
	"free-ai-router/internal/capability"
	"free-ai-router/internal/config"
	"free-ai-router/internal/provider"
)

// retryPolicies builds the retry mechanisms from the configuration.
//
// WHO GETS ONE. Only a provider that asked for it, by naming the mechanism in its
// settings:
//
//	providers:
//	  stub:
//	    type: stub
//	    settings:
//	      retry:
//	        attempts: 3
//	        delay_ms: 500
//
// There is no global switch, and that is deliberate. A repeat is only ever
// meaningful for a provider whose refusals say "not now" — the provider is the only
// party that can tell, and it says so by declaring the mechanism. Turning repeats on
// for everything and letting the ones that do not want them ignore them would mean
// spending the client's time to discover what the configuration could have stated.
//
// The mechanism carries a policy and no state: the number of attempts is per
// request, so it lives in the loop that makes them.
func retryPolicies(cfg *config.Config, providers *provider.Registry) map[string]*capability.Retry {
	out := make(map[string]*capability.Retry)

	for _, p := range providers.All() {
		id := p.ID()
		pc, ok := cfg.Providers[string(id)]
		if !ok {
			continue
		}
		settings, ok := pc.Settings["retry"].(map[string]any)
		if !ok {
			continue
		}

		built, err := capability.NewRetry(settings)
		if err != nil {
			// The settings shape was already checked when the provider was built,
			// so a failure here is a mechanism that cannot be assembled at all.
			// Falling back to no repeats is the safe direction: a repeat that does
			// not happen costs one refusal, and one that happens wrongly costs the
			// client's time on every request.
			continue
		}
		policy, ok := built.(*capability.Retry)
		if !ok {
			continue
		}
		out[string(id)] = policy
	}

	if len(out) == 0 {
		return nil
	}
	return out
}
