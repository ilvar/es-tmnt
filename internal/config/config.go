package config

import "time"

type Ports struct {
	HTTP  int `json:"http"`
	Admin int `json:"admin"`
}

type Regex struct {
	Enabled     bool   `json:"enabled"`
	Pattern     string `json:"pattern"`
	Replacement string `json:"replacement"`
}

type Config struct {
	Ports        Ports         `json:"ports"`
	UpstreamURL  string        `json:"upstream_url"`
	Mode         string        `json:"mode"`
	Passthrough  []string      `json:"passthrough"`
	Regex        Regex         `json:"regex"`
	ReadTimeout  time.Duration `json:"read_timeout"`
	WriteTimeout time.Duration `json:"write_timeout"`
	IdleTimeout  time.Duration `json:"idle_timeout"`
}

func Default() Config {
	return Config{
		Ports: Ports{
			HTTP:  8080,
			Admin: 0,
		},
		UpstreamURL: "http://localhost:9200",
		Mode:        "regex",
		Passthrough: []string{},
		Regex: Regex{
			Enabled:     true,
			Pattern:     `^/tenant/([^/]+)/`,
			Replacement: `/$1/`,
		},
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
}
