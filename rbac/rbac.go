// Package rbac evaluates tenant-wide method roles from named holders, groups
// and bindings. Sources answer grants rather than database rows, so a file and
// an application's directory can share the same evaluation without copying
// configuration into a database. Tenant and object visibility remain the
// application's responsibility.
package rbac

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/slug"
)

// Rules is a complete policy. Names are @tenant/subject, using canonical
// aliases or UUIDs as the application requires. Every reference in a
// binding or membership must stay in that tenant. Groups are not nested and
// only bindings grant permissions. An empty policy grants nothing.
type Rules struct {
	Roles    map[string]Role  `yaml:"roles" json:"roles"`
	Groups   map[string]Group `yaml:"groups" json:"groups"`
	Bindings []Binding        `yaml:"bindings" json:"bindings"`
}

type Role struct {
	Methods []string `yaml:"methods" json:"methods"`
}
type Group struct {
	Members []string `yaml:"members" json:"members"`
}
type Binding struct {
	Role   string `yaml:"role" json:"role"`
	Holder string `yaml:"holder,omitempty" json:"holder,omitempty"`
	Group  string `yaml:"group,omitempty" json:"group,omitempty"`
}

// Grant retains the role responsible for a decision. Source identifies the
// provider; it is diagnostic, never part of a permission comparison.
type Grant struct {
	Source  string
	Role    string
	Methods []string
}

// Source answers the current tenant-wide grants of a canonical holder name.
// A lookup failure must be returned, never treated as an empty grant set.
type Source interface {
	Grants(context.Context, string) ([]Grant, error)
}
type SourceFunc func(context.Context, string) ([]Grant, error)

func (f SourceFunc) Grants(ctx context.Context, name string) ([]Grant, error) { return f(ctx, name) }

// Union combines independent sources. Removing a grant from one source does
// not withdraw a grant another source still holds.
type Union []Source

func (u Union) Grants(ctx context.Context, name string) ([]Grant, error) {
	var out []Grant
	for _, s := range u {
		gs, err := s.Grants(ctx, name)
		if err != nil {
			return nil, err
		}
		out = append(out, gs...)
	}
	return out, nil
}

// Allows uses the same pattern grammar as credential grants.
func Allows(gs []Grant, method string) bool {
	for _, g := range gs {
		for _, p := range g.Methods {
			if frame.Covers(p, method) {
				return true
			}
		}
	}
	return false
}

// Methods returns the sorted union, without expanding wildcards against the
// methods of one binary (which would widen differently across releases).
func Methods(gs []Grant) []string {
	m := map[string]bool{}
	for _, g := range gs {
		for _, p := range g.Methods {
			m[p] = true
		}
	}
	return slices.Sorted(maps.Keys(m))
}

// ValidateMethod rejects patterns that look like globs but Covers would treat
// as literals. In particular, the universal pattern is /*.*/*, not *.
func ValidateMethod(v string) error {
	bad := func() error {
		return fmt.Errorf("method %q: want /package.Service/Method with * only as a whole component", v)
	}
	if !strings.HasPrefix(v, "/") {
		return bad()
	}
	service, method, ok := strings.Cut(v[1:], "/")
	if !ok || strings.Contains(method, "/") {
		return bad()
	}
	i := strings.LastIndexByte(service, '.')
	if i < 1 {
		return bad()
	}
	for n, part := range []string{service[:i], service[i+1:], method} {
		if part == "*" {
			continue
		}
		if part == "" {
			return bad()
		}
		for _, p := range strings.Split(part, ".") {
			if p == "" || (n != 0 && strings.Contains(part, ".")) {
				return bad()
			}
			for j, c := range p {
				if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || j > 0 && c >= '0' && c <= '9') {
					return bad()
				}
			}
		}
	}
	return nil
}

// Name checks the unambiguous, tenant-qualified spelling a policy uses.
func Name(v string) (tenant string, err error) {
	body, ok := strings.CutPrefix(v, "@")
	t, subject, separated := strings.Cut(body, "/")
	if !ok || !separated {
		return "", fmt.Errorf("name %q: want @tenant/subject", v)
	}
	for _, part := range []string{t, subject} {
		if slug.Validate(part) == nil {
			continue
		}
		id, err := pdid.Parse(part)
		if err != nil || id.IsZero() || id.String() != part {
			return "", fmt.Errorf("name %q: components must be canonical aliases or UUIDs", v)
		}
	}
	return t, nil
}

// Snapshot is immutable, including with respect to the Rules it was built
// from and the grants returned to callers.
type Snapshot struct{ byHolder map[string][]Grant }

func New(r Rules) (*Snapshot, error) {
	s := &Snapshot{byHolder: map[string][]Grant{}}
	for _, name := range slices.Sorted(maps.Keys(r.Roles)) {
		if _, err := Name(name); err != nil {
			return nil, fmt.Errorf("role: %w", err)
		}
		for _, m := range r.Roles[name].Methods {
			if err := ValidateMethod(m); err != nil {
				return nil, fmt.Errorf("role %s: %w", name, err)
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(r.Groups)) {
		t, err := Name(name)
		if err != nil {
			return nil, fmt.Errorf("group: %w", err)
		}
		for _, member := range r.Groups[name].Members {
			mt, err := Name(member)
			if err != nil {
				return nil, fmt.Errorf("group %s: %w", name, err)
			}
			if mt != t {
				return nil, fmt.Errorf("group %s: member %s belongs to another tenant", name, member)
			}
		}
	}
	for i, b := range r.Bindings {
		role, ok := r.Roles[b.Role]
		if !ok {
			return nil, fmt.Errorf("binding %d: unknown role %q", i, b.Role)
		}
		t, _ := Name(b.Role)
		if (b.Holder == "") == (b.Group == "") {
			return nil, fmt.Errorf("binding %d: name exactly one holder or group", i)
		}
		members := []string{b.Holder}
		name := b.Holder
		if b.Group != "" {
			g, ok := r.Groups[b.Group]
			if !ok {
				return nil, fmt.Errorf("binding %d: unknown group %q", i, b.Group)
			}
			members, name = g.Members, b.Group
		}
		bt, err := Name(name)
		if err != nil {
			return nil, fmt.Errorf("binding %d: %w", i, err)
		}
		if t != bt {
			return nil, fmt.Errorf("binding %d: role and subject belong to different tenants", i)
		}
		for _, member := range members {
			s.byHolder[member] = append(s.byHolder[member], Grant{Role: b.Role, Methods: slices.Clone(role.Methods)})
		}
	}
	return s, nil
}

func (r *Rules) Validate() error { _, err := New(*r); return err }

func (s *Snapshot) Grants(_ context.Context, name string) ([]Grant, error) {
	if _, err := Name(name); err != nil {
		return nil, err
	}
	out := slices.Clone(s.byHolder[name])
	for i := range out {
		out[i].Methods = slices.Clone(out[i].Methods)
	}
	return out, nil
}
