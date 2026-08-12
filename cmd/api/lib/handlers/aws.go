package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"

	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
)

// AWSConfig holds the tunables for AWS self-service (docs/phase-3-aws-access-plan.md).
type AWSConfig struct {
	AccountID             string
	Region                string
	Bucket                string
	ContributorPolicyName string
	SelfManagePolicyName  string
	STSRoleARN            string
	// UsernamePrefix optionally namespaces IAM usernames beyond the raw
	// GitHub login. Empty by default.
	UsernamePrefix string
}

func DefaultAWSConfig() AWSConfig {
	return AWSConfig{
		ContributorPolicyName: "lfs-s3-vengeance-contributor",
		SelfManagePolicyName:  "lfs-s3-self-manage-credentials",
	}
}

type AWSHandler struct {
	iam *iam.Client
	sts *sts.Client
	cfg AWSConfig
}

func NewAWSHandler(iamClient *iam.Client, stsClient *sts.Client, cfg AWSConfig) *AWSHandler {
	return &AWSHandler{iam: iamClient, sts: stsClient, cfg: cfg}
}

// iamIdentityPattern matches IAM's own allowed username charset (alphanumeric
// plus _+=,.@-, max 64), which is also within STS RoleSessionName's allowed
// set. GitHub usernames already fit this; this is a defensive
// belt-and-suspenders check before any AWS call, not a real mapping layer.
var iamIdentityPattern = regexp.MustCompile(`^[\w+=,.@-]{1,64}$`)

// iamUsername derives the target IAM identity from the session claims
// already validated by RequireAuth: the self-service flows always act on the
// caller's own AWS access.
func (h *AWSHandler) iamUsername(c echo.Context) (string, error) {
	claims, ok := c.Get("user").(jwt.MapClaims)
	if !ok {
		return "", echo.NewHTTPError(http.StatusUnauthorized, "not logged in")
	}
	sub, _ := claims["sub"].(string)
	return h.resolveUsername(sub)
}

// resolveUsername applies the same prefix and charset check as iamUsername,
// but for a caller-supplied username rather than the session's own claims:
// the admin flows act on someone else's AWS access, so the target has to
// come from the request instead.
func (h *AWSHandler) resolveUsername(raw string) (string, error) {
	username := h.cfg.UsernamePrefix + raw
	if !iamIdentityPattern.MatchString(username) {
		return "", echo.NewHTTPError(http.StatusBadRequest, "username doesn't fit IAM's allowed character set")
	}
	return username, nil
}

func (h *AWSHandler) policyARN(name string) string {
	return fmt.Sprintf("arn:aws:iam::%s:policy/%s", h.cfg.AccountID, name)
}

// contributorPolicyDocument is bucket-scoped read/write + list, nothing else.
func contributorPolicyDocument(bucket string) (string, error) {
	doc := map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{
			{
				"Sid":      "BucketObjectAccess",
				"Effect":   "Allow",
				"Action":   []string{"s3:GetObject", "s3:PutObject"},
				"Resource": fmt.Sprintf("arn:aws:s3:::%s/*", bucket),
			},
			{
				"Sid":      "BucketListAccess",
				"Effect":   "Allow",
				"Action":   "s3:ListBucket",
				"Resource": fmt.Sprintf("arn:aws:s3:::%s", bucket),
			},
		},
	}
	b, err := json.Marshal(doc)
	return string(b), err
}

// selfManagePolicyDocument lets a user manage ONLY their own access
// keys/login profile, via the ${aws:username} policy variable, never anyone
// else's.
func selfManagePolicyDocument(accountID string) (string, error) {
	userResource := fmt.Sprintf("arn:aws:iam::%s:user/${aws:username}", accountID)
	doc := map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{
			{
				"Sid":    "ManageOwnAccessKeys",
				"Effect": "Allow",
				"Action": []string{
					"iam:CreateAccessKey", "iam:DeleteAccessKey", "iam:ListAccessKeys",
					"iam:UpdateAccessKey", "iam:GetAccessKeyLastUsed",
				},
				"Resource": userResource,
			},
			{
				"Sid":      "ManageOwnLoginProfile",
				"Effect":   "Allow",
				"Action":   []string{"iam:ChangePassword", "iam:GetLoginProfile", "iam:UpdateLoginProfile"},
				"Resource": userResource,
			},
		},
	}
	b, err := json.Marshal(doc)
	return string(b), err
}

// isNoSuchEntity reports whether err is IAM's "doesn't exist yet" error,
// as opposed to a real failure.
func isNoSuchEntity(err error) bool {
	var notFound *iamtypes.NoSuchEntityException
	return errors.As(err, &notFound)
}

// lookupOrCreatePolicy returns the ARN of the named policy, creating it from
// document if it doesn't exist yet.
func (h *AWSHandler) lookupOrCreatePolicy(ctx context.Context, name, document string) (string, error) {
	arn := h.policyARN(name)
	if _, err := h.iam.GetPolicy(ctx, &iam.GetPolicyInput{PolicyArn: &arn}); err == nil {
		return arn, nil
	} else if !isNoSuchEntity(err) {
		return "", err
	}

	out, err := h.iam.CreatePolicy(ctx, &iam.CreatePolicyInput{
		PolicyName:     &name,
		PolicyDocument: &document,
	})
	if err != nil {
		return "", err
	}
	return *out.Policy.Arn, nil
}

// ensureUser makes sure the IAM user exists and has both bucket-scoped
// policies attached: the common setup Flow A1 (LFS key) and Flow A2 (console
// access) both need before doing their own thing. AttachUserPolicy is called
// unconditionally either way; AWS no-ops if it's already attached.
func (h *AWSHandler) ensureUser(ctx context.Context, username string) error {
	if _, err := h.iam.GetUser(ctx, &iam.GetUserInput{UserName: &username}); err != nil {
		if !isNoSuchEntity(err) {
			return err
		}
		if _, err := h.iam.CreateUser(ctx, &iam.CreateUserInput{UserName: &username}); err != nil {
			return err
		}
	}

	contributorDoc, err := contributorPolicyDocument(h.cfg.Bucket)
	if err != nil {
		return err
	}
	contributorARN, err := h.lookupOrCreatePolicy(ctx, h.cfg.ContributorPolicyName, contributorDoc)
	if err != nil {
		return err
	}

	selfManageDoc, err := selfManagePolicyDocument(h.cfg.AccountID)
	if err != nil {
		return err
	}
	selfManageARN, err := h.lookupOrCreatePolicy(ctx, h.cfg.SelfManagePolicyName, selfManageDoc)
	if err != nil {
		return err
	}

	if _, err := h.iam.AttachUserPolicy(ctx, &iam.AttachUserPolicyInput{UserName: &username, PolicyArn: &contributorARN}); err != nil {
		return err
	}
	if _, err := h.iam.AttachUserPolicy(ctx, &iam.AttachUserPolicyInput{UserName: &username, PolicyArn: &selfManageARN}); err != nil {
		return err
	}
	return nil
}
