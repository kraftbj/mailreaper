#!/usr/bin/env bash
# Adds user-specific triage rules via the MailReaper API.
# Run after server starts: bash scripts/seed-user-rules.sh
#
# These are sender patterns derived from analyzing 582 manually-classified
# messages. Safe to re-run — SaveRule uses upsert.

set -euo pipefail
BASE="${MAILREAPER_URL:-http://localhost:8025}"

post_rule() {
  local resp
  resp=$(curl -sf -X POST "$BASE/api/rules" \
    -H 'Content-Type: application/json' \
    -d "$1") || { echo "FAILED: $1"; return 1; }
  echo "OK: $(echo "$1" | python3 -c 'import json,sys; print(json.load(sys.stdin)["name"])')"
}

echo "=== Paper-Trail ==="

post_rule '{
  "id": "user-chase-statements",
  "name": "Chase statements & alerts \u2192 Paper-Trail",
  "enabled": true, "priority": 29, "builtin": false,
  "matchConfig": {"senderPatterns": ["*@chase.com"]},
  "expirationConfig": {"type": "classify"},
  "action": "move",
  "destinationFolder": "Folders/AI-Triage/Paper-Trail",
  "gracePeriodDays": 0
}'

post_rule '{
  "id": "user-betterment",
  "name": "Betterment \u2192 Paper-Trail",
  "enabled": true, "priority": 29, "builtin": false,
  "matchConfig": {"senderPatterns": ["*@betterment.com", "*@e.betterment.com"]},
  "expirationConfig": {"type": "classify"},
  "action": "move",
  "destinationFolder": "Folders/AI-Triage/Paper-Trail",
  "gracePeriodDays": 0
}'

post_rule '{
  "id": "user-amex-zelle",
  "name": "AmEx / Zelle payments \u2192 Paper-Trail",
  "enabled": true, "priority": 29, "builtin": false,
  "matchConfig": {"senderPatterns": ["*@welcome.americanexpress.com"]},
  "expirationConfig": {"type": "classify"},
  "action": "move",
  "destinationFolder": "Folders/AI-Triage/Paper-Trail",
  "gracePeriodDays": 0
}'

post_rule '{
  "id": "user-utcatholic",
  "name": "UT Catholic payments \u2192 Paper-Trail",
  "enabled": true, "priority": 29, "builtin": false,
  "matchConfig": {"senderPatterns": ["*@utcatholic.org"]},
  "expirationConfig": {"type": "classify"},
  "action": "move",
  "destinationFolder": "Folders/AI-Triage/Paper-Trail",
  "gracePeriodDays": 0
}'

post_rule '{
  "id": "user-kraft-fwd",
  "name": "Forwarded financial (kraft.im) \u2192 Paper-Trail",
  "enabled": true, "priority": 29, "builtin": false,
  "matchConfig": {"senderPatterns": ["*@kraft.im"]},
  "expirationConfig": {"type": "classify"},
  "action": "move",
  "destinationFolder": "Folders/AI-Triage/Paper-Trail",
  "gracePeriodDays": 0
}'

echo ""
echo "=== Notifications ==="

post_rule '{
  "id": "user-basecamp",
  "name": "Basecamp activity digests \u2192 Notifications",
  "enabled": true, "priority": 28, "builtin": false,
  "matchConfig": {"senderPatterns": ["*@basecamp.com", "*@3.basecamp.com"]},
  "expirationConfig": {"type": "classify"},
  "action": "move",
  "destinationFolder": "Folders/AI-Triage/Notifications",
  "gracePeriodDays": 0
}'

post_rule '{
  "id": "user-opensrs",
  "name": "OpenSRS alerts \u2192 Notifications",
  "enabled": true, "priority": 28, "builtin": false,
  "matchConfig": {"senderPatterns": ["*@opensrs.email", "*@opensrs.com"]},
  "expirationConfig": {"type": "classify"},
  "action": "move",
  "destinationFolder": "Folders/AI-Triage/Notifications",
  "gracePeriodDays": 0
}'

post_rule '{
  "id": "user-heb",
  "name": "HEB pharmacy \u2192 Notifications",
  "enabled": true, "priority": 28, "builtin": false,
  "matchConfig": {"senderPatterns": ["*@heb.com"]},
  "expirationConfig": {"type": "classify"},
  "action": "move",
  "destinationFolder": "Folders/AI-Triage/Notifications",
  "gracePeriodDays": 0
}'

post_rule '{
  "id": "user-usps-tracking",
  "name": "USPS tracking & delivery \u2192 Notifications",
  "enabled": true, "priority": 28, "builtin": false,
  "matchConfig": {"senderPatterns": ["*@usps.com", "*@email.informeddelivery.usps.com"]},
  "expirationConfig": {"type": "classify"},
  "action": "move",
  "destinationFolder": "Folders/AI-Triage/Notifications",
  "gracePeriodDays": 0
}'

post_rule '{
  "id": "user-github-notifications",
  "name": "GitHub notifications \u2192 Notifications",
  "enabled": true, "priority": 28, "builtin": false,
  "matchConfig": {
    "senderPatterns": ["*@github.com"],
    "subjectPatterns": ["*Dependabot*", "*requesting*permissions*", "*security*alert*", "*Actions*"]
  },
  "expirationConfig": {"type": "classify"},
  "action": "move",
  "destinationFolder": "Folders/AI-Triage/Notifications",
  "gracePeriodDays": 0
}'

