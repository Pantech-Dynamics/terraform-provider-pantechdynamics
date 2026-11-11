package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Problem codes the IAM endpoints return.
const (
	// CodeIAMTeamNotFound is the 404 for a team that is not in the
	// organization. It counts as ErrNotFound.
	CodeIAMTeamNotFound = "IAM_TEAM_NOT_FOUND"

	// CodeIAMPolicyNotFound is the 404 for an unknown policy. It counts as
	// ErrNotFound.
	CodeIAMPolicyNotFound = "IAM_POLICY_NOT_FOUND"

	// CodeIAMAttachmentNotFound is the 404 for a policy that is not attached
	// to the principal. It counts as ErrNotFound.
	CodeIAMAttachmentNotFound = "IAM_ATTACHMENT_NOT_FOUND"

	// CodeIAMManagedPolicy is the 409 for a change to a Pantech-managed
	// policy.
	CodeIAMManagedPolicy = "IAM_MANAGED_POLICY"

	// CodeIAMAdminTarget is the 403 when an admin acts on an admin, or on a
	// team holding one: only owners may.
	CodeIAMAdminTarget = "IAM_ADMIN_TARGET"

	// CodeInvalidPolicy is the 422 for statements the backend refuses; its
	// errors name statements[i].<field>[j].
	CodeInvalidPolicy = "INVALID_POLICY"
)

// ManagedPolicyPrefix starts the id of every Pantech-managed policy.
const ManagedPolicyPrefix = "pol_pantech_"

// IAMTeam is a named group of the organization's members.
type IAMTeam struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	MemberCount int64      `json:"member_count"`
	PolicyIDs   []string   `json:"policy_ids"`
	CreatedAt   *time.Time `json:"created_at"`
	UpdatedAt   *time.Time `json:"updated_at"`
	Version     int64      `json:"version"`
}

// CreateIAMTeamRequest adds a team.
type CreateIAMTeamRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// UpdateIAMTeamRequest changes a team. A nil field is left out.
type UpdateIAMTeamRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}

// IAMTeamMember is a member of a team. Email and name are personal data.
type IAMTeamMember struct {
	UserID  string     `json:"user_id"`
	Email   string     `json:"email"`
	Name    string     `json:"name"`
	Role    string     `json:"role"`
	AddedAt *time.Time `json:"added_at"`
}

// PolicyConditions must all hold for a statement to apply. A nil MFA is no
// condition; false on a deny means "only without MFA", so nil and false are
// different.
type PolicyConditions struct {
	SourceIP []string `json:"source_ip,omitempty"`
	MFA      *bool    `json:"mfa,omitempty"`
}

// PolicyStatement is one allow or deny statement.
type PolicyStatement struct {
	Sid        string            `json:"sid,omitempty"`
	Effect     string            `json:"effect"`
	Actions    []string          `json:"actions"`
	Resources  []string          `json:"resources"`
	Conditions *PolicyConditions `json:"conditions,omitempty"`
}

// IAMPolicy is an access policy: the organization's own, or Pantech-managed.
type IAMPolicy struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	Managed         bool              `json:"managed"`
	Statements      []PolicyStatement `json:"statements"`
	AttachmentCount int64             `json:"attachment_count"`
	CreatedAt       *time.Time        `json:"created_at"`
	UpdatedAt       *time.Time        `json:"updated_at"`
	Version         int64             `json:"version"`
}

// IAMPolicyRequest creates or replaces a policy: PUT takes the whole policy.
type IAMPolicyRequest struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Statements  []PolicyStatement `json:"statements"`
}

// IAMAttachment is a policy attached to a member or a team.
type IAMAttachment struct {
	PolicyID      string     `json:"policy_id"`
	PrincipalType string     `json:"principal_type"`
	PrincipalID   string     `json:"principal_id"`
	AttachedAt    *time.Time `json:"attached_at"`
}

// attachRequest is the body of an attach.
type attachRequest struct {
	PrincipalType string `json:"principal_type"`
	PrincipalID   string `json:"principal_id"`
}

// IAMAction is one entry of the action catalogue.
type IAMAction struct {
	Action        string   `json:"action"`
	Service       string   `json:"service"`
	Description   string   `json:"description"`
	ResourceTypes []string `json:"resource_types"`
	Read          bool     `json:"read"`
}

// AccessSettings is the organization's access setting.
type AccessSettings struct {
	MemberBaseline string     `json:"member_baseline"`
	Version        int64      `json:"version"`
	UpdatedAt      *time.Time `json:"updated_at"`
}

