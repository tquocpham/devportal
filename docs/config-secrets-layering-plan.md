# Layered config resolution: Secrets Manager → SSM → Redis → plain config

## Context

Every sensitive value this app needs (`jwt_secret`, `github_client_secret`, `anthropic_api_key`, `voyage_api_key`, `aws_secret_access_key`, `database_url`) currently lives in plaintext in `config.local.yaml` (or, per the fallback just added, `config.yaml`) on whatever machine runs `cmd/api`. That file is gitignored, but it's still a plaintext secret sitting on disk on the deployment machine, readable by anything with filesystem access there. The ask: let sensitive values optionally live in a real secret store instead, checked in priority order, falling back to today's plain-file behavior so nothing breaks for values that don't opt in.

**Resolution order per key, first hit wins**: AWS Secrets Manager → AWS SSM Parameter Store → Redis → the plain config/env value (today's `mustGet`/`get`, unchanged as the final fallback). This is an opt-in *lookup*, not a required migration: a key with no matching entry in any of the three stores just falls through to its existing plain value exactly as today, so this ships without forcing every key to move anywhere.

**Why this is a lookup chain and not a per-value prefix syntax** (e.g. `jwt_secret: "awssecret://name"`): a prefix scheme needs every value edited to opt in. A pure lookup-by-key-name chain needs nothing edited in `config.local.yaml` at all, if a Secrets Manager entry exists for `jwt_secret`, it's used automatically; if it doesn't, the plain config value (if any) is used exactly as today. Since this only runs once at process startup (not per-request), doing 2-3 extra API calls per key at boot costs nothing that matters, even for keys that end up resolving to a plain literal.

**Scope, deliberately not universal**: this does not apply to every config key, only ones that opt in by being read through a new `mustGetSecret`/`getSecret` pair instead of the existing `mustGet`/`get`. Non-sensitive keys (`port`, `callback_url`, `log_level`, etc.) stay on plain `mustGet`/`get`, unchanged, no reason to send those through three extra network round-trips at boot.

**Hard exception, not optional**: `aws_access_key_id`/`aws_secret_access_key` themselves must stay on plain `mustGet`, never `mustGetSecret`. The AWS client used to reach Secrets Manager/SSM needs *some* credential to authenticate with in the first place, using this app's own AWS-backed secret resolution to fetch the very credential that resolution depends on is circular. This is a real constraint, not a stylistic choice, worth a comment at the call site so nobody "fixes" it later without understanding why.

## Approach

**New AWS clients, reusing existing credentials**: `secretsmanager.NewFromConfig(awsCfg)` and `ssm.NewFromConfig(awsCfg)`, built from the *same* `awsCfg` (`awsconfig.LoadDefaultConfig`) `main.go` already constructs for the IAM/STS handlers, no new credential setup. **Structural requirement**: that construction currently happens mid-`main()` (around where `awsHandlerCfg` is built), well after early keys like `jwt_secret`/`anthropic_api_key` are already read via plain `mustGet`. For those early keys to benefit from Secrets Manager/SSM resolution, AWS client construction has to move to the very top of `main()`, immediately after `aws_region`/`aws_access_key_id`/`aws_secret_access_key` are read (which stay on plain `mustGet` per the exception above, so this isn't circular, just reordered).

**Verified against real SDK source, not assumed** (`aws-sdk-go-v2/service/secretsmanager@v1.44.5`, `.../service/ssm@v1.73.5`):
- `secretsmanager.Client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &name})` → `*GetSecretValueOutput`, value in `.SecretString *string`. A "not found" error is `types.ResourceNotFoundException`, must be checked via `errors.As` to distinguish "not configured, fall through" from "configured but something's actually wrong."
- `ssm.Client.GetParameter(ctx, &ssm.GetParameterInput{Name: &name, WithDecryption: aws.Bool(true)})` → `*GetParameterOutput`, value in `.Parameter.Value *string`. `WithDecryption` is required to get plaintext back for `SecureString` parameters, ignored for plain `String` ones, safe to always set true. Not-found error type needs the same `errors.As` verification at implementation time (not asserted here).

**Naming convention for the lookup key**, namespaced to avoid colliding with other apps in the same AWS account: `secrets_namespace` config value (default `devportal`), applied per backend using each store's own idiom, not one flat format forced onto all three:
- Secrets Manager: `{namespace}/{key}` → `devportal/jwt_secret`
- SSM: `/{namespace}/{key}` → `/devportal/jwt_secret` (SSM's own path convention)
- Redis: `{namespace}:{key}` → `devportal:jwt_secret` (Redis's own convention)

**New file `cmd/api/secrets.go`** (sibling to the existing `env.go`, same package):
```go
func mustGetSecret(ctx context.Context, key string) string {
    v, err := resolveSecret(ctx, key)
    if err != nil {
        log.Fatalf("resolving secret %q: %v", key, err)
    }
    if v == "" {
        log.Fatalf("required secret %q not found in Secrets Manager, SSM, Redis, or config", key)
    }
    return v
}

func getSecret(ctx context.Context, key, def string) string {
    v, err := resolveSecret(ctx, key)
    if err != nil {
        log.Fatalf("resolving secret %q: %v", key, err) // a real backend error, not "just missing," still worth failing loudly on
    }
    if v == "" {
        return def
    }
    return v
}

func resolveSecret(ctx context.Context, key string) (string, error) {
    if v, ok, err := lookupSecretsManager(ctx, key); err != nil || ok {
        return v, err
    }
    if v, ok, err := lookupSSM(ctx, key); err != nil || ok {
        return v, err
    }
    if v, ok, err := lookupRedis(ctx, key); err != nil || ok {
        return v, err
    }
    return viper.GetString(key), nil // today's behavior, unchanged, as the final fallback
}
```

Each `lookupX(ctx, key) (value string, found bool, err error)` returns `found=false, err=nil` when that backend simply has no entry for this key (expected, move to the next one), but a real non-nil `err` when the backend is configured and reachable yet the call itself failed (bad credentials, network issue, malformed response). That distinction matters: silently falling through on a *real* error would mask a genuine misconfiguration by quietly landing on a possibly-wrong plain-config value instead of failing loudly.

**Each backend is independently optional, not all-or-nothing**: `lookupSecretsManager`/`lookupSSM` simply aren't attempted (`found=false, err=nil` immediately) if AWS isn't configured at all (no `aws_region` set), same reasoning as why AWS client construction is already conditional today, this app must work with zero AWS setup for teams that only want chat/admin, per the existing "AWS is opt-in infrastructure" pattern already in this codebase. `lookupRedis` likewise no-ops if `redis_url` isn't set.

**Redis is the one piece this project doesn't have any infrastructure for yet**, worth being explicit about before building it: no Redis service in `docker-compose.yml`, no client dependency, nothing else in this app uses it. Adding it *purely* to be the third tier of a secret-lookup chain is a meaningfully bigger infra lift than Secrets Manager/SSM (which need zero new infrastructure, just reusing AWS credentials already required for the existing IAM/STS handlers). Recommend building Secrets Manager + SSM first, confirming they're actually useful, and treating Redis as a deferred addition, not because the idea is wrong, but because it's the one tier that isn't "free" given what's already running. If Redis already exists for another reason by the time this is prioritized, this reasoning doesn't apply, it's specifically about not standing up a new service for this alone.

**Which keys actually switch to `mustGetSecret`**: the sensitive ones, `jwt_secret`, `github_client_secret`, `anthropic_api_key`, `voyage_api_key`, `database_url`. Explicitly **not** `aws_access_key_id`/`aws_secret_access_key` (the hard exception above), and not the clearly-non-sensitive keys (`port`, `callback_url`, `log_level`, `github_org`).

## Files

- `cmd/api/go.mod` — add `aws-sdk-go-v2/service/secretsmanager`, `aws-sdk-go-v2/service/ssm`; Redis client deferred until that tier is actually built
- `cmd/api/secrets.go` (new) — `mustGetSecret`, `getSecret`, `resolveSecret`, `lookupSecretsManager`, `lookupSSM`, `lookupRedis` (stub returning `false, nil` until Redis is built)
- `cmd/api/main.go` — move AWS client construction (`awsconfig.LoadDefaultConfig`, the two new service clients) to the top of `main()`, ahead of the now-`mustGetSecret` calls that depend on it; switch the five sensitive keys listed above from `mustGet`/`get` to `mustGetSecret`/`getSecret`
- `cmd/api/config.local.example.yaml` — document `secrets_namespace` (optional, default `devportal`) and a comment explaining the lookup-chain behavior so it's discoverable without reading Go source

## Verification

- With nothing in Secrets Manager/SSM and no `redis_url` set: confirm the app starts exactly as it does today, every key resolves from the plain config file, zero behavior change, this is the "opt-in, not required" property actually holding.
- Create a real Secrets Manager entry named `devportal/jwt_secret` with a different value than what's in `config.local.yaml`; confirm the app actually starts signing sessions with the Secrets-Manager value, not the file's, proving priority order.
- Same check one tier down: an SSM parameter for a key with no Secrets Manager entry, confirm SSM wins over the plain config value.
- Temporarily break AWS credentials (wrong key) while a Secrets Manager entry exists for a key being looked up: confirm this fails loudly (`log.Fatalf` with the real AWS error) rather than silently falling through to a plain config value that happens to also be set, the "real error vs. genuinely absent" distinction actually holding.
- Confirm `aws_access_key_id`/`aws_secret_access_key` themselves are never looked up through this chain, grep the final `main.go` for `mustGetSecret("aws_access_key_id"` and confirm it doesn't exist.
