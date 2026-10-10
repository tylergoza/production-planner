package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/tylergoza/production-planner/internal/sso"
	"github.com/tylergoza/production-planner/internal/store"
)

// The app-admin Users page, with SSO on ----------------------------------
//
// People, their roles and suspensions live in User Management; this page
// reads and changes them through its API, acting as the signed-in admin
// (their session's grant goes along as X-Acting-Grant, and User Management
// decides whether they may). Adding people, passwords and renames are for
// a user admin there.

const ssoCallTimeout = 8 * time.Second

// ssoUserOf is how a person from User Management is cached here.
func ssoUserOf(u sso.AppUser) store.SSOUser {
	return store.SSOUser{Subject: u.Sub, Username: u.Username, DisplayName: u.Name, IsAdmin: u.Role == "admin", Active: u.Active}
}

// syncPeople fetches everyone with access to this app (cached for a
// minute in internal/sso) and brings the local users table in line, so
// people pickers include people who haven't signed in here yet. On error
// the local cache is left as it is.
func (s *Server) syncPeople(ctx context.Context) (*sso.AppUsers, error) {
	ctx, cancel := context.WithTimeout(ctx, ssoCallTimeout)
	defer cancel()
	list, err := s.sso.AppUsers(ctx)
	if err != nil {
		s.log.Warn("sync people from User Management failed; using the local list", "err", err)
		return nil, err
	}
	users := make([]store.SSOUser, 0, len(list.Users))
	for _, u := range list.Users {
		users = append(users, ssoUserOf(u))
	}
	if err := s.store.SyncSSOUsers(users); err != nil {
		s.log.Error("sync people: update local users", "err", err)
		return list, err
	}
	return list, nil
}

// pickableUsers is the choices for a people picker. keep is who's already
// attached, so they still show if their access was removed. With SSO off
// it's everyone, as before.
func (s *Server) pickableUsers(r *http.Request, keep []int64) ([]store.User, error) {
	if !s.sso.Enabled() {
		return s.store.ListUsers()
	}
	s.syncPeople(r.Context())
	return s.store.ListPickableUsers(keep)
}

// actingGrant is the signed-in admin's SSO grant, or "" when they signed
// in with the break-glass local login.
func actingGrant(r *http.Request) string {
	if sess, _ := r.Context().Value(ctxSession).(*store.Session); sess != nil {
		return sess.Grant
	}
	return ""
}

func (s *Server) handleSSOUsers(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Title":     "Users",
		"ReadOnly":  actingGrant(r) == "",
		"ManageURL": s.sso.PublicURL + "/admin/users",
	}
	list, err := s.syncPeople(r.Context())
	if err != nil && list == nil {
		msg := "Something went wrong reading the list from User Management."
		var ae *sso.APIError
		switch {
		case errors.As(err, &ae):
			msg = ae.Message()
		case errors.Is(err, sso.ErrUnreachable):
			msg = "User Management can't be reached right now, so people can't be listed or changed. Please try again in a few minutes."
		}
		data["LoadError"] = msg
		s.render(w, r, http.StatusOK, "users/sso", data)
		return
	}
	me := currentUser(r)
	for _, u := range list.Users {
		if u.Sub != "" && u.Sub == me.SSOSubject && u.UserAdmin {
			data["UserAdmin"] = true
		}
	}
	data["People"], data["Roles"] = list.Users, list.Roles
	s.render(w, r, http.StatusOK, "users/sso", data)
}

// handleSSOAccessUpdate changes one person's role or suspension in User
// Management. The form sends either role or suspended ("1" or "0").
func (s *Server) handleSSOAccessUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.sso.Enabled() {
		s.notFound(w, r)
		return
	}
	back := func(kind, msg string) {
		s.setFlash(w, r, kind, msg)
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	}
	grant := actingGrant(r)
	if grant == "" {
		back("error", "You signed in with the local login, so changes can't be made here. Sign in through User Management to change people's access.")
		return
	}
	var role *string
	var suspended *bool
	if v, ok := r.PostForm["role"]; ok && len(v) > 0 {
		role = &v[0]
	}
	switch r.PostFormValue("suspended") {
	case "1":
		t := true
		suspended = &t
	case "0":
		f := false
		suspended = &f
	}
	if role == nil && suspended == nil {
		back("error", "Nothing to change.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), ssoCallTimeout)
	defer cancel()
	u, err := s.sso.UpdateAppUser(ctx, grant, r.PathValue("sub"), role, suspended)
	if err != nil {
		s.log.Warn("change access in User Management refused or failed", "by", currentUser(r).Username, "sub", r.PathValue("sub"), "err", err)
		var ae *sso.APIError
		switch {
		case errors.As(err, &ae):
			back("error", ae.Message())
		case errors.Is(err, sso.ErrUnreachable):
			back("error", "User Management can't be reached right now. Nothing was changed; please try again in a few minutes.")
		default:
			back("error", "Something went wrong; nothing was changed.")
		}
		return
	}
	s.sso.Forget()
	local, err := s.store.SaveSSOUser(ssoUserOf(*u))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	name := local.Name()
	if !u.Active {
		// Takes effect at once: sign them out of this app now rather than
		// at their next grant check.
		if err := s.store.DeleteSessionsForUser(local.ID); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	msg := name + " updated."
	switch {
	case suspended != nil && *suspended:
		msg = name + " is suspended and has been signed out of this app."
	case suspended != nil:
		msg = name + " can use this app again."
	case role != nil:
		msg = name + " is now " + u.Role + "."
	}
	// Someone who just took away their own admin can't see this page any more.
	if local.ID == currentUser(r).ID && (!local.IsAdmin || !local.Active) {
		s.redirect(w, r, "/", msg)
		return
	}
	s.redirect(w, r, "/admin/users", msg)
}