// setAccessSettingsRequest is the body of PUT /iam/settings.
type setAccessSettingsRequest struct {
	MemberBaseline string `json:"member_baseline"`
}

// ListIAMActions returns the action catalogue.
func (c *Client) ListIAMActions(ctx context.Context) ([]IAMAction, error) {
	actions, err := listAll[IAMAction](ctx, c, "/iam/actions")
	if err != nil {
		return nil, fmt.Errorf("listing iam actions: %w", err)
	}
	return actions, nil
}

// GetAccessSettings returns the organization's access setting.
func (c *Client) GetAccessSettings(ctx context.Context) (*AccessSettings, error) {
	var s AccessSettings
	if err := c.do(ctx, http.MethodGet, "/iam/settings", nil, &s); err != nil {
		return nil, fmt.Errorf("getting access settings: %w", err)
	}
	return &s, nil
}

// SetAccessSettings changes the member baseline. Owners only.
func (c *Client) SetAccessSettings(ctx context.Context, memberBaseline string) (*AccessSettings, error) {
	var s AccessSettings
	if err := c.do(ctx, http.MethodPut, "/iam/settings", setAccessSettingsRequest{MemberBaseline: memberBaseline}, &s, replaySafe()); err != nil {
		return nil, fmt.Errorf("setting access settings: %w", err)
	}
	return &s, nil
}

// CreateIAMTeam adds a team.
func (c *Client) CreateIAMTeam(ctx context.Context, req CreateIAMTeamRequest) (*IAMTeam, error) {
	var t IAMTeam
	if err := c.do(ctx, http.MethodPost, "/iam/teams", req, &t, replaySafe()); err != nil {
		return nil, fmt.Errorf("creating team: %w", err)
	}
	return &t, nil
}

// GetIAMTeam returns one team, or ErrNotFound.
func (c *Client) GetIAMTeam(ctx context.Context, id string) (*IAMTeam, error) {
	var t IAMTeam
	if err := c.do(ctx, http.MethodGet, teamPath(id), nil, &t); err != nil {
		return nil, fmt.Errorf("getting team %s: %w", id, err)
	}
	return &t, nil
}

// ListIAMTeams returns every team.
func (c *Client) ListIAMTeams(ctx context.Context) ([]IAMTeam, error) {
	teams, err := listAll[IAMTeam](ctx, c, "/iam/teams")
	if err != nil {
		return nil, fmt.Errorf("listing teams: %w", err)
	}
	return teams, nil
}

// UpdateIAMTeam changes a team's name or description.
func (c *Client) UpdateIAMTeam(ctx context.Context, id string, req UpdateIAMTeamRequest) (*IAMTeam, error) {
	var t IAMTeam
	if err := c.do(ctx, http.MethodPatch, teamPath(id), req, &t, replaySafe()); err != nil {
		return nil, fmt.Errorf("updating team %s: %w", id, err)
	}
	return &t, nil
}

// DeleteIAMTeam removes a team. ErrNotFound means it is already gone.
func (c *Client) DeleteIAMTeam(ctx context.Context, id string) error {
	if err := c.do(ctx, http.MethodDelete, teamPath(id), nil, nil, replaySafe()); err != nil {
		return fmt.Errorf("deleting team %s: %w", id, err)
	}
	return nil
}

// ListIAMTeamMembers returns a team's members, or ErrNotFound for an unknown
// team.
func (c *Client) ListIAMTeamMembers(ctx context.Context, teamID string) ([]IAMTeamMember, error) {
	members, err := listAll[IAMTeamMember](ctx, c, teamPath(teamID)+"/members")
	if err != nil {
		return nil, fmt.Errorf("listing members of team %s: %w", teamID, err)
	}
	return members, nil
}

// AddIAMTeamMember puts a member in a team. The PUT has no body and adding a
// member twice leaves the team the same.
func (c *Client) AddIAMTeamMember(ctx context.Context, teamID, userID string) (*IAMTeamMember, error) {
	var m IAMTeamMember
	if err := c.do(ctx, http.MethodPut, teamMemberPath(teamID, userID), nil, &m, replaySafe()); err != nil {
		return nil, fmt.Errorf("adding %s to team %s: %w", userID, teamID, err)
	}
	return &m, nil
}

