package authz

import "github.com/ncecere/pi-durable-platform/internal/apperr"

// Rules of agent templates.
//
//   - Platform templates (no team) are curated by platform admins and are
//     always in the catalog: everyone signed in sees and deploys them.
//   - A team template is private to its team: its members see it and deploy
//     it; editors and above (people or editor service accounts) change it
//     and add versions. The team's owners and admins ask to publish it to
//     the catalog, or take it out again; a platform admin approves or rejects
//     the request. Published, every team sees and deploys it.
//   - Archived templates take no new deployments; existing agents keep
//     their version.
//   - Platform admins and auditors see every template (they review them).

// Template statuses.
const (
	TemplatePrivate   = "private"
	TemplatePending   = "pending"
	TemplatePublished = "published"
)

var errTemplateNotFound = apperr.NotFound("template_not_found", "Agent template not found")

// TemplateSubject is a template as seen by an actor.
type TemplateSubject struct {
	Platform bool   // owned by the platform (no team)
	Status   string // private, pending or published
	Archived bool
	Role     string // the actor's role in the owning team ("" when not on it)
	// Actor kind.
	IsService       bool
	IsPlatformAdmin bool
	IsStaff         bool // a platform admin or auditor
}

// CheckTemplateSee decides whether the actor may see the template and its
// versions. Not allowed reads as not found.
func CheckTemplateSee(s TemplateSubject) error {
	if s.Platform || s.Status == TemplatePublished || RoleAtLeast(s.Role, RoleMember) || s.IsStaff {
		return nil
	}
	return errTemplateNotFound
}

// CheckTemplateEdit decides whether the actor may change the template (name,
// description, archived) and add versions.
func CheckTemplateEdit(s TemplateSubject) error {
	if err := CheckTemplateSee(s); err != nil {
		return err
	}
	if s.Platform {
		if s.IsPlatformAdmin {
			return nil
		}
		return apperr.Forbidden("Only platform admins can change platform templates")
	}
	if RoleAtLeast(s.Role, RoleEditor) {
		return nil
	}
	return apperr.Forbidden("Only the team's editors, admins and owners can change its templates")
}

// CheckTemplatePublish decides whether the actor may ask to publish a team
// template to the catalog, or take it out of the catalog: the owning team's
// owners and admins.
func CheckTemplatePublish(s TemplateSubject) error {
	if err := CheckTemplateSee(s); err != nil {
		return err
	}
	if s.Platform {
		return apperr.Conflict("platform_template", "Platform templates are always in the catalog")
	}
	if s.IsService || !RoleAtLeast(s.Role, RoleAdmin) {
		return apperr.Forbidden("Only the team's owners and admins can publish its templates")
	}
	return nil
}

// CheckTemplateReview decides whether the actor may approve or reject a
// request to publish: platform admins.
func CheckTemplateReview(s TemplateSubject) error {
	if err := CheckTemplateSee(s); err != nil {
		return err
	}
	if !s.IsPlatformAdmin {
		return apperr.Forbidden("Only platform admins review templates for the catalog")
	}
	return nil
}

// CheckTemplateDeploy decides whether a team may deploy the template:
// platform and published templates, and the team's own (ownTeam). Archived
// templates take no new deployments.
func CheckTemplateDeploy(s TemplateSubject, ownTeam bool) error {
	if !(s.Platform || s.Status == TemplatePublished || ownTeam) {
		return errTemplateNotFound
	}
	if s.Archived {
		return apperr.Conflict("template_archived", "This template is archived and takes no new deployments")
	}
	return nil
}
