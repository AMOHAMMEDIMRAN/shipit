package detect

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Service is what shipit reads from one compose service. shipit only reads
// the file. It never edits it.
type Service struct {
	Name          string
	Image         string
	ContainerName string
	HasBuild      bool
	BuildContext  string
	Dockerfile    string
	Ports         []Port
	EnvFiles      []string
	HasHealth     bool
}

// Port is one published port.
type Port struct {
	Host      string
	Bind      string
	Container string
}

// Compose is the facts shipit needs from a compose file.
type Compose struct {
	File     string
	Name     string
	Services []Service
	Volumes  []string
}

type rawCompose struct {
	Name     string                `yaml:"name"`
	Services map[string]rawService `yaml:"services"`
	Volumes  map[string]any        `yaml:"volumes"`
}

type rawService struct {
	Image         string    `yaml:"image"`
	ContainerName string    `yaml:"container_name"`
	Build         yaml.Node `yaml:"build"`
	Ports         []any     `yaml:"ports"`
	EnvFile       yaml.Node `yaml:"env_file"`
	Healthcheck   any       `yaml:"healthcheck"`
}

// ParseCompose reads a compose file for facts. Problems are explained in plain words.
func ParseCompose(path string) (*Compose, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseComposeData(path, b)
}

// ParseComposeData reads compose content that is already in memory, for
// example a file taken from a git commit. name is only used in messages.
func ParseComposeData(path string, b []byte) (*Compose, error) {
	var raw rawCompose
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("%s is not valid YAML: %v", path, err)
	}
	if len(raw.Services) == 0 {
		return nil, fmt.Errorf("%s has no services", path)
	}
	c := &Compose{File: path, Name: raw.Name}
	names := make([]string, 0, len(raw.Services))
	for n := range raw.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		r := raw.Services[n]
		s := Service{Name: n, Image: r.Image, ContainerName: r.ContainerName, HasHealth: r.Healthcheck != nil}
		switch r.Build.Kind {
		case yaml.ScalarNode:
			s.HasBuild, s.BuildContext = true, r.Build.Value
		case yaml.MappingNode:
			s.HasBuild = true
			var bm struct {
				Context    string `yaml:"context"`
				Dockerfile string `yaml:"dockerfile"`
			}
			_ = r.Build.Decode(&bm)
			s.BuildContext, s.Dockerfile = bm.Context, bm.Dockerfile
		}
		for _, p := range r.Ports {
			if port, ok := parsePort(p); ok {
				s.Ports = append(s.Ports, port)
			}
		}
		switch r.EnvFile.Kind {
		case yaml.ScalarNode:
			s.EnvFiles = append(s.EnvFiles, r.EnvFile.Value)
		case yaml.SequenceNode:
			for _, it := range r.EnvFile.Content {
				switch it.Kind {
				case yaml.ScalarNode:
					s.EnvFiles = append(s.EnvFiles, it.Value)
				case yaml.MappingNode:
					var m struct {
						Path string `yaml:"path"`
					}
					_ = it.Decode(&m)
					if m.Path != "" {
						s.EnvFiles = append(s.EnvFiles, m.Path)
					}
				}
			}
		}
		c.Services = append(c.Services, s)
	}
	for v := range raw.Volumes {
		c.Volumes = append(c.Volumes, v)
	}
	sort.Strings(c.Volumes)
	return c, nil
}

var portRe = regexp.MustCompile(`^(?:(\d{1,3}(?:\.\d{1,3}){3}|\[[0-9a-f:]+\]):)?(?:(\d+)(?:-\d+)?:)?(\d+)(?:-\d+)?(?:/(?:tcp|udp))?$`)

func parsePort(v any) (Port, bool) {
	switch t := v.(type) {
	case string:
		m := portRe.FindStringSubmatch(strings.TrimSpace(t))
		if m == nil || m[2] == "" {
			return Port{}, false // not published to the host
		}
		return Port{Bind: m[1], Host: m[2], Container: m[3]}, true
	case int:
		return Port{}, false
	case map[string]any:
		pub, ok := t["published"]
		if !ok {
			return Port{}, false
		}
		tgt := t["target"]
		return Port{Host: fmt.Sprint(pub), Container: fmt.Sprint(tgt), Bind: fmt.Sprint(t["host_ip"])}, true
	}
	return Port{}, false
}

// AnyBuild says whether any service is built from source.
func (c *Compose) AnyBuild() bool {
	for _, s := range c.Services {
		if s.HasBuild {
			return true
		}
	}
	return false
}

// HealthURL suggests a health address from the first published port.
func (c *Compose) HealthURL() string {
	for _, s := range c.Services {
		for _, p := range s.Ports {
			host := p.Bind
			if host == "" || host == "0.0.0.0" || host == "<nil>" {
				host = "127.0.0.1"
			}
			return fmt.Sprintf("http://%s:%s/", host, p.Host)
		}
	}
	return ""
}

// HostPorts lists every port the file publishes, for the port-conflict check.
func (c *Compose) HostPorts() []string {
	var out []string
	for _, s := range c.Services {
		for _, p := range s.Ports {
			out = append(out, p.Host)
		}
	}
	return out
}

// FixedNames says whether the file pins container names, which means a new
// version has to replace the old container in place.
func (c *Compose) FixedNames() bool {
	for _, s := range c.Services {
		if s.ContainerName != "" {
			return true
		}
	}
	return false
}

var tagRe = regexp.MustCompile(`:[^:/]+$`)

// Override builds the small extra file that gives every built service a
// version-specific image tag, so going back to a version reuses its image.
func (c *Compose) Override(project, tag string) string {
	var sb strings.Builder
	sb.WriteString("# written by shipit for this version. Your own compose file is never changed.\nservices:\n")
	any := false
	for _, s := range c.Services {
		if !s.HasBuild {
			continue
		}
		base := tagRe.ReplaceAllString(s.Image, "")
		if base == "" {
			base = "shipit-" + project + "-" + s.Name
		}
		sb.WriteString("  " + s.Name + ":\n    image: " + base + ":" + tag + "\n")
		any = true
	}
	if !any {
		return ""
	}
	return sb.String()
}
