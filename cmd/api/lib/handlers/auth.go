package handlers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
)

type AuthHandler struct {
	oauthCfg     *oauth2.Config
	jwtSecret    []byte
	allowedOrg   string
	allowedUsers map[string]struct{}
}

func NewAuthHandler(clientID, clientSecret, callbackURL, allowedOrg, jwtSecret string, allowedUsers []string) *AuthHandler {
	users := make(map[string]struct{}, len(allowedUsers))
	for _, u := range allowedUsers {
		users[u] = struct{}{}
	}
	return &AuthHandler{
		oauthCfg: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  callbackURL,
			Scopes:       []string{"read:org", "read:user", "user:email"},
			Endpoint:     github.Endpoint,
		},
		jwtSecret:    []byte(jwtSecret),
		allowedOrg:   allowedOrg,
		allowedUsers: users,
	}
}

func (h *AuthHandler) Login(c echo.Context) error {
	state := randomState()
	c.SetCookie(&http.Cookie{
		Name:     "oauth_state",
		Value:    state,
		HttpOnly: true,
		MaxAge:   300,
		Path:     "/",
	})
	return c.Redirect(http.StatusTemporaryRedirect, h.oauthCfg.AuthCodeURL(state))
}

func (h *AuthHandler) Callback(c echo.Context) error {
	cookie, err := c.Cookie("oauth_state")
	if err != nil || cookie.Value != c.QueryParam("state") {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid state")
	}

	token, err := h.oauthCfg.Exchange(context.Background(), c.QueryParam("code"))
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "failed to exchange code")
	}

	client := h.oauthCfg.Client(context.Background(), token)

	user, err := fetchGitHubUser(client)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fetch user")
	}

	email, err := fetchPrimaryEmail(client)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fetch email")
	}

	orgs, err := fetchOrgs(client)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fetch orgs")
	}

	if len(h.allowedUsers) > 0 {
		if _, ok := h.allowedUsers[user.Login]; !ok {
			return echo.NewHTTPError(http.StatusForbidden, "user not allowed")
		}
	}

	if h.allowedOrg != "" {
		member, err := checkOrgMembership(client, h.allowedOrg, user.Login)
		if err != nil || !member {
			return echo.NewHTTPError(http.StatusForbidden, "not a member of the required org")
		}
	}

	jwtToken := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":    user.Login,
		"id":     user.ID,
		"name":   user.Name,
		"avatar": user.AvatarURL,
		"email":  email,
		"orgs":   orgs,
		"exp":    time.Now().Add(8 * time.Hour).Unix(),
	})
	signed, err := jwtToken.SignedString(h.jwtSecret)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to issue token")
	}

	c.SetCookie(&http.Cookie{
		Name:     "session",
		Value:    signed,
		HttpOnly: true,
		MaxAge:   int((8 * time.Hour).Seconds()),
		Path:     "/",
	})

	return c.Redirect(http.StatusTemporaryRedirect, "/")
}

type githubUser struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}

type githubEmail struct {
	Email   string `json:"email"`
	Primary bool   `json:"primary"`
}

type githubOrg struct {
	Login string `json:"login"`
}

func fetchGitHubUser(client *http.Client) (*githubUser, error) {
	resp, err := client.Get("https://api.github.com/user")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var u githubUser
	return &u, json.NewDecoder(resp.Body).Decode(&u)
}

func fetchPrimaryEmail(client *http.Client) (string, error) {
	resp, err := client.Get("https://api.github.com/user/emails")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var emails []githubEmail
	if err := json.NewDecoder(resp.Body).Decode(&emails); err != nil {
		return "", err
	}
	for _, e := range emails {
		if e.Primary {
			return e.Email, nil
		}
	}
	return "", nil
}

func fetchOrgs(client *http.Client) ([]string, error) {
	resp, err := client.Get("https://api.github.com/user/orgs")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var orgs []githubOrg
	if err := json.NewDecoder(resp.Body).Decode(&orgs); err != nil {
		return nil, err
	}
	names := make([]string, len(orgs))
	for i, o := range orgs {
		names[i] = o.Login
	}
	return names, nil
}

func checkOrgMembership(client *http.Client, org, username string) (bool, error) {
	resp, err := client.Get("https://api.github.com/orgs/" + org + "/members/" + username)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusNoContent, nil
}

func randomState() string {
	b := make([]byte, 16)
	rand.Read(b)
	return base64.URLEncoding.EncodeToString(b)
}
