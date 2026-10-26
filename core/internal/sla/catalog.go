package sla

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
)

const (
	maxServices     = 64
	maxServiceLinks = 128
	maxServiceRefs  = 32
)

// Catalog is the editable service tree and manual topology.
// A saved file replaces the startup configuration on the next start.
type Catalog struct {
	mu       sync.Mutex
	path     string
	services []Service
	links    []Link
}

type catalogFile struct {
	Services []Service `json:"services"`
	Links    []Link    `json:"links"`
}

// OpenCatalog seeds from the process configuration. When path exists, that
// file is the saved edit and replaces the seed.
func OpenCatalog(path string, services []Service, links []Link) (*Catalog, error) {
	services, links, err := copyValid(services, links)
	if err != nil {
		return nil, err
	}
	c := &Catalog{path: path, services: services, links: links}
	if path == "" {
		return c, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, nil
		}
		return nil, err
	}
	var file catalogFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	services, links, err = copyValid(file.Services, file.Links)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.services, c.links = services, links
	return c, nil
}

func (c *Catalog) Services() []Service {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return copyServices(c.services)
}

func (c *Catalog) Links() []Link {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return copyLinks(c.links)
}

// ReplaceServices stores a new service tree.
func (c *Catalog) ReplaceServices(services []Service) ([]Service, error) {
	services, _, err := c.replace(services, nil, true, false)
	return services, err
}

// ReplaceLinks stores manual topology edges.
func (c *Catalog) ReplaceLinks(links []Link) ([]Link, error) {
	_, links, err := c.replace(nil, links, false, true)
	return links, err
}

func (c *Catalog) replace(services []Service, links []Link, servicesSet, linksSet bool) ([]Service, []Link, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	nextServices, nextLinks := c.services, c.links
	if servicesSet {
		nextServices = services
	}
	if linksSet {
		nextLinks = links
	}
	validServices, validLinks, err := copyValid(nextServices, nextLinks)
	if err != nil {
		return nil, nil, err
	}
	if err := c.saveLocked(validServices, validLinks); err != nil {
		return nil, nil, err
	}
	c.services, c.links = validServices, validLinks
	return copyServices(c.services), copyLinks(c.links), nil
}

func (c *Catalog) saveLocked(services []Service, links []Link) error {
	if c.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(catalogFile{Services: services, Links: links}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.path), ".services-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), c.path)
}

func copyValid(services []Service, links []Link) ([]Service, []Link, error) {
	if len(services) > maxServices {
		return nil, nil, fmt.Errorf("at most %d services", maxServices)
	}
	if len(links) > maxServiceLinks {
		return nil, nil, fmt.Errorf("at most %d topology links", maxServiceLinks)
	}
	names := map[string]bool{}
	out := make([]Service, 0, len(services))
	for _, svc := range services {
		name, err := cleanName(svc.Name)
		if err != nil {
			return nil, nil, fmt.Errorf("service: %w", err)
		}
		if names[name] {
			return nil, nil, fmt.Errorf("duplicate service %s", name)
		}
		names[name] = true
		alarms, err := cleanRefs(svc.Alarms)
		if err != nil {
			return nil, nil, fmt.Errorf("service %s alarms: %w", name, err)
		}
		children, err := cleanRefs(svc.Children)
		if err != nil {
			return nil, nil, fmt.Errorf("service %s children: %w", name, err)
		}
		out = append(out, Service{Name: name, Alarms: alarms, Children: children})
	}
	for _, svc := range out {
		for _, child := range svc.Children {
			if !names[child] {
				return nil, nil, fmt.Errorf("service %s child %s does not exist", svc.Name, child)
			}
			if child == svc.Name {
				return nil, nil, fmt.Errorf("service %s cannot contain itself", svc.Name)
			}
		}
	}
	if err := serviceCycle(out); err != nil {
		return nil, nil, err
	}
	edges := make([]Link, 0, len(links))
	seenEdge := map[string]bool{}
	for _, link := range links {
		source, err := cleanName(link.Source)
		if err != nil {
			return nil, nil, fmt.Errorf("link source: %w", err)
		}
		target, err := cleanName(link.Target)
		if err != nil {
			return nil, nil, fmt.Errorf("link target: %w", err)
		}
		key := source + "\x00" + target
		if seenEdge[key] {
			continue
		}
		seenEdge[key] = true
		edges = append(edges, Link{Source: source, Target: target})
	}
	return out, edges, nil
}

func serviceCycle(services []Service) error {
	children := map[string][]string{}
	for _, svc := range services {
		children[svc.Name] = svc.Children
	}
	state := map[string]int{}
	var visit func(name string) error
	visit = func(name string) error {
		switch state[name] {
		case 1:
			return fmt.Errorf("service cycle at %s", name)
		case 2:
			return nil
		}
		state[name] = 1
		for _, child := range children[name] {
			if err := visit(child); err != nil {
				return err
			}
		}
		state[name] = 2
		return nil
	}
	for _, svc := range services {
		if err := visit(svc.Name); err != nil {
			return err
		}
	}
	return nil
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		return "", errors.New("name required")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", errors.New("name has control characters")
		}
	}
	return name, nil
}

func cleanRefs(refs []string) ([]string, error) {
	if len(refs) > maxServiceRefs {
		return nil, fmt.Errorf("at most %d entries", maxServiceRefs)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		ref, err := cleanName(ref)
		if err != nil {
			return nil, err
		}
		if seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	return out, nil
}

func copyServices(services []Service) []Service {
	out := make([]Service, len(services))
	for i, svc := range services {
		out[i] = svc
		out[i].Alarms = append([]string(nil), svc.Alarms...)
		out[i].Children = append([]string(nil), svc.Children...)
	}
	return out
}

func copyLinks(links []Link) []Link {
	return append([]Link(nil), links...)
}
