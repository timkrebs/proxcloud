package catalog

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"text/template"

	"gopkg.in/yaml.v3"
)

// servicesFS embeds every catalog definition so the running binary and its
// catalog can never drift (ADR-0026). `all:` includes files a bare embed would
// skip; the tree must live under this package because //go:embed cannot cross
// the module boundary or use ".." — see README.md.
//
//go:embed all:services
var servicesFS embed.FS

const (
	serviceFile   = "service.yaml"
	cloudInitFile = "cloud-init.yaml.tftpl"
	nextStepsFile = "next-steps.md.tftpl"
)

// Catalog is the loaded, validated set of platform services. It is immutable
// after Load; List/Get are read-only.
type Catalog struct {
	byID  map[string]*ServiceDef
	order []string // ids sorted for a stable List
}

// Load parses and validates every embedded definition, failing fast on a
// malformed or duplicate-id service (the process must refuse to boot rather than
// half-provision a guest). It is called once at startup.
func Load() (*Catalog, error) {
	return loadFS(servicesFS, "services")
}

// loadFS is Load against an arbitrary fs.FS rooted at root — the seam the tests
// use to feed malformed definitions without touching the embedded tree.
func loadFS(fsys fs.FS, root string) (*Catalog, error) {
	entries, err := fs.ReadDir(fsys, root)
	if err != nil {
		return nil, fmt.Errorf("catalog: read %s: %w", root, err)
	}
	c := &Catalog{byID: map[string]*ServiceDef{}}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := e.Name()
		def, err := loadService(fsys, path.Join(root, dir), dir)
		if err != nil {
			return nil, err
		}
		if _, dup := c.byID[def.ID]; dup {
			return nil, fmt.Errorf("catalog: duplicate service id %q", def.ID)
		}
		c.byID[def.ID] = def
		c.order = append(c.order, def.ID)
	}
	if len(c.order) == 0 {
		return nil, errors.New("catalog: no service definitions found")
	}
	sort.Strings(c.order)
	return c, nil
}

func loadService(fsys fs.FS, dir, name string) (*ServiceDef, error) {
	raw, err := fs.ReadFile(fsys, path.Join(dir, serviceFile))
	if err != nil {
		return nil, fmt.Errorf("catalog: %s/%s: %w", name, serviceFile, err)
	}
	var def ServiceDef
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true) // an unknown field is an authoring mistake — fail fast.
	if err := dec.Decode(&def); err != nil {
		return nil, fmt.Errorf("catalog: parse %s/%s: %w", name, serviceFile, err)
	}
	if err := def.validate(name); err != nil {
		return nil, fmt.Errorf("catalog: invalid %s: %w", name, err)
	}

	if def.IsSet() {
		// A set service ships one cloud-init template per role, named
		// "<role>.cloud-init.yaml.tftpl" (ADR-0029/0030). There is no single
		// cloud-init.yaml.tftpl.
		def.roleCloudInit = map[string]*template.Template{}
		for _, role := range def.Roles {
			file := role.Name + ".cloud-init.yaml.tftpl"
			t, err := parseTemplate(fsys, dir, file, def.ID)
			if err != nil {
				return nil, err
			}
			def.roleCloudInit[role.Name] = t
		}
	} else {
		ci, err := parseTemplate(fsys, dir, cloudInitFile, def.ID)
		if err != nil {
			return nil, err
		}
		def.cloudInit = ci
	}
	ns, err := parseTemplate(fsys, dir, nextStepsFile, def.ID)
	if err != nil {
		return nil, err
	}
	def.nextSteps = ns
	return &def, nil
}

// parseTemplate loads and parses a service template with Missingkey=error so a
// template referencing an input the renderer does not supply fails the render
// loudly instead of emitting "<no value>" into a #cloud-config.
func parseTemplate(fsys fs.FS, dir, file, id string) (*template.Template, error) {
	raw, err := fs.ReadFile(fsys, path.Join(dir, file))
	if err != nil {
		return nil, fmt.Errorf("catalog: %s/%s: %w", id, file, err)
	}
	t, err := template.New(file).Option("missingkey=error").Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("catalog: parse template %s/%s: %w", id, file, err)
	}
	return t, nil
}

// List returns the services in a stable (id-sorted) order.
func (c *Catalog) List() []*ServiceDef {
	out := make([]*ServiceDef, 0, len(c.order))
	for _, id := range c.order {
		out = append(out, c.byID[id])
	}
	return out
}

// Get returns the service with id, or (nil, false). The catalog is global, so an
// unknown id is a genuine "no such service" (the handler maps it to 404).
func (c *Catalog) Get(id string) (*ServiceDef, bool) {
	s, ok := c.byID[id]
	return s, ok
}
