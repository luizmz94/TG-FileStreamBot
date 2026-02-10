# Firebase Stream Authentication

This project now supports a high-performance auth flow for `/direct/:message_id`:

1. The client authenticates with Firebase (normal app login).
2. The client sends the Firebase ID token once to:
   - `POST /auth/firebase/exchange`
   - Header: `Authorization: Bearer <firebase_id_token>`
3. The stream server validates Firebase JWT and returns a short-lived `stream_token`.
4. All subsequent `/direct/:message_id` requests use only the local `stream_token`:
   - Query: `?st=<stream_token>`
   - Header: `x-stream-token: <stream_token>`
   - Header: `Authorization: Bearer <stream_token>`
   - Or HttpOnly cookie set by exchange endpoint.

No Firebase verification happens on `/direct` requests.

## Environment variables

```env
# Optional migration mode for old signed links
STREAM_SECRET=your-existing-hmac-secret
```

Non-secret values (`FIREBASE_PROJECT_ID`, session TTL, cookie name/flags, etc.) are now hardcoded in `config/config.go`.

## Quick test

```bash
# 1) Exchange Firebase token
curl -X POST "https://your-stream-host/auth/firebase/exchange" \
  -H "Authorization: Bearer <firebase_id_token>"

# Response:
# {
#   "stream_token": "...",
#   "expires_at": 1739333282,
#   ...
# }

# 2) Stream using local token
curl "https://your-stream-host/direct/123?st=<stream_token>"
```

## Notes

- Session tokens are stored in-memory (fast, O(1) lookup).
- Restarting the service invalidates active stream sessions.
- For horizontal scaling, use a shared store (e.g. Redis) for session tokens.
