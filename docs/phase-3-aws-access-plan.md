# Phase 3: AWS self-service (LFS access key + console OTP + STS)

## Context

Phase 2 (retrieval-augmented chat, plus the search-loop upgrade) is done and working. This phase is split out ahead of branch-aware code review (now Phase 5) because you already have people who need AWS access today — this is the higher-priority half of what was originally scoped as one combined phase.

Today, onboarding a new contributor to AWS means you running `NEW_LFS_USER.sh` by hand: it looks up-or-creates two IAM policies (bucket-scoped S3 read/write, and a policy letting a user manage only their own access keys/login profile), creates the IAM user, attaches both policies, and generates a one-time console password. That gets replaced and extended with **three** self-service flows, not two — the original plan bundled "long-lived credentials" and "console browser access" into one flow, but they're genuinely different needs: git/LFS wants a long-lived key with zero friction (nobody wants to re-auth every `git pull`), devops work sometimes needs to actually browse the AWS console in a browser, and ad hoc ops work is better served by short-lived credentials. All three are triggerable by developers themselves from the web app, not routed through you.

This is genuinely new surface area for the app: first AWS SDK dependency in the repo, first handler that mutates state in a third-party system, first feature with a real "you must do this by hand in AWS first" precondition. All three flows authenticate off the already-issued session JWT — `c.Get("user").(jwt.MapClaims)["sub"]`, same as `handlers.Me` (`cmd/api/lib/handlers/me.go`) does today. No new login step, no new authorization layer: anyone who can log into the app already passed the org/allowlist gate in `auth.go`'s `Callback`, so `RequireAuth` alone is sufficient here too.

## Approach

**New dependency**: `github.com/aws/aws-sdk-go-v2` + `.../config` + `.../credentials` + `.../service/iam` + `.../service/sts`. Client construction happens once in `main.go`, same spot as `retrieval.NewStore`/`anthropic.NewClient` — `awsconfig.LoadDefaultConfig(ctx, ...)` with static creds from config, then `iam.NewFromConfig`/`sts.NewFromConfig`, plus one `stsClient.GetCallerIdentity` call at startup to derive the AWS account ID (no need to hand-enter it in config).

**New files**: `cmd/api/lib/handlers/aws.go` (shared `AWSHandler`/`AWSConfig`/`NewAWSHandler`, IAM-username derivation from claims, the two policy-document builders, `lookupOrCreatePolicy`/`lookupOrCreateUser` helpers), `aws_lfs_key.go` (Flow A1), `aws_console.go` (Flow A2), `aws_sts.go` (Flow B).

GitHub usernames (alphanumeric + hyphen, ≤39 chars) already fit both IAM's username charset and STS `RoleSessionName`'s charset (≤64 chars, same allowed set) — no mapping table needed, just a defensive regex check before any AWS call as a belt-and-suspenders guard.

**Flow A1 — `POST /api/v1/aws/lfs-access-key`** (no request body; identity from session). The direct-issue shortcut — no console visit, no self-serve step:
1. Derive `username` from `claims["sub"]`.
2. Ensure the IAM user exists and both policies (`lfs-s3-vengeance-contributor`, `lfs-s3-self-manage-credentials`) are attached — same lookup-or-create/attach logic as Flow A2 below, factored into a shared helper both call, since both flows need the user in the same state before doing their own thing.
3. `iam.ListAccessKeys` for the user first. **If an active key already exists, don't create another** — return `{alreadyExists: true, accessKeyId: "AKIA...", message: "..."}` pointing at the delete action below rather than creating a second key toward AWS's hard 2-key-per-user cap.
4. If none exists: `iam.CreateAccessKey`, return `{accessKeyId, secretAccessKey}` once — never logged, never persisted server-side. **Real change from the original design, worth stating plainly**: unlike Flow A2's temp password (which the developer's own browser session creates via self-service, invisible to this app), this secret genuinely passes through the app's backend in the response body. It's never written to a log, a database, or disk — but it does transit the process, which the original "the app never touches the secret" design deliberately avoided. This is an accepted tradeoff for removing the console round-trip from the everyday LFS-setup path, not an oversight.

