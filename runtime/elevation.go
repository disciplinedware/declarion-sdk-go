package runtime

import (
	"slices"

	"github.com/disciplinedware/declarion-sdk-go/execution"
)

func (c *HandlerCtx) WithSystemRole() *HandlerCtx {
	next := *c
	next.Platform = c.Platform.WithSystemRole()
	next.projectAuthority()
	return &next
}

func (c *HandlerCtx) WithGrants(grants ...string) (*HandlerCtx, error) {
	client, err := c.Platform.WithGrants(grants...)
	if err != nil {
		return nil, err
	}
	next := *c
	next.Platform = client
	next.projectAuthority()
	return &next, nil
}

func (c *HandlerCtx) projectAuthority() {
	if c.claims == nil {
		return
	}
	p := c.claims
	c.UserID, c.RealUserID, c.AgentID = p.UserID, p.RealUserID, p.AgentID
	c.TenantID, c.TenantCode, c.Kind = p.TenantID, p.TenantCode, p.Kind
	c.Roles = slices.Clone(p.Roles)
	c.Permissions = slices.Clone(p.Permissions)
	c.IsSuperadmin, c.IsTenantOwner, c.IsGlobalUser = p.IsSuperadmin, p.IsTenantOwner, p.IsGlobalUser
	c.CallerContext = p.CallerContext
	c.Attributes = execution.CloneAttributes(p.Attributes)
	c.RoleAttributes = execution.CloneAttributes(p.RoleAttributes)
	if p.InvokeDepth != nil {
		c.InvokeDepth = *p.InvokeDepth
	}
	e := c.Platform.Elevation()
	c.IsSystem = e.System
	c.Permissions = append(c.Permissions, e.Grants...)
	if e.System {
		c.Permissions = append(c.Permissions, "*")
	}
	c.Permissions = execution.Normalize(execution.Elevation{Grants: c.Permissions}).Grants
}
