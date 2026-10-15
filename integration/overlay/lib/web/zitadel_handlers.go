// SPDX-License-Identifier: AGPL-3.0-or-later
// These handlers adapt Teleport's public GitHub login handlers to OIDC and use
// its existing browser CSRF, session cookie, and encrypted CLI response helpers.
package web

import (
	"net"
	"net/http"

	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/lib/client"
	"github.com/gravitational/teleport/lib/httplib"
	"github.com/gravitational/trace"
	"github.com/julienschmidt/httprouter"
)

func (h *Handler) RegisterZitadelHandlers() {
	h.GET("/webapi/oidc/login/web", h.WithRedirect(h.zitadelLoginWeb))
	h.GET("/webapi/oidc/callback", h.WithMetaRedirect(h.zitadelCallback))
	h.POST("/webapi/oidc/login/console", h.WithLimiter(h.zitadelLoginConsole))
	h.GET("/webapi/zitadel/connectors", h.WithAuth(h.listZitadelAuthConnectors))
	h.GET("/webapi/zitadel/connectors/:name", h.WithAuth(h.getZitadelConnector))
	h.POST("/webapi/zitadel/connectors", h.WithAuth(h.createZitadelConnector))
	h.PUT("/webapi/zitadel/connectors/:name", h.WithAuth(h.updateZitadelConnector))
	h.DELETE("/webapi/zitadel/connectors/:name", h.WithAuth(h.deleteZitadelConnector))
}

func (h *Handler) zitadelLoginWeb(w http.ResponseWriter, r *http.Request, p httprouter.Params) string {
	req, err := ParseSSORequestParams(r)
	if err != nil {
		return client.LoginFailedRedirectURL
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return client.LoginFailedRedirectURL
	}
	response, err := h.cfg.ProxyClient.CreateOIDCAuthRequest(r.Context(), types.OIDCAuthRequest{
		ConnectorID: req.ConnectorID, CSRFToken: req.CSRFToken, CreateWebSession: true,
		ClientRedirectURL: req.ClientRedirectURL, ClientLoginIP: ip, ClientUserAgent: r.UserAgent()})
	if err != nil {
		h.logger.WarnContext(r.Context(), "Cannot start Zitadel browser login", "error", err)
		return client.LoginFailedRedirectURL
	}
	return response.RedirectURL
}

func (h *Handler) zitadelLoginConsole(w http.ResponseWriter, r *http.Request, p httprouter.Params) (interface{}, error) {
	req := new(client.SSOLoginConsoleReq)
	if err := httplib.ReadResourceJSON(r, req); err != nil {
		return nil, trace.AccessDenied("%s", SSOLoginFailureMessage)
	}
	if err := req.CheckAndSetDefaults(); err != nil {
		return nil, trace.AccessDenied("%s", SSOLoginFailureMessage)
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return nil, trace.AccessDenied("%s", SSOLoginFailureMessage)
	}
	response, err := h.cfg.ProxyClient.CreateOIDCAuthRequest(r.Context(), types.OIDCAuthRequest{
		ConnectorID: req.ConnectorID, SshPublicKey: req.SSHPubKey, TlsPublicKey: req.TLSPubKey,
		SshAttestationStatement: req.SSHAttestationStatement.ToProto(), TlsAttestationStatement: req.TLSAttestationStatement.ToProto(),
		CertTTL: req.CertTTL, ClientRedirectURL: req.RedirectURL, Compatibility: req.Compatibility,
		RouteToCluster: req.RouteToCluster, KubernetesCluster: req.KubernetesCluster, ClientLoginIP: ip, Scope: req.Scope})
	if err != nil {
		return nil, trace.AccessDenied("%s", SSOLoginFailureMessage)
	}
	return &client.SSOLoginConsoleResponse{RedirectURL: response.RedirectURL}, nil
}

func (h *Handler) zitadelCallback(w http.ResponseWriter, r *http.Request, p httprouter.Params) string {
	// Do not log the callback query: it contains the authorization code.
	response, err := h.cfg.ProxyClient.ValidateOIDCAuthCallback(r.Context(), r.URL.Query())
	if err != nil {
		h.logger.WarnContext(r.Context(), "Zitadel callback rejected", "error", err)
		if requestID := r.URL.Query().Get("state"); requestID != "" {
			if req, lookupErr := h.cfg.ProxyClient.GetOIDCAuthRequest(r.Context(), requestID); lookupErr == nil && !req.CreateWebSession {
				if redirect, encodeErr := RedirectURLWithError(req.ClientRedirectURL, trace.AccessDenied("Zitadel authentication failed")); encodeErr == nil {
					return redirect.String()
				}
			}
		}
		return client.LoginFailedBadCallbackRedirectURL
	}
	if response.Req.CreateWebSession {
		if response.Session == nil {
			return client.LoginFailedRedirectURL
		}
		res := &SSOCallbackResponse{CSRFToken: response.Req.CSRFToken, Username: response.Username,
			SessionName: response.Session.GetName(), SessionExpiry: response.Session.Expiry(), ClientRedirectURL: response.Req.ClientRedirectURL}
		if err := SSOSetWebSessionAndRedirectURL(w, r, res, true); err != nil {
			return client.LoginFailedRedirectURL
		}
		if token := response.Session.GetDeviceWebToken(); token != nil {
			redirect, err := BuildDeviceWebRedirectPath(token, res.ClientRedirectURL)
			if err != nil {
				return client.LoginFailedRedirectURL
			}
			return redirect
		}
		return res.ClientRedirectURL
	}
	if len(response.Req.SSHPubKey)+len(response.Req.TLSPubKey) == 0 {
		return client.LoginFailedRedirectURL
	}
	redirect, err := ConstructSSHResponse(AuthParams{ClientRedirectURL: response.Req.ClientRedirectURL,
		Username: response.Username, Identity: response.Identity, Session: response.Session,
		Cert: response.Cert, TLSCert: response.TLSCert, HostSigners: response.HostSigners, FIPS: h.cfg.FIPS, ClientOptions: response.ClientOptions})
	if err != nil {
		return client.LoginFailedRedirectURL
	}
	return redirect.String()
}
