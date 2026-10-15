// SPDX-License-Identifier: AGPL-3.0-or-later
package authclient

import (
	"context"
	"net/url"

	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/lib/services"
	"github.com/gravitational/trace"
)

// GetZitadelAuthPreference reads the authoritative preference for management
// operations. The regular API intentionally serves a cached preference.
func (c *Client) GetZitadelAuthPreference(ctx context.Context) (types.AuthPreference, error) {
	response, err := c.HTTPClient.Get(ctx, c.HTTPClient.Endpoint("zitadel", "authpreference"), url.Values{})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return services.UnmarshalAuthPreference(response.Bytes())
}
