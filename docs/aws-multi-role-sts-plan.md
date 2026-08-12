# Multi-role "Get Temporary AWS Credentials"

## Context

`POST /api/v1/aws/sts-credentials` (`aws_sts.go`) currently assumes exactly one hardcoded role, `h.cfg.STSRoleARN`, the caller can only choose *how long* the session lasts, not *what it's for*. The actual ask: let a user pick from several purpose-specific roles (LFS bucket ops today, more later, e.g. CloudWatch log viewing) depending on what they're actually trying to do. Each role this depends on is provisioned via the Terraform module from [`docs/aws-terraform-migration-plan.md`](aws-terraform-migration-plan.md), that doc covers *creating* the roles; this one covers the app knowing about them and letting a user pick.

## Approach

**Config shape changes from a single ARN to a named map**:
```yaml
aws_sts_roles:
  lfs-contributor:
    arn: arn:aws:iam::<account-id>:role/lfs-s3-contributor-sts
    label: "LFS bucket read/write"
  cloudwatch-viewer:
    arn: arn:aws:iam::<account-id>:role/cloudwatch-logs-viewer-sts
    label: "View CloudWatch logs"
```
`AWSConfig.STSRoles map[string]STSRoleOption{ARN, Label string}`, replacing the single `STSRoleARN` field. **Must verify at implementation time**: the exact viper call for unmarshaling a nested map like this (`viper.UnmarshalKey`, most likely, but confirm the exact shape against the installed viper version's source before writing this, same discipline as everywhere else in this project, not assumed here).

**Request shape**: `stsCredentialsRequest` gains `RoleKey string` alongside the existing `DurationSeconds`. The handler looks up `RoleKey` in the configured map; a key that doesn't match anything configured is a `400` naming the valid keys, not a silent fallback to some default role, picking the wrong role by accident is exactly the failure mode this feature should make impossible, not easy.

**New read endpoint**: `GET /api/v1/aws/sts-roles` (protected, no admin requirement, same as the other AWS self-service routes) returns the configured `{key, label}` pairs (ARNs aren't secret, could be included too, but the UI only needs the label to build a picker). This is what lets the frontend render real, current options instead of a hardcoded list that drifts from config.

**Authorization, deliberately not built yet**: today, *any* authenticated user can assume the one configured role, no differentiation. Recommend keeping that property, every configured role requestable by every authenticated user, until a concrete need for role-gating shows up (e.g. a future role that should be admin-only). An optional `RequiredRole users.Role` field per configured `STSRoleOption` would be the natural extension if that need arrives, not building it preemptively without a real case for it.

**UI**: the "Temporary credentials" section gets a role `<select>` next to the existing duration `<select>`, populated from `GET /api/v1/aws/sts-roles` on tab-show (same `if (name === "aws")`-style hook pattern already used for the admin tab's user list), not hardcoded `<option>`s in `index.html`, since the whole point is that roles can be added via Terraform without an app deploy. `awsStsBtn`'s click handler adds `roleKey: awsStsRoleSelect.value` to the POST body alongside the existing `durationSeconds`.

**Two-step process when adding a new role, not automated by this plan**: (1) add a `module` block in the Terraform config and apply it, producing a real role ARN; (2) copy that ARN into `aws_sts_roles` in `config.local.yaml`. These two steps are manually kept in sync for now, an automated version (Terraform `output` feeding a script that writes config) is a reasonable later improvement, not core to landing this feature.

## Files

- `cmd/api/lib/handlers/aws.go` — `AWSConfig.STSRoles map[string]STSRoleOption` replacing `STSRoleARN`
- `cmd/api/lib/handlers/aws_sts.go` — `RoleKey` on the request, lookup-or-400 logic, `STSRoles` (new handler method backing `GET /api/v1/aws/sts-roles`)
- `cmd/api/main.go` — load `aws_sts_roles` into `AWSConfig.STSRoles`, register the new `GET` route
- `cmd/api/config.local.example.yaml` — replace the single `aws_sts_role_arn` example with the new map shape
- `cmd/api/web/index.html` / `app.js` — role `<select>`, populated from the new endpoint, wired into the existing STS request

## Verification

- With two roles configured, request each by key, confirm the returned credentials can do what that specific role allows and *not* what the other role allows (e.g. the LFS role's temp creds can `aws s3 ls` the bucket but not view CloudWatch logs, and vice versa), proves the right role is actually being assumed, not just that *a* role is.
- Request a `roleKey` that isn't configured, confirm a clear `400` naming the valid options, not a generic error or a silent fallback.
- Omit `roleKey` entirely, confirm it's rejected the same way, no default-role fallback to accidentally rely on.
- Add a third role via the Terraform module, update config, confirm it shows up in the UI's picker without any other code change, the "adding a role is just config + Terraform" property actually holding.
