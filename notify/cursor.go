package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
)

type cursor struct {
	Version int       `json:"v"`
	Scope   string    `json:"s"`
	Created time.Time `json:"c"`
	ID      string    `json:"i"`
}

func (s *Service) encode(p Position, scope string) (string, error) {
	if identifier(p.ID) != nil || timestamppb.New(p.DateCreated).CheckValid() != nil {
		return "", invalid("notification store returned an invalid page position")
	}
	b, err := json.Marshal(cursor{Version: 1, Scope: scope, Created: p.DateCreated.UTC(), ID: p.ID.String()})
	if err != nil {
		return "", err
	}
	h := hmac.New(sha256.New, s.key)
	_, _ = h.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil)), nil
}

func (s *Service) decode(v, scope string) (*Position, error) {
	bad := invalid("cursor is invalid or belongs to another read scope")
	if len(v) > 8192 {
		return nil, bad
	}
	parts := strings.Split(v, ".")
	if len(parts) != 2 {
		return nil, bad
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, bad
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, bad
	}
	h := hmac.New(sha256.New, s.key)
	_, _ = h.Write(b)
	if !hmac.Equal(sig, h.Sum(nil)) {
		return nil, bad
	}
	var c cursor
	if json.Unmarshal(b, &c) != nil || c.Version != 1 || c.Scope != scope || timestamppb.New(c.Created).CheckValid() != nil {
		return nil, bad
	}
	id, err := pdid.Parse(c.ID)
	if err != nil || id.IsZero() {
		return nil, bad
	}
	return &Position{DateCreated: c.Created, ID: id}, nil
}

// scopeKey binds paging to the authenticated identity and its credential's
// authority, not merely a tenant the request happened to name. Stable ordering
// lets a caller reorder the same topic set without invalidating its cursor.
func scopeKey(ctx context.Context, method string, tenant, reader pdid.Id, topics []Topic) string {
	f := frame.Must(ctx)
	origin, _ := grpc.Method(ctx)
	ids := func(vs []pdid.Id) []string {
		out := make([]string, len(vs))
		for i, v := range vs {
			out[i] = v.String()
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	keys := make([]string, len(topics))
	for i, t := range topics {
		keys[i] = t.Key()
	}
	actions := slices.Clone(f.Grant.Actions())
	slices.Sort(actions)
	b, _ := json.Marshal(struct {
		Method, Origin, Tenant, Reader, Actor, ActorTenant, Name, TenantName string
		Topics, Scope, Tenants, Sets, Actions                                []string
		All, AnyTenant, AnySet, AnyAction                                    bool
	}{method, origin, tenant.String(), reader.String(), f.Actor.String(), f.Tenant.String(), f.Name, f.TenantName,
		keys, ids(f.Scope.Ids()), ids(f.Grant.TenantIds()), ids(f.Grant.SetIds()), slices.Compact(actions),
		f.Scope.All(), f.Grant.AnyTenant(), f.Grant.AnySet(), f.Grant.AnyAction()})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
