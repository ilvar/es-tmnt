package config

import "time"

type Ports struct {
	HTTP  int `json:"http"`
	Admin int `json:"admin"`
}

type TenantRegex struct {
	Pattern string `json:"pattern"`
}

type SharedIndex struct {
	Name        string `json:"name"`
	AliasFormat string `json:"alias_format"`
	TenantField string `json:"tenant_field"`
}

type IndexPerTenant struct {
	IndexFormat string `json:"index_format"`
}

type Config struct {
	Ports          Ports          `json:"ports"`
	UpstreamURL    string         `json:"upstream_url"`
	Mode           string         `json:"mode"`
	Passthrough    []string       `json:"passthrough"`
	TenantRegex    TenantRegex    `json:"tenant_regex"`
	SharedIndex    SharedIndex    `json:"shared_index"`
	IndexPerTenant IndexPerTenant `json:"index_per_tenant"`
	ReadTimeout    time.Duration  `json:"read_timeout"`
	WriteTimeout   time.Duration  `json:"write_timeout"`
	IdleTimeout    time.Duration  `json:"idle_timeout"`
}

func Default() Config {
	return Config{
		Ports: Ports{
			HTTP:  8080,
			Admin: 0,
		},
		UpstreamURL: "http://localhost:9200",
		Mode:        "shared-index",
		Passthrough: []string{},
		TenantRegex: TenantRegex{
			Pattern: `^(?P<prefix>.*)/tenant/(?P<tenant>[^/]+)(?P<postfix>/.*)?$`,
		},
		SharedIndex: SharedIndex{
			Name:        "shared-index",
			AliasFormat: "{index}-{tenant}",
			TenantField: "tenant_id",
		},
		IndexPerTenant: IndexPerTenant{
			IndexFormat: "tenant-{tenant}",
		},
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
}
