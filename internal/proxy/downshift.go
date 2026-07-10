package proxy

import "encoding/json"

// haikuMaxTokens is the output ceiling of the Haiku tier — larger requested
// values would 400 after a downshift.
const haikuMaxTokens = 64000

// downshiftBody rewrites a standalone housekeeping request to run on a
// cheaper Claude model. Only fields the target can't accept are touched:
// model, thinking (the Haiku tier has no adaptive thinking), output_config's
// effort (unsupported on Haiku), and max_tokens above the target's ceiling.
// System, tools, and message content are NEVER modified — and because prompt
// caches are per-model, this must only ever run on standalone requests, never
// on conversation turns.
func downshiftBody(body []byte, model string) ([]byte, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return nil, false
	}
	mj, err := json.Marshal(model)
	if err != nil {
		return nil, false
	}
	m["model"] = mj
	delete(m, "thinking")
	if raw, ok := m["output_config"]; ok {
		var oc map[string]json.RawMessage
		if json.Unmarshal(raw, &oc) == nil {
			delete(oc, "effort")
			if len(oc) == 0 {
				delete(m, "output_config")
			} else if b, err := json.Marshal(oc); err == nil {
				m["output_config"] = b
			}
		}
	}
	if raw, ok := m["max_tokens"]; ok {
		var mt int
		if json.Unmarshal(raw, &mt) == nil && mt > haikuMaxTokens {
			b, _ := json.Marshal(haikuMaxTokens)
			m["max_tokens"] = b
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return nil, false
	}
	return out, true
}
