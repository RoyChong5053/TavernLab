#!/usr/bin/env bash
# Deploy the current local master to the m64 prod box (tavernlab.service).
#
# Loop, proven on 2026-10-29:
#   1. push local master to origin (so the box and the repo agree)
#   2. ssh m64: fast-forward pull -> go build -> sudo systemctl restart
#   3. health-check the port
#
# Usage: ./deploy.sh [branch] [host]
#   ./deploy.sh master m64
set -euo pipefail

BRANCH="${1:-master}"
HOST="${2:-m64}"
PORT="${PORT:-8888}"

cd "$(dirname "$0")"

echo ">> 1. push ${BRANCH} to origin"
git push origin "${BRANCH}"

echo ">> 2. pull + build + restart on ${HOST}"
ssh -o BatchMode=yes "${HOST}" <<EOF
set -euo pipefail
cd /home/roychong/app/TavernLab
# Fast-forward only: refuse to clobber any local edits on the box.
git fetch origin "${BRANCH}"
git merge --ff-only "origin/${BRANCH}"
go build -o tavernlab . && echo "build OK"
sudo -n systemctl restart tavernlab
sleep 2
systemctl is-active tavernlab || exit 1
EOF

echo ">> 3. health check on :${PORT}"
code="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:${PORT}/api/settings")"
echo "   /api/settings -> HTTP ${code}"
[ "${code}" != "000" ] || { echo "deploy FAILED: no response"; exit 1; }

echo ">> deploy to ${HOST} complete"
