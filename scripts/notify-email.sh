# notify-email.sh — sourced by backup.sh and verify-backup.sh: notify_email
# SUBJECT BODY sends a mail through Resend to BACKUP_NOTIFY_EMAILS (comma
# separated) from EMAIL_FROM_ADDRESS. Without those or RESEND_API_KEY it only
# warns: a missing alert channel must not fail the backup itself.
notify_email() {
  if [[ -z "${BACKUP_NOTIFY_EMAILS:-}" || -z "${EMAIL_FROM_ADDRESS:-}" || -z "${RESEND_API_KEY:-}" ]]; then
    echo "warning: no email sent (RESEND_API_KEY, EMAIL_FROM_ADDRESS or BACKUP_NOTIFY_EMAILS unset): $1" >&2
    return 0
  fi
  SUBJECT="$1" BODY="$2" python3 -c '
import json, os, sys
to = [a.strip() for a in os.environ["BACKUP_NOTIFY_EMAILS"].split(",") if a.strip()]
json.dump({"from": os.environ["EMAIL_FROM_ADDRESS"], "to": to, "subject": os.environ["SUBJECT"], "text": os.environ["BODY"]}, sys.stdout)
' | curl --fail --silent --show-error --max-time 20 -X POST https://api.resend.com/emails \
    -H @<(printf 'Authorization: Bearer %s\n' "$RESEND_API_KEY") -H 'Content-Type: application/json' --data-binary @- >/dev/null
}
