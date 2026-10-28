#!/usr/bin/env bash
# Deploy the hosted clusterwand MCP server to Cloud Run.
#
#   sites/ursa-major/deploy/deploy.sh plan     # show what would be created/changed (default; changes nothing)
#   sites/ursa-major/deploy/deploy.sh apply    # do it (asks before each billable or IAM change)
#
# Needs: gcloud logged in as a project owner, docker. Reads sites/ursa-major/deploy/env (not in git):
#   PROJECT=ucr-ursa-major-hpc-cluster  REGION=us-central1
#   CONFIG=path/to/config.yaml  USERS=path/to/users.yaml
#   CLIENT_JSON=path/to/oauth-client.json   (downloaded when creating the Web OAuth client; see docs/DEPLOY.md)
#   MIN_INSTANCES=1   (default 1: always warm, ~$10/month idle; 0 = no idle cost, cold starts)
set -euo pipefail
cd "$(dirname "$0")/../../.."
MODE=${1:-plan}
[ -f sites/ursa-major/deploy/env ] || { echo "sites/ursa-major/deploy/env missing (see the header of this script)"; exit 2; }
# shellcheck disable=SC1091
. sites/ursa-major/deploy/env
: "${PROJECT:?}" "${REGION:?}" "${CONFIG:?}" "${USERS:?}"
SERVICE=clusterwand-mcp
SA=clusterwand-mcp@${PROJECT}.iam.gserviceaccount.com
REPO=${REGION}-docker.pkg.dev/${PROJECT}/clusterwand
BUCKET=${PROJECT}-clusterwand-data
VERSION=$(git describe --tags --always --dirty --match 'v[0-9]*')  # agent-v* tags share commits
IMAGE=${REPO}/clusterwand:${VERSION}

say()  { printf '\n== %s\n' "$*"; }
run()  { if [ "$MODE" = apply ]; then echo "+ $*"; "$@"; else echo "would run: $*"; fi; }
ask()  { [ "$MODE" = apply ] || return 0; read -r -p "$1 [y/N] " a; [ "$a" = y ] || { echo "skipped"; return 1; }; }
have() { "$@" >/dev/null 2>&1; }

say "Plan for $SERVICE in $PROJECT ($REGION), image $IMAGE"

say "1. APIs"
for api in run.googleapis.com artifactregistry.googleapis.com secretmanager.googleapis.com; do
  if gcloud services list --enabled --project "$PROJECT" --format='value(config.name)' | grep -qx "$api"; then echo "ok   $api"
  else ask "enable $api?" && run gcloud services enable "$api" --project "$PROJECT"; fi
done

say "2. Service account (no cluster access: it never SSHes; it reads secrets and the data bucket)"
if have gcloud iam service-accounts describe "$SA" --project "$PROJECT"; then echo "ok   $SA"
else ask "create $SA?" && run gcloud iam service-accounts create clusterwand-mcp --project "$PROJECT" --display-name "clusterwand MCP server (no cluster access)"; fi

say "3. Artifact Registry repo"
if have gcloud artifacts repositories describe clusterwand --location "$REGION" --project "$PROJECT"; then echo "ok   $REPO"
else ask "create repo clusterwand?" && run gcloud artifacts repositories create clusterwand --repository-format docker --location "$REGION" --project "$PROJECT"; fi

say "4. Data bucket (sealed sessions, keys, ledgers; AES-GCM inside, private, versioned)"
if have gcloud storage buckets describe "gs://$BUCKET"; then echo "ok   gs://$BUCKET"
else ask "create gs://$BUCKET?" && { run gcloud storage buckets create "gs://$BUCKET" --project "$PROJECT" --location "$REGION" --uniform-bucket-level-access --public-access-prevention;
  run gcloud storage buckets update "gs://$BUCKET" --versioning; }; fi
run gcloud storage buckets add-iam-policy-binding "gs://$BUCKET" --member "serviceAccount:$SA" --role roles/storage.objectUser

say "4b. Staging bucket (file uploads and download links, SPEC 18; private, 7-day delete, no versioning)"
STAGING=${PROJECT}-clusterwand-staging
AGENT_ORIGIN="https://ursa-agent-$(gcloud projects describe "$PROJECT" --format='value(projectNumber)' 2>/dev/null)-${REGION}.run.app"
if have gcloud storage buckets describe "gs://$STAGING"; then echo "ok   gs://$STAGING"
else ask "create gs://$STAGING?" && run gcloud storage buckets create "gs://$STAGING" --project "$PROJECT" --location "$REGION" --uniform-bucket-level-access --public-access-prevention --soft-delete-duration=0; fi
if [ "$MODE" = apply ]; then
  printf '{"rule":[{"action":{"type":"Delete"},"condition":{"age":7}}]}' > /tmp/clusterwand-staging-lifecycle.json
  printf '[{"origin":["%s"],"method":["PUT"],"responseHeader":["Content-Type","x-goog-content-length-range"],"maxAgeSeconds":3600}]' "$AGENT_ORIGIN" > /tmp/clusterwand-staging-cors.json
  run gcloud storage buckets update "gs://$STAGING" --lifecycle-file=/tmp/clusterwand-staging-lifecycle.json --cors-file=/tmp/clusterwand-staging-cors.json