**Flow A1b — `DELETE /api/v1/aws/lfs-access-key`** (no request body; identity from session) — self-service key deletion, so losing a key or wanting to rotate one doesn't require Flow A2 at all:
1. Derive `username` from `claims["sub"]`.
2. `iam.ListAccessKeys` for the user. If none exists, return a clear "you don't have a key to delete" response rather than erroring.
3. If one exists: `iam.DeleteAccessKey` for that key ID, return success. The developer can immediately call Flow A1 again to get a fresh one — delete-then-recreate is the rotation path.

This means Flow A2 (console access) is no longer the only recovery path for a lost LFS key — it stays relevant for actual browser/console needs (S3 browsing, CloudWatch), but a lost or rotated LFS key is now handled entirely within Flow A1/A1b, no console visit required.

**Flow A2 — `POST /api/v1/aws/console-access`** (no request body; identity from session; renamed from the original plan's `console-user` for clarity now that A1 exists). Kept for devops work that actually needs the AWS console UI in a browser — S3 browsing, CloudWatch, etc. Otherwise unchanged from the original design:
1. Derive `username` from `claims["sub"]`.
2. Same user/policy lookup-or-create as A1 (shared helper).
3. `iam.GetLoginProfile` first: **if a login profile already exists, don't touch it** — return `{alreadyExisted: true}` pointing at "forgot password" instead of silently rotating a password the developer may have already changed. If none exists, generate a random temp password (`crypto/rand`, same primitive as `randomState()` in `auth.go:191` but longer/richer charset) and `iam.CreateLoginProfile(..., PasswordResetRequired: true)`.
4. Return `{username, consoleSignInURL, temporaryPassword, passwordResetRequired, alreadyExisted}` once — never logged, never persisted. This flow keeps the original "app never sees the secret" property; only A1 gives that up, and only for the access-key case.

**Flow B — `POST /api/v1/aws/sts-credentials`** (optional `{durationSeconds}`) — unchanged from the original plan, this is the short-lived "ops stuff" path:
1. Derive `username`/session name from `claims["sub"]`.
2. Clamp `durationSeconds` to `[900, aws_sts_max_session_duration_seconds]` (900 = STS's own floor; ceiling defaults to 3600, must be ≤ the role's own `MaxSessionDuration` set during one-time setup below).
3. `sts.AssumeRole(RoleArn: aws_sts_role_arn, RoleSessionName: username, DurationSeconds: clamped)`.
4. Return `{accessKeyId, secretAccessKey, sessionToken, expiration}` straight from the response — nothing written server-side, no long-lived secret created, matches the Isengard-style ask directly.

**Why Flow B's role is one-time manual setup, not dynamic like the IAM policies in A1/A2:** creating an assumable role needs `iam:CreateRole` + `iam:AttachRolePolicy`/`iam:PutRolePolicy` on the app's own standing AWS credential — a well-known IAM privilege-escalation primitive (a credential that can create a role and attach any policy to it can hand itself `AdministratorAccess` and assume it). `CreateUser`/`AttachUserPolicy`/`CreateLoginProfile`/`CreateAccessKey` don't have that property — they can only grant capabilities the app's own policy already explicitly lists, never invent new ones. Keeping role creation manual keeps the app's own AWS permission set small and non-self-escalating, and there's only ever one such role needed here (bucket-scoped, essentially static) — the textbook case for "create once by hand, reference by ARN."

**Audit trail**: recommend structured logrus only for this phase, not a new Postgres table. `main.go`'s existing `middleware.RequestLoggerWithConfig` JSON logger already captures method/URI/status/latency for every request; worth a one-line addition to include `claims["sub"]` in `LogValuesFunc` when present (benefits every route). Add one explicit `logger.WithFields(...).Info(...)` per AWS handler call with flow-specific detail (policies/role touched, duration granted). AWS CloudTrail is already the authoritative audit log for every `CreateUser`/`AttachUserPolicy`/`AssumeRole` call regardless of what this app logs. A `aws_access_log` Postgres table (reusing the same instance as `code_chunks`, per our earlier conversation) is a reasonable fast-follow if a queryable "who has AWS access" view is ever actually wanted — not building it now since it wasn't asked for.

**Config keys** (`config.local.example.yaml` additions): `aws_region`, `aws_access_key_id`/`aws_secret_access_key` (the provisioner user's own key — see setup checklist), `aws_lfs_bucket`, `aws_sts_role_arn`, `aws_sts_max_session_duration_seconds` (default 3600); optional `aws_iam_username_prefix`, `aws_lfs_contributor_policy_name`, `aws_self_manage_policy_name` overrides.

## One-time AWS setup (outside this repo — required before either flow works)

New `docs/aws-one-time-setup.md`, reviewable independent of the code. No AWS credentials or tools are available in this environment, so none of this can be done or verified as part of implementation — it's on you, in order:

1. **S3 bucket** for LFS exists (or create it) — note its name for `aws_lfs_bucket`.
2. **Provisioner IAM user** (e.g. `devportal-aws-provisioner`) — the identity the Go backend authenticates as. Attach a policy granting exactly: `iam:CreateUser`, `iam:GetUser`, `iam:CreatePolicy`, `iam:GetPolicy`, `iam:AttachUserPolicy`, `iam:CreateLoginProfile`, `iam:GetLoginProfile`, `iam:CreateAccessKey`, `iam:ListAccessKeys`, `iam:DeleteAccessKey`, `sts:AssumeRole` (scoped to the one role ARN from step 4), `sts:GetCallerIdentity`. The last three IAM actions are new versus the original plan — needed for Flow A1/A1b to issue and delete long-lived keys directly. Deliberately **no** `iam:CreateRole`/`iam:AttachRolePolicy`/`iam:PutRolePolicy` — see the escalation argument above; none of `CreateAccessKey`/`ListAccessKeys`/`DeleteAccessKey` have that property — they only create, list, or revoke credentials for already-existing users with already-granted permissions, never grant new permissions. (`DeleteAccessKey` is if anything the safest of the three — its only failure mode is revoking access, never expanding it.) Generate an access key for this user; it goes in `config.local.yaml` as `aws_access_key_id`/`aws_secret_access_key`, gitignored same as every other secret in that file. Optional hardening: an IAM permissions boundary on this user, and/or scoping these three actions' resource to a username-prefix pattern if `aws_iam_username_prefix` is set, so the provisioner can't touch IAM users outside the ones it manages.
3. **The two IAM policies** — pre-create by hand with the JSON below, or leave them for Flow A1/A2 to create on first use; either works.
4. **STS role** (e.g. `lfs-s3-contributor-sts`): trust policy naming the provisioner user's ARN as `Principal`; permissions policy = same bucket-scoped S3 actions as the contributor policy (can literally attach that managed policy to the role); set `MaxSessionDuration` to whatever ceiling you want (e.g. 12h) — `aws_sts_max_session_duration_seconds` must stay ≤ this. Record the role ARN into `aws_sts_role_arn`.
5. Confirm the provisioner user can call `sts:GetCallerIdentity`.
6. Once Flow A1/A2 are verified to produce the same shape of IAM user as `NEW_LFS_USER.sh`, the script can be retired.

Policy JSON (double-check against the literal policy documents in `NEW_LFS_USER.sh` since that script isn't in this repo):
```json
// lfs-s3-vengeance-contributor
{"Version":"2012-10-17","Statement":[
  {"Sid":"BucketObjectAccess","Effect":"Allow","Action":["s3:GetObject","s3:PutObject"],"Resource":"arn:aws:s3:::<bucket>/*"},
  {"Sid":"BucketListAccess","Effect":"Allow","Action":"s3:ListBucket","Resource":"arn:aws:s3:::<bucket>"}
]}
// lfs-s3-self-manage-credentials
{"Version":"2012-10-17","Statement":[
  {"Sid":"ManageOwnAccessKeys","Effect":"Allow","Action":["iam:CreateAccessKey","iam:DeleteAccessKey","iam:ListAccessKeys","iam:UpdateAccessKey","iam:GetAccessKeyLastUsed"],"Resource":"arn:aws:iam::<accountID>:user/${aws:username}"},
  {"Sid":"ManageOwnLoginProfile","Effect":"Allow","Action":["iam:ChangePassword","iam:GetLoginProfile","iam:UpdateLoginProfile"],"Resource":"arn:aws:iam::<accountID>:user/${aws:username}"}
]}
```

## UI (`cmd/api/web/index.html`)

Already fully stubbed and wired — the AWS Access tab has all four actions hitting real routes that currently return `501`. This phase swaps the stub handlers for real `AWSHandler` methods; the UI itself needs no further changes. Existing conventions already in place: `.reveal-box` for one-time secret reveals, `copyableRow` for labeled copy-button fields, `apiErrorMessage` for surfacing `{"message": "..."}` errors.

- **"Get LFS Access Key"** → `POST /api/v1/aws/lfs-access-key`. Success: `copyableRow` for `accessKeyId`/`secretAccessKey`. `alreadyExists: true`: a message pointing at the delete button below rather than console access — deleting and recreating is now entirely self-contained in this tab.
- **"Delete LFS Access Key"** → `DELETE /api/v1/aws/lfs-access-key`, confirm-prompted (same `confirm()` pattern as the admin panel's remove-user button).
- **"Get AWS Console Access"** (route `/api/v1/aws/console-access`) → one-time reveal box (username/temp password/console URL), or the forgot-password message if `alreadyExisted`.
- **"Get Temporary AWS Credentials"** → `POST /api/v1/aws/sts-credentials` (optional duration input, clamped server-side regardless), shows AccessKeyId/SecretAccessKey/SessionToken/expiration with a ready-to-paste `export AWS_...` block and the expiration timestamp.

## Files

- `cmd/api/lib/handlers/aws.go`, `aws_lfs_key.go`, `aws_console.go`, `aws_sts.go` (new) — `AWSHandler`, Flow A1 (including A1b delete), Flow A2, Flow B
- `cmd/api/lib/handlers/aws_stub.go` — deleted, replaced by the real handlers
- `cmd/api/main.go` — new AWS config reads, construct `AWSHandler`, swap all four stub route registrations for the real handler methods (routes/methods themselves stay the same)
- `cmd/api/go.mod` — add `aws-sdk-go-v2` + `config`/`credentials`/`service/iam`/`service/sts`
- `cmd/api/config.local.example.yaml` — document all new AWS keys
- `cmd/api/web/index.html` — no changes needed, already built against these exact routes/response shapes
- `docs/aws-one-time-setup.md` (new) — the manual checklist above

## Verification

- `go build ./...` — works without real AWS credentials, since SDK client construction doesn't make network calls until a method is invoked.
- Unit tests for the two policy-document JSON builders — pure, credential-free, and the piece most likely to have a typo that only shows up against real AWS.
- Smoke test (needs real AWS credentials from the one-time setup — **not available in this environment, must be run by you**):
  - Call `/api/v1/aws/lfs-access-key` twice — first creates and returns a key pair, second detects the existing key and returns `alreadyExists: true` without creating a second one; verify in the AWS console that only one key exists and the attached policy is bucket-scoped.
  - Call `DELETE /api/v1/aws/lfs-access-key`, confirm it succeeds and the key is gone in the AWS console; call it again with no key present and confirm a clean "nothing to delete" response, not an error. Call `POST /api/v1/aws/lfs-access-key` again and confirm a fresh key issues successfully — the full create → delete → recreate loop working end to end without ever touching Flow A2.
  - Call `/api/v1/aws/console-access` twice — first creates and returns a password, second detects the existing login profile and returns `alreadyExisted: true` without rotating it.
  - Log into the console with that password, delete the access key from step 1, confirm `/api/v1/aws/lfs-access-key` now successfully issues a fresh one — proves the "recover via console" path actually works end to end.
  - Call `/api/v1/aws/sts-credentials`, confirm the returned expiration matches the clamped duration, and that the returned temporary credentials can run `aws s3 ls s3://<bucket>` but *not* e.g. `aws iam list-users` — proves the role is really bucket-scoped.
