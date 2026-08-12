# AWS one-time setup

Required once per AWS account before any of `cmd/api`'s AWS self-service flows work (docs/phase-3-aws-access-plan.md). Nothing here can be done or verified from this repo; it's manual AWS Console/CLI work, done once by whoever administers the account.

## 1. S3 bucket

The LFS bucket already exists, or create it now. Note its name for `aws_lfs_bucket` in `config.local.yaml`.

## 2. Provisioner IAM user

This is the identity `cmd/api` itself authenticates as. Create an IAM user (e.g. `devportal-aws-provisioner`) with an attached policy granting exactly:

```json
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
      "Resource": "arn:aws:iam::<account-id>:role/lfs-s3-contributor-sts"
    },
    {
      "Sid": "IdentityCheck",
      "Effect": "Allow",
      "Action": "sts:GetCallerIdentity",
      "Resource": "*"
    }
  ]
}
```

Deliberately **no** `iam:CreateRole`, `iam:AttachRolePolicy`, or `iam:PutRolePolicy`: a credential that can create a role and attach any policy to it can hand itself `AdministratorAccess` and assume it. Everything this policy does grant (`CreateUser`, `AttachUserPolicy`, `CreateAccessKey`, etc.) can only work with permissions the provisioner's own policy already lists; none of it can invent new ones.

Generate an access key for this user. It goes in `config.local.yaml` as `aws_access_key_id`/`aws_secret_access_key`, gitignored the same as every other secret in that file.

**Optional hardening**: attach an IAM permissions boundary to this user, and/or scope the `Resource` on the first statement to a username-prefix pattern (e.g. `arn:aws:iam::<account-id>:user/gh-*`) if you set `aws_iam_username_prefix` in config, so the provisioner literally cannot touch IAM users outside the ones it manages.

## 3. The two IAM policies

`lfs-s3-vengeance-contributor` and `lfs-s3-self-manage-credentials` can be pre-created by hand with the JSON below, or left for the app to create idempotently on first use (`AWSHandler.ensureUser` in `cmd/api/lib/handlers/aws.go`); either works, since it's a lookup-or-create either way.

```json
// lfs-s3-vengeance-contributor
{"Version":"2012-10-17","Statement":[
  {"Sid":"BucketObjectAccess","Effect":"Allow","Action":["s3:GetObject","s3:PutObject"],"Resource":"arn:aws:s3:::<bucket>/*"},
  {"Sid":"BucketListAccess","Effect":"Allow","Action":"s3:ListBucket","Resource":"arn:aws:s3:::<bucket>"}
]}
// lfs-s3-self-manage-credentials
{"Version":"2012-10-17","Statement":[
  {"Sid":"ManageOwnAccessKeys","Effect":"Allow","Action":["iam:CreateAccessKey","iam:DeleteAccessKey","iam:ListAccessKeys","iam:UpdateAccessKey","iam:GetAccessKeyLastUsed"],"Resource":"arn:aws:iam::<account-id>:user/${aws:username}"},
  {"Sid":"ManageOwnLoginProfile","Effect":"Allow","Action":["iam:ChangePassword","iam:GetLoginProfile","iam:UpdateLoginProfile"],"Resource":"arn:aws:iam::<account-id>:user/${aws:username}"}
]}
```

## 4. STS role (for Flow B: temporary credentials)

Create a role (e.g. `lfs-s3-contributor-sts`):

- **Trust policy**: `Principal` is the provisioner IAM user's ARN from step 2, `Action: sts:AssumeRole`.
- **Permissions policy**: same bucket-scoped S3 actions as `lfs-s3-vengeance-contributor`; you can literally attach that managed policy to the role instead of writing a new one.
- **`MaxSessionDuration`**: set to whatever ceiling you want developers to be able to request (e.g. 12 hours). `aws_sts_max_session_duration_seconds` in config must stay at or below this value; the app clamps requests to it either way.

Record the role's ARN into `aws_sts_role_arn`.

## 5. Verify

Confirm the provisioner user can call `sts:GetCallerIdentity`. `cmd/api` calls this once at startup to derive the AWS account ID, and fatals immediately if it fails, so this is a fast way to sanity-check the whole setup before running the app.

## 6. Retire the old script

Once Flow A1 (`POST /api/v1/aws/lfs-access-key`) and Flow A2 (`POST /api/v1/aws/console-access`) are verified to produce the same shape of IAM user as `NEW_LFS_USER.sh` did, that script is no longer needed.
