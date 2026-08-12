// Package configs embeds the built-in data files shipped with AgentCell.
package configs

import _ "embed"

// ProvidersYAML is the built-in model-provider preset table; user overrides
// in /etc/agentcell/providers.d/*.yaml (or the operator's mounted config)
// take precedence at load time.
//
//go:embed providers.yaml
var ProvidersYAML []byte

// RunnersYAML is the built-in agent-CLI preset table. Runners are data for
// the same reason providers are: a CLI's flags are the fastest-moving thing
// in this system, and an upstream rename should be a config fix rather than
// a release.
//
//go:embed runners.yaml
var RunnersYAML []byte