echo ""
echo "=== Newsletters ==="

post_rule '{
  "id": "user-solidarity-party",
  "name": "American Solidarity Party \u2192 Newsletters",
  "enabled": true, "priority": 28, "builtin": false,
  "matchConfig": {"senderPatterns": ["*@solidarity-party.org"]},
  "expirationConfig": {"type": "classify"},
  "action": "move",
  "destinationFolder": "Folders/AI-Triage/Newsletters",
  "gracePeriodDays": 0
}'

post_rule '{
  "id": "user-statesman",
  "name": "Austin Statesman \u2192 Newsletters",
  "enabled": true, "priority": 28, "builtin": false,
  "matchConfig": {"senderPatterns": ["*@reply.statesman.com"]},
  "expirationConfig": {"type": "classify"},
  "action": "move",
  "destinationFolder": "Folders/AI-Triage/Newsletters",
  "gracePeriodDays": 0
}'

post_rule '{
  "id": "user-github-marketing",
  "name": "GitHub marketing \u2192 Promotions",
  "enabled": true, "priority": 27, "builtin": false,
  "matchConfig": {
    "senderPatterns": ["*@github.com"],
    "subjectPatterns": ["*Copilot*", "*AI*", "*Get to know*", "*Awaken*"]
  },
  "expirationConfig": {"type": "classify"},
  "action": "move",
  "destinationFolder": "Folders/AI-Triage/Promotions",
  "gracePeriodDays": 0
}'

echo ""
echo "=== Promotions ==="

post_rule '{
  "id": "user-chase-marketing",
  "name": "Chase rewards marketing \u2192 Promotions",
  "enabled": true, "priority": 28, "builtin": false,
  "matchConfig": {"senderPatterns": ["*@mcmap.chase.com"]},
  "expirationConfig": {"type": "classify"},
  "action": "move",
  "destinationFolder": "Folders/AI-Triage/Promotions",
  "gracePeriodDays": 0
}'

post_rule '{
  "id": "user-fandango",
  "name": "Fandango \u2192 Promotions",
  "enabled": true, "priority": 29, "builtin": false,
  "matchConfig": {"senderPatterns": ["*@movies.fandango.com", "*@fandango.com"]},
  "expirationConfig": {"type": "classify"},
  "action": "move",
  "destinationFolder": "Folders/AI-Triage/Promotions",
  "gracePeriodDays": 0
}'

post_rule '{
  "id": "user-radiant-plumbing",
  "name": "Radiant Plumbing \u2192 Promotions",
  "enabled": true, "priority": 29, "builtin": false,
  "matchConfig": {"senderPatterns": ["*@radiantplumbing.servicetitanmail.io"]},
  "expirationConfig": {"type": "classify"},
  "action": "move",
  "destinationFolder": "Folders/AI-Triage/Promotions",
  "gracePeriodDays": 0
}'

post_rule '{
  "id": "user-codecademy",
  "name": "Codecademy \u2192 Promotions",
  "enabled": true, "priority": 29, "builtin": false,
  "matchConfig": {"senderPatterns": ["*@itr.mail.codecademy.com", "*@codecademy.com"]},
  "expirationConfig": {"type": "classify"},
  "action": "move",
  "destinationFolder": "Folders/AI-Triage/Promotions",
  "gracePeriodDays": 0
}'

post_rule '{
  "id": "user-foursquare",
  "name": "Foursquare \u2192 Promotions",
  "enabled": true, "priority": 29, "builtin": false,
  "matchConfig": {"senderPatterns": ["*@foursquare.com"]},
  "expirationConfig": {"type": "classify"},
  "action": "move",
  "destinationFolder": "Folders/AI-Triage/Promotions",
  "gracePeriodDays": 0
}'

post_rule '{
  "id": "user-grubhub",
  "name": "Grubhub \u2192 Promotions",
  "enabled": true, "priority": 29, "builtin": false,
  "matchConfig": {"senderPatterns": ["*@a.grubhub.com", "*@grubhub.com"]},
  "expirationConfig": {"type": "classify"},
  "action": "move",
  "destinationFolder": "Folders/AI-Triage/Promotions",
  "gracePeriodDays": 0
}'

post_rule '{
  "id": "user-airbnb",
  "name": "Airbnb marketing \u2192 Promotions",
  "enabled": true, "priority": 29, "builtin": false,
  "matchConfig": {"senderPatterns": ["*@airbnb.com"]},
  "expirationConfig": {"type": "classify"},
  "action": "move",
  "destinationFolder": "Folders/AI-Triage/Promotions",
  "gracePeriodDays": 0
}'

post_rule '{
  "id": "user-marriott",
  "name": "Marriott / Ritz-Carlton marketing \u2192 Promotions",
  "enabled": true, "priority": 29, "builtin": false,
  "matchConfig": {"senderPatterns": ["*@email-marriott.com"]},
  "expirationConfig": {"type": "classify"},
  "action": "move",
  "destinationFolder": "Folders/AI-Triage/Promotions",
  "gracePeriodDays": 0
}'

echo ""
echo "=== Done ==="
echo "New builtin rules (travel alerts, LLM classifiers) were seeded on server start."
echo "Enable them via the dashboard if not already enabled."
