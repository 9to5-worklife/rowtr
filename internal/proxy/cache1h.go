package proxy

import "encoding/json"

// upgradeCacheTTL rewrites a request's ephemeral prompt-cache breakpoints from
// the 5-minute default to Anthropic's 1-hour TTL. It walks the decoded JSON and
// sets `"ttl":"1h"` on every `cache_control: {"type":"ephemeral"}` object that
// has no explicit ttl (or an explicit "5m"). This is breakpoint *metadata* — it
// does not change the cached content, so hits still land; it just keeps the
// prefix warm longer. Returns the original bytes unchanged if there was nothing
// to upgrade, so requests without cache breakpoints forward byte-identical.
func upgradeCacheTTL(body []byte) ([]byte, bool) {
	var v any
	if json.Unmarshal(body, &v) != nil {
		return body, false
	}
	if !setCache1h(v) {
		return body, false
	}
	nb, err := json.Marshal(v)
	if err != nil {
		return body, false
	}
	return nb, true
}

// setCache1h recursively finds cache_control markers and extends their TTL,
// reporting whether it changed anything.
func setCache1h(v any) bool {
	changed := false
	switch t := v.(type) {
	case map[string]any:
		if cc, ok := t["cache_control"].(map[string]any); ok && cc["type"] == "ephemeral" {
			if ttl, has := cc["ttl"]; !has || ttl == "5m" {
				cc["ttl"] = "1h"
				changed = true
			}
		}
		for _, val := range t {
			if setCache1h(val) {
				changed = true
			}
		}
	case []any:
		for _, item := range t {
			if setCache1h(item) {
				changed = true
			}
		}
	}
	return changed
}