fi
run gcloud storage buckets add-iam-policy-binding "gs://$STAGING" --member "serviceAccount:$SA" --role roles/storage.objectAdmin
# signs download/upload links as itself through IAM signBlob (no key file)
run gcloud iam service-accounts add-iam-policy-binding "$SA" --project "$PROJECT" --member "serviceAccount:$SA" --role roles/iam.serviceAccountTokenCreator

say "5. Secrets (values never printed)"
for s in clusterwand-secret-key clusterwand-google-client-secret clusterwand-config clusterwand-users; do
  if have gcloud secrets describe "$s" --project "$PROJECT"; then echo "ok   $s"
  else echo "MISSING $s"; fi
done
if [ "$MODE" = apply ]; then
  have gcloud secrets describe clusterwand-secret-key --project "$PROJECT" || \
    { ask "create clusterwand-secret-key (32 random bytes)?" && openssl rand -base64 32 | tr -d '\n' | gcloud secrets create clusterwand-secret-key --project "$PROJECT" --data-file=-; }
  if ! have gcloud secrets describe clusterwand-google-client-secret --project "$PROJECT"; then
    if [ -n "${CLIENT_JSON:-}" ] && [ -f "$CLIENT_JSON" ]; then
      ask "create clusterwand-google-client-secret from $CLIENT_JSON (value not printed)?" && \
        python3 -c "import json,sys;print(json.load(open(sys.argv[1]))['web']['client_secret'],end='')" "$CLIENT_JSON" | \
        gcloud secrets create clusterwand-google-client-secret --project "$PROJECT" --replication-policy automatic --data-file=-
    else
      echo "set CLIENT_JSON in sites/ursa-major/deploy/env to the downloaded OAuth client JSON, then apply again"
    fi
  fi
  if [ -n "${CLIENT_JSON:-}" ] && [ -f "$CLIENT_JSON" ]; then
    cid=$(python3 -c "import json,sys;print(json.load(open(sys.argv[1]))['web']['client_id'])" "$CLIENT_JSON")
    grep -q "$cid" "$CONFIG" || echo "WARNING: $CONFIG server.google_client_id does not match $CLIENT_JSON"
  fi
  ask "upload $CONFIG as a new version of clusterwand-config?" && { have gcloud secrets describe clusterwand-config --project "$PROJECT" || gcloud secrets create clusterwand-config --project "$PROJECT" --replication-policy automatic; gcloud secrets versions add clusterwand-config --project "$PROJECT" --data-file="$CONFIG"; }
  ask "upload $USERS as a new version of clusterwand-users?" && { have gcloud secrets describe clusterwand-users --project "$PROJECT" || gcloud secrets create clusterwand-users --project "$PROJECT" --replication-policy automatic; gcloud secrets versions add clusterwand-users --project "$PROJECT" --data-file="$USERS"; }
  for s in clusterwand-secret-key clusterwand-google-client-secret clusterwand-config clusterwand-users; do
    have gcloud secrets describe "$s" --project "$PROJECT" && gcloud secrets add-iam-policy-binding "$s" --project "$PROJECT" --member "serviceAccount:$SA" --role roles/secretmanager.secretAccessor >/dev/null
  done
fi

say "6. Build and push the image"
run docker build --build-arg VERSION="$VERSION" -t "$IMAGE" .
run gcloud auth configure-docker "${REGION}-docker.pkg.dev" --quiet
run docker push "$IMAGE"

say "7. Deploy Cloud Run (public URL; every MCP request needs a clusterwand token from Google sign-in)"
ask "deploy $SERVICE?" && run gcloud run deploy "$SERVICE" --project "$PROJECT" --region "$REGION" --image "$IMAGE" \
  --service-account "$SA" --allow-unauthenticated --min-instances "${MIN_INSTANCES:-1}" --max-instances 1 --concurrency 40 \
  --cpu 1 --memory 512Mi --timeout 900 --execution-environment gen2 \
  --set-secrets "CLUSTERWAND_SECRET_KEY=clusterwand-secret-key:latest,CLUSTERWAND_GOOGLE_CLIENT_SECRET=clusterwand-google-client-secret:latest,/config/config.yaml=clusterwand-config:latest,/users/users.yaml=clusterwand-users:latest" \
  --add-volume "name=data,type=cloud-storage,bucket=$BUCKET" --add-volume-mount "volume=data,mount-path=/data"

URL="https://${SERVICE}-$(gcloud projects describe "$PROJECT" --format='value(projectNumber)')-${REGION}.run.app"
say "Done ($MODE). Service URL: $URL (deterministic: set it as server.base_url and add $URL/oauth/google/callback to the OAuth client BEFORE the first deploy)."
[ "$MODE" = apply ] && curl -fsS "$URL/health" && echo
