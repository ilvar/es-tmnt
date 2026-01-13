package config

import "time"

type Ports struct {
	HTTP  int `json:"http" yaml:"http"`
	Admin int `json:"admin" yaml:"admin"`
}

type TenantRegex struct {
	Pattern string `json:"pattern" yaml:"pattern"`
}

type SharedIndex struct {
	Name          string `json:"name" yaml:"name"`
	AliasTemplate string `json:"alias_template" yaml:"alias_template"`
	TenantField   string `json:"tenant_field" yaml:"tenant_field"`
}

type IndexPerTenant struct {
	IndexTemplate string `json:"index_template" yaml:"index_template"`
}

type Config struct {
	Ports            Ports          `json:"ports" yaml:"ports"`
	UpstreamURL      string         `json:"upstream_url" yaml:"upstream_url"`
	Mode             string         `json:"mode" yaml:"mode"`
	PassthroughPaths []string       `json:"passthrough_paths" yaml:"passthrough_paths"`
	TenantRegex      TenantRegex    `json:"tenant_regex" yaml:"tenant_regex"`
	SharedIndex      SharedIndex    `json:"shared_index" yaml:"shared_index"`
	IndexPerTenant   IndexPerTenant `json:"index_per_tenant" yaml:"index_per_tenant"`
	ReadTimeout      time.Duration  `json:"read_timeout" yaml:"read_timeout"`
	WriteTimeout     time.Duration  `json:"write_timeout" yaml:"write_timeout"`
	IdleTimeout      time.Duration  `json:"idle_timeout" yaml:"idle_timeout"`
}

func Default() Config {
	return Config{
		Ports: Ports{
			HTTP:  8080,
			Admin: 0,
		},
		UpstreamURL:      "http://localhost:9200",
		Mode:             "shared",
		PassthroughPaths: []string{},
		TenantRegex: TenantRegex{
			Pattern: `^(?P<prefix>.*)/tenant/(?P<tenant>[^/]+)(?P<postfix>/.*)?$`,
		},
		SharedIndex: SharedIndex{
			Name:          "shared-index",
			AliasTemplate: "{index}-{tenant}",
			TenantField:   "tenant_id",
		},
		IndexPerTenant: IndexPerTenant{
			IndexTemplate: "tenant-{tenant}",
		},
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
}
