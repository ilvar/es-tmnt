package config

import "regexp"

type Config struct {
	Ports            Ports          `yaml:"ports"`
	UpstreamURL      string         `yaml:"upstream_url"`
	Mode             string         `yaml:"mode"`
	Verbose          bool           `yaml:"verbose"`
	TenantRegex      TenantRegex    `yaml:"tenant_regex"`
	SharedIndex      SharedIndex    `yaml:"shared_index"`
	IndexPerTenant   IndexPerTenant `yaml:"index_per_tenant"`
	PassthroughPaths []string       `yaml:"passthrough_paths"`
	Auth             Auth           `yaml:"auth"`
}

type Ports struct {
	HTTP  int `yaml:"http"`
	Admin int `yaml:"admin"`
}

type TenantRegex struct {
	Pattern  string         `yaml:"pattern"`
	Compiled *regexp.Regexp `yaml:"-"`
}

type SharedIndex struct {
	Name          string           `yaml:"name"`
	AliasTemplate string           `yaml:"alias_template"`
	TenantField   string           `yaml:"tenant_field"`
	DenyPatterns  []string         `yaml:"deny_patterns"`
	DenyCompiled  []*regexp.Regexp `yaml:"-"`
}

type IndexPerTenant struct {
	IndexTemplate string `yaml:"index_template"`
}

type Auth struct {
	Required bool   `yaml:"required"`
	Header   string `yaml:"header"`
}

func Default() Config {
	return Config{
		Ports: Ports{
			HTTP:  8080,
			Admin: 8081,
		},
		UpstreamURL: "http://localhost:9200",
		Mode:        "shared",
		Verbose:     false,
		TenantRegex: TenantRegex{
			Pattern: `^(?P<prefix>[^-]+)-(?P<tenant>[^-]+)(?P<postfix>.*)$`,
		},
		SharedIndex: SharedIndex{
			Name:          "{{.index}}",
			AliasTemplate: "alias-{{.index}}-{{.tenant}}",
			TenantField:   "tenant_id",
		},
		IndexPerTenant: IndexPerTenant{
			IndexTemplate: "shared-index",
		},
		Auth: Auth{
			Required: false,
			Header:   "Authorization",
		},
	}
}
