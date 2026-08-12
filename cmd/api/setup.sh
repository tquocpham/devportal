#!/usr/bin/env bash
set -euo pipefail

# Usage: BUCKET=your-lfs-bucket ./setup.sh
#    or: ./setup.sh your-lfs-bucket
#
# Everything else is derived or defaulted, nothing account-specific to
# hand-edit: ACCOUNT_ID comes from whichever AWS credentials/profile are
# active, PROVISIONER_USER/ROLE_NAME/the two policy names fall back to this
# project's naming convention but can be overridden via environment
# variable, e.g. for a second game/account sharing the same AWS org.
BUCKET="${BUCKET:-${1:-}}"
if [[ -z "$BUCKET" ]]; then
  echo "Usage: BUCKET=your-lfs-bucket $0   (or: $0 your-lfs-bucket)" >&2
  exit 1
fi

ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)
PROVISIONER_USER="${PROVISIONER_USER:-devportal-aws-provisioner}"
ROLE_NAME="${ROLE_NAME:-lfs-s3-contributor-sts}"
PROVISIONER_POLICY_NAME="${PROVISIONER_POLICY_NAME:-devportal-provisioner-policy}"
CONTRIBUTOR_POLICY_NAME="${CONTRIBUTOR_POLICY_NAME:-lfs-s3-vengeance-contributor}"

echo "Account:            $ACCOUNT_ID"
echo "Bucket:              $BUCKET"
echo "Provisioner user:    $PROVISIONER_USER"
echo "Role:                $ROLE_NAME"
echo "Provisioner policy:  $PROVISIONER_POLICY_NAME"
echo "Contributor policy:  $CONTRIBUTOR_POLICY_NAME"
echo

if ! aws iam get-user --user-name "$PROVISIONER_USER" &>/dev/null; then
  aws iam create-user --user-name "$PROVISIONER_USER"
  echo "Waiting for IAM user to propagate..."
  sleep 15
else
  echo "User $PROVISIONER_USER already exists, skipping..."
fi

# 0. The provisioner's own permissions: no iam:CreateRole/AttachRolePolicy/
#    PutRolePolicy (self-escalation primitive), everything else scoped to
#    exactly what cmd/api's handlers call. Written unconditionally (unlike
#    the lookup-or-create policies below) so that re-running this script
#    after adding a new permission here actually pushes it to AWS, instead
#    of silently reusing the stale version already attached.
cat > provisioner-policy.json <<POLICY
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "ManageContributorUsers",
      "Effect": "Allow",
      "Action": [
        "iam:CreateUser",
        "iam:GetUser",
        "iam:CreatePolicy",
        "iam:GetPolicy",
        "iam:AttachUserPolicy",
        "iam:CreateLoginProfile",
        "iam:GetLoginProfile",
        "iam:DeleteLoginProfile",
        "iam:CreateAccessKey",
        "iam:ListAccessKeys",
        "iam:DeleteAccessKey"
      ],
      "Resource": "*"
    },
    {
      "Sid": "AssumeContributorRole",
      "Effect": "Allow",
      "Action": "sts:AssumeRole",
      "Resource": "arn:aws:iam::${ACCOUNT_ID}:role/${ROLE_NAME}"
    },
    {
      "Sid": "IdentityCheck",
      "Effect": "Allow",
      "Action": "sts:GetCallerIdentity",
      "Resource": "*"
    }
  ]
}
POLICY

PROVISIONER_POLICY_ARN=$(aws iam list-policies --scope Local \
  --query "Policies[?PolicyName=='${PROVISIONER_POLICY_NAME}'].Arn" --output text)

if [[ -z "$PROVISIONER_POLICY_ARN" ]]; then
  echo "Creating $PROVISIONER_POLICY_NAME..."
  PROVISIONER_POLICY_ARN=$(aws iam create-policy \
    --policy-name "$PROVISIONER_POLICY_NAME" \
    --policy-document file://provisioner-policy.json \
    --query "Policy.Arn" --output text)
