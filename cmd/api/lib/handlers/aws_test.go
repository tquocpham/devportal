package handlers

import (
	"encoding/json"
	"testing"
)

type policyDoc struct {
	Version   string
	Statement []policyStatement
}

type policyStatement struct {
	Sid      string
	Effect   string
	Action   any // string or []string in the real documents
	Resource string
}

func actions(t *testing.T, a any) []string {
	t.Helper()
	switch v := a.(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, len(v))
		for i, s := range v {
			out[i] = s.(string)
		}
		return out
	default:
		t.Fatalf("unexpected Action type %T", a)
		return nil
	}
}

func TestContributorPolicyDocument(t *testing.T) {
	raw, err := contributorPolicyDocument("my-bucket")
	if err != nil {
		t.Fatalf("contributorPolicyDocument: %v", err)
	}

	var doc policyDoc
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, raw)
	}
	if doc.Version != "2012-10-17" {
		t.Errorf("Version = %q, want 2012-10-17", doc.Version)
	}
	if len(doc.Statement) != 2 {
		t.Fatalf("got %d statements, want 2", len(doc.Statement))
	}

	obj, list := doc.Statement[0], doc.Statement[1]

	if obj.Resource != "arn:aws:s3:::my-bucket/*" {
		t.Errorf("object statement Resource = %q, want arn:aws:s3:::my-bucket/*", obj.Resource)
	}
	wantObjActions := map[string]bool{"s3:GetObject": true, "s3:PutObject": true}
	for _, a := range actions(t, obj.Action) {
		if !wantObjActions[a] {
			t.Errorf("unexpected object action %q", a)
		}
		delete(wantObjActions, a)
	}
	if len(wantObjActions) > 0 {
		t.Errorf("missing object actions: %v", wantObjActions)
	}

	if list.Resource != "arn:aws:s3:::my-bucket" {
		t.Errorf("list statement Resource = %q, want arn:aws:s3:::my-bucket (no trailing /*)", list.Resource)
	}
	if got := actions(t, list.Action); len(got) != 1 || got[0] != "s3:ListBucket" {
		t.Errorf("list statement Action = %v, want [s3:ListBucket]", got)
	}
}

func TestSelfManagePolicyDocument(t *testing.T) {
	raw, err := selfManagePolicyDocument("123456789012")
	if err != nil {
		t.Fatalf("selfManagePolicyDocument: %v", err)
	}

	var doc policyDoc
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, raw)
	}
	if len(doc.Statement) != 2 {
		t.Fatalf("got %d statements, want 2", len(doc.Statement))
	}

	wantResource := "arn:aws:iam::123456789012:user/${aws:username}"
	for _, s := range doc.Statement {
		if s.Resource != wantResource {
			t.Errorf("statement %q Resource = %q, want %q (must scope to the caller's own user, not a fixed one)", s.Sid, s.Resource, wantResource)
		}
	}

	keysStmt, loginStmt := doc.Statement[0], doc.Statement[1]

	wantKeyActions := map[string]bool{
		"iam:CreateAccessKey": true, "iam:DeleteAccessKey": true, "iam:ListAccessKeys": true,
		"iam:UpdateAccessKey": true, "iam:GetAccessKeyLastUsed": true,
	}
	for _, a := range actions(t, keysStmt.Action) {
		if !wantKeyActions[a] {
			t.Errorf("unexpected access-key action %q", a)
		}
		delete(wantKeyActions, a)
	}
	if len(wantKeyActions) > 0 {
		t.Errorf("missing access-key actions: %v", wantKeyActions)
	}
	// Deliberately must NOT be able to touch login profiles via the
	// access-key statement, or vice versa. Each statement should be
	// narrowly scoped to its own concern.
	for _, a := range actions(t, keysStmt.Action) {
		if a == "iam:CreateLoginProfile" || a == "iam:UpdateLoginProfile" {
			t.Errorf("access-key statement must not grant login-profile actions, got %q", a)
		}
	}

	wantLoginActions := map[string]bool{
		"iam:ChangePassword": true, "iam:GetLoginProfile": true, "iam:UpdateLoginProfile": true,
	}
	for _, a := range actions(t, loginStmt.Action) {
		if !wantLoginActions[a] {
			t.Errorf("unexpected login-profile action %q", a)
		}
		delete(wantLoginActions, a)
	}
	if len(wantLoginActions) > 0 {
		t.Errorf("missing login-profile actions: %v", wantLoginActions)
	}
	// This policy must never grant CreateLoginProfile. That's the app's
	// own provisioner-only capability; a self-manage user creating their
	// OWN login profile would let them re-arm a login they were never
	// meant to have.
	for _, a := range actions(t, loginStmt.Action) {
		if a == "iam:CreateLoginProfile" {
			t.Error("self-manage policy must not grant iam:CreateLoginProfile")
		}
	}
}
