# Migrate AWS setup from setup.sh to Terraform

## Context

`cmd/api/setup.sh` provisions the whole AWS side of this app by hand: the provisioner IAM user + its policy, one STS role (`lfs-s3-contributor-sts`) + trust policy + permissions policy. It works, but every new resource is a copy-pasted bash block with its own manual lookup-or-create idempotency check (see the 5-version-cap pruning logic for the provisioner policy, or the `if aws iam get-role ... else create` pattern for the role); that scales fine to one role, badly to several.

**The actual trigger**: the plan to let "Get Temporary AWS Credentials" offer multiple purpose-specific roles (separate plan, [`docs/aws-multi-role-sts-plan.md`](aws-multi-role-sts-plan.md)) means going from one STS role to several, each needing its own trust policy + permissions policy + provisioner-side `sts:AssumeRole` grant. That's exactly the point where declarative state tracking and a reusable module start paying for their setup cost, a handful of stable one-time resources didn't justify it, several growing over time does.

**Terraform over CDK**: no complex conditional logic here that would benefit from a real programming language, just parameterized IAM resources (a role + two policies, repeated per purpose). CDK would also drag in a second language/toolchain (TypeScript or Python, npm/pip) alongside this project's existing Go stack for a case that doesn't need that flexibility. Terraform's AWS provider is mature and this is about as standard a use case as it gets.

## Approach

**Scope, deliberately not everything**:
- **In scope**: the provisioner IAM user + its policy, the S3 contributor policy, and (the actual point) an `sts-role` module instantiated once per purpose-specific role.
- **Explicitly out of scope, stays manual**: the provisioner's own access key. Terraform *can* create an `aws_iam_access_key` resource, but the secret would then live in Terraform state, a live AWS credential sitting in a state file is a real secret-management problem this project has been careful to avoid everywhere else (`config.local.yaml` gitignored, the config-secrets-layering plan, etc.). Keep minting that key a manual one-time step exactly like today, Terraform manages the *user*, not its credential.
- **Also out of scope**: the S3 bucket itself. It already exists and holds real production LFS data; pulling an existing bucket under Terraform management retroactively via `terraform import` is fine, but only with `lifecycle { prevent_destroy = true }` set from the very first apply, accidentally removing the resource block should never be able to delete real data. Worth doing deliberately later, not bundled into this migration.

**Reusable module, the actual reason for this migration**:
```hcl
module "sts_lfs_contributor" {
  source                  = "./modules/sts-role"
  role_name                = "lfs-s3-contributor-sts"
  provisioner_user_arn     = aws_iam_user.provisioner.arn
  max_session_duration     = 43200
  permissions_policy_json  = data.aws_iam_policy_document.lfs_s3_contributor.json
}
```
Each new purpose (the motivating case from the multi-role plan) becomes one more `module` block + one more `aws_iam_policy_document`, not a new bash function with its own hand-written idempotency logic.

**Provisioner's own `sts:AssumeRole` grant needs to change shape**: today it's scoped to exactly one role ARN (`docs/aws-one-time-setup.md`'s `AssumeContributorRole` statement). With multiple roles, this becomes a list of exact ARNs, one per module instantiated, grown deliberately as roles are added. Recommend against a wildcard pattern (e.g. `role/*-sts`), even though the naming convention would support it, an explicit list keeps the provisioner from ever being able to assume some future unrelated role that happens to match the pattern.

**State backend, a real open decision, not resolved here**: local state is simplest and matches "start simple," but has no locking and lives only on whichever machine ran `terraform apply`. An S3 backend + DynamoDB lock table is the standard durable/collaborative answer, but is itself a bootstrapping problem, you can't manage the state bucket from the state it's storing. Recommend starting with local state given the team size today, and migrating later (`terraform init -migrate-state`) if collaboration needs grow, rather than building the S3+DynamoDB bootstrap now for a need that isn't concrete yet. Flagging this as a decision to revisit, not asserting it's settled.

**Migration path for resources that already exist in AWS**: `setup.sh` has already created the real provisioner user, policy, and role. This migration should `terraform import` each of them into state, not destroy and recreate, recreating would rotate the provisioner's access key (breaking every deployed `config.local.yaml` until updated) and briefly take down prod's AWS-dependent flows. Import commands needed, one per existing resource:
```bash
terraform import aws_iam_user.provisioner devportal-aws-provisioner
terraform import aws_iam_policy.provisioner_policy <provisioner-policy-arn>
terraform import module.sts_lfs_contributor.aws_iam_role.this lfs-s3-contributor-sts
# ...one per existing resource
```

## Files

- `terraform/main.tf`, `terraform/variables.tf`, `terraform/outputs.tf` (new)
- `terraform/modules/sts-role/` (new) — the reusable role module
- `cmd/api/setup.sh` — retired once the import is verified working, not deleted immediately, kept as a documented fallback until Terraform's been proven against the real account for at least one full cycle
- `docs/aws-one-time-setup.md` — rewritten to describe running `terraform apply` instead of `./setup.sh`, the manual-step list (access key creation, bucket) stays as-is

## Verification

- `terraform plan` immediately after the import shows **zero** changes, proof the imported state actually matches what `setup.sh` already created, not a subtly different resource that would get modified on first apply.
- Add a second, clearly-test-only role via a new module block, `terraform apply`, confirm it's created correctly and the provisioner's policy grew to include the new role's ARN in its `sts:AssumeRole` resource list.
- `terraform destroy` that one test role, confirm the provisioner policy shrinks back down and nothing else is touched.
- Confirm `cmd/api` still starts and the existing LFS/console/STS flows still work unchanged after the import, this migration should be invisible to the running app, it only changes how the AWS side is managed, not what exists.