else
  echo "$PROVISIONER_POLICY_NAME already exists, pushing the current policy document as a new default version..."
  # IAM caps policies at 5 versions; prune the oldest non-default one first
  # if we're already at the cap, so create-policy-version below never fails
  # just because this script has been re-run a few times.
  VERSION_COUNT=$(aws iam list-policy-versions --policy-arn "$PROVISIONER_POLICY_ARN" \
    --query 'length(Versions)' --output text)
  if [[ "$VERSION_COUNT" -ge 5 ]]; then
    OLDEST_VERSION=$(aws iam list-policy-versions --policy-arn "$PROVISIONER_POLICY_ARN" \
      --query 'Versions[?IsDefaultVersion==`false`] | sort_by(@, &CreateDate)[0].VersionId' --output text)
    echo "At the 5-version cap, deleting oldest version $OLDEST_VERSION..."
    aws iam delete-policy-version --policy-arn "$PROVISIONER_POLICY_ARN" --version-id "$OLDEST_VERSION"
  fi
  aws iam create-policy-version \
    --policy-arn "$PROVISIONER_POLICY_ARN" \
    --policy-document file://provisioner-policy.json \
    --set-as-default
fi

aws iam attach-user-policy \
  --user-name "$PROVISIONER_USER" \
  --policy-arn "$PROVISIONER_POLICY_ARN"

# Only issue an access key if this user doesn't already have one; the
# SecretAccessKey is shown exactly once, so re-running this script must
# never silently mint (and orphan) a second one.
EXISTING_KEY=$(aws iam list-access-keys --user-name "$PROVISIONER_USER" \
  --query 'AccessKeyMetadata[0].AccessKeyId' --output text)

if [[ -z "$EXISTING_KEY" || "$EXISTING_KEY" == "None" ]]; then
  echo "Creating access key for $PROVISIONER_USER..."
  echo "Copy these into config.local.yaml as aws_access_key_id/aws_secret_access_key now; the secret is shown once."
  aws iam create-access-key --user-name "$PROVISIONER_USER"
else
  echo "$PROVISIONER_USER already has access key $EXISTING_KEY, skipping (delete it first via the AWS console/CLI if you need a new one)."
fi

# 1. Trust policy: only the provisioner user can assume this role
cat > trust-policy.json <<POLICY
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": { "AWS": "arn:aws:iam::${ACCOUNT_ID}:user/${PROVISIONER_USER}" },
      "Action": "sts:AssumeRole"
    }
  ]
}
POLICY

# 2. Create the role if it doesn't already exist. 43200 = 12 hours, the
#    ceiling aws_sts_max_session_duration_seconds can never exceed.
if aws iam get-role --role-name "$ROLE_NAME" &>/dev/null; then
  echo "Role $ROLE_NAME already exists, skipping creation..."
else
  aws iam create-role \
    --role-name "$ROLE_NAME" \
    --assume-role-policy-document file://trust-policy.json \
    --max-session-duration 43200
fi

# 3. Look up the contributor policy; only create it if it doesn't already
#    exist (e.g. from a previous Flow A1/A2 run), then attach it to the role
#    either way. Same lookup-or-create idiom as NEW_LFS_USER.sh and
#    AWSHandler.lookupOrCreatePolicy. Replaces the old 3a/3b split, which
#    ran both unconditionally and would either error loudly (policy not
#    found yet) or fail on create-policy (policy already exists).
POLICY_ARN=$(aws iam list-policies --scope Local \
  --query "Policies[?PolicyName=='${CONTRIBUTOR_POLICY_NAME}'].Arn" --output text)

if [[ -z "$POLICY_ARN" ]]; then
  echo "Creating $CONTRIBUTOR_POLICY_NAME..."
  cat > contributor-policy.json <<POLICY
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "BucketObjectAccess",
      "Effect": "Allow",
      "Action": ["s3:GetObject", "s3:PutObject"],
      "Resource": "arn:aws:s3:::${BUCKET}/*"
    },
    {
      "Sid": "BucketListAccess",
      "Effect": "Allow",
      "Action": "s3:ListBucket",
      "Resource": "arn:aws:s3:::${BUCKET}"
    }
  ]
}
POLICY
  POLICY_ARN=$(aws iam create-policy \
    --policy-name "$CONTRIBUTOR_POLICY_NAME" \
    --policy-document file://contributor-policy.json \
    --query "Policy.Arn" --output text)
else
  echo "$CONTRIBUTOR_POLICY_NAME already exists, reusing it..."
fi

aws iam attach-role-policy \
  --role-name "$ROLE_NAME" \
  --policy-arn "$POLICY_ARN"

# 4. This is what goes in aws_sts_role_arn in config.local.yaml
echo "Role ARN:"
aws iam get-role --role-name "$ROLE_NAME" --query 'Role.Arn' --output text