// RemoveIAMTeamMember takes a member out of a team. ErrNotFound means it is
// already out.
func (c *Client) RemoveIAMTeamMember(ctx context.Context, teamID, userID string) error {
	if err := c.do(ctx, http.MethodDelete, teamMemberPath(teamID, userID), nil, nil, replaySafe()); err != nil {
		return fmt.Errorf("removing %s from team %s: %w", userID, teamID, err)
	}
	return nil
}

// ListIAMPolicies returns policies: managed nil lists both kinds, true only
// Pantech's, false only the organization's.
func (c *Client) ListIAMPolicies(ctx context.Context, managed *bool) ([]IAMPolicy, error) {
	path := "/iam/policies"
	if managed != nil {
		path += fmt.Sprintf("?managed=%t", *managed)
	}
	policies, err := listAll[IAMPolicy](ctx, c, path)
	if err != nil {
		return nil, fmt.Errorf("listing policies: %w", err)
	}
	return policies, nil
}

// CreateIAMPolicy adds a policy.
func (c *Client) CreateIAMPolicy(ctx context.Context, req IAMPolicyRequest) (*IAMPolicy, error) {
	var p IAMPolicy
	if err := c.do(ctx, http.MethodPost, "/iam/policies", req, &p, replaySafe()); err != nil {
		return nil, fmt.Errorf("creating policy: %w", err)
	}
	return &p, nil
}

// GetIAMPolicy returns one policy, managed ones included, or ErrNotFound.
func (c *Client) GetIAMPolicy(ctx context.Context, id string) (*IAMPolicy, error) {
	var p IAMPolicy
	if err := c.do(ctx, http.MethodGet, policyPath(id), nil, &p); err != nil {
		return nil, fmt.Errorf("getting policy %s: %w", id, err)
	}
	return &p, nil
}

// UpdateIAMPolicy replaces a policy, only if it is still at version (sent as
// If-Match); otherwise the backend answers 412 VERSION_CONFLICT.
func (c *Client) UpdateIAMPolicy(ctx context.Context, id string, version int64, req IAMPolicyRequest) (*IAMPolicy, error) {
	var p IAMPolicy
	if err := c.do(ctx, http.MethodPut, policyPath(id), req, &p, replaySafe(), ifMatch(version)); err != nil {
		return nil, fmt.Errorf("updating policy %s: %w", id, err)
	}
	return &p, nil
}

// DeleteIAMPolicy removes a policy and detaches it everywhere. ErrNotFound
// means it is already gone.
func (c *Client) DeleteIAMPolicy(ctx context.Context, id string) error {
	if err := c.do(ctx, http.MethodDelete, policyPath(id), nil, nil, replaySafe()); err != nil {
		return fmt.Errorf("deleting policy %s: %w", id, err)
	}
	return nil
}

// ListIAMPolicyAttachments returns where a policy is attached.
func (c *Client) ListIAMPolicyAttachments(ctx context.Context, policyID string) ([]IAMAttachment, error) {
	attachments, err := listAll[IAMAttachment](ctx, c, policyPath(policyID)+"/attachments")
	if err != nil {
		return nil, fmt.Errorf("listing attachments of policy %s: %w", policyID, err)
	}
	return attachments, nil
}

// AttachIAMPolicy attaches a policy to a member or a team.
func (c *Client) AttachIAMPolicy(ctx context.Context, policyID, principalType, principalID string) (*IAMAttachment, error) {
	var a IAMAttachment
	body := attachRequest{PrincipalType: principalType, PrincipalID: principalID}
	if err := c.do(ctx, http.MethodPost, policyPath(policyID)+"/attachments", body, &a, replaySafe()); err != nil {
		return nil, fmt.Errorf("attaching policy %s to %s %s: %w", policyID, principalType, principalID, err)
	}
	return &a, nil
}

// DetachIAMPolicy detaches a policy. ErrNotFound means it was not attached.
func (c *Client) DetachIAMPolicy(ctx context.Context, policyID, principalType, principalID string) error {
	p := policyPath(policyID) + "/attachments/" + url.PathEscape(principalType) + "/" + url.PathEscape(principalID)
	if err := c.do(ctx, http.MethodDelete, p, nil, nil, replaySafe()); err != nil {
		return fmt.Errorf("detaching policy %s from %s %s: %w", policyID, principalType, principalID, err)
	}
	return nil
}

func teamPath(id string) string { return "/iam/teams/" + url.PathEscape(id) }

func teamMemberPath(teamID, userID string) string {
	return teamPath(teamID) + "/members/" + url.PathEscape(userID)
}

func policyPath(id string) string { return "/iam/policies/" + url.PathEscape(id) }
