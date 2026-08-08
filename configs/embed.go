// Package configs embeds the built-in data files shipped with AgentCell.
package configs

import _ "embed"

// ProvidersYAML is the built-in model-provider preset table; user overrides
// in /etc/agentcell/providers.d/*.yaml (or the operator's mounted config)
// take precedence at load time.
//
//go:embed providers.yaml
var ProvidersYAML []byte
