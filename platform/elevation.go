package platform

import "github.com/disciplinedware/declarion-sdk-go/execution"

func (c *Client) Elevation() execution.Elevation { return execution.Normalize(c.elevation) }

func (c *Client) WithSystemRole() *Client {
	next := *c
	next.elevation = execution.Normalize(c.elevation)
	next.elevation.System = true
	next.selected = true
	return &next
}

func (c *Client) WithGrants(grants ...string) (*Client, error) {
	if c.selectionErr != nil {
		return nil, c.selectionErr
	}
	next := *c
	next.elevation = c.elevation.WithGrants(grants...)
	next.selected = true
	return &next, nil
}
