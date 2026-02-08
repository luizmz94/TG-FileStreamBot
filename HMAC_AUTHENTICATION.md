# HMAC Authentication for /direct Route

## Overview
The `/direct/:messageID` route now supports HMAC-SHA256 authentication to prevent unauthorized access to streamed files.

## Backend Configuration (Go)

### 1. Add to your `fsb.env`:
```env
STREAM_SECRET=your-secret-key-min-32-chars-recommended
```

### 2. Generate a secure secret:
```bash
# Generate a random 64-character hex string
openssl rand -hex 32

# Or use any secure random generator
# Example: 8f7d3e2a9c1b4f6e8d5a7c9b3e1f4a6d8c2b5e7f9a3d6c8e1b4f7a9c2e5d8f
```

### 3. Restart your application
The HMAC validation will be automatically enabled when `STREAM_SECRET` is set.

## Security Features

✅ **Backward Compatible**: If `STREAM_SECRET` is not configured, the route works without authentication (useful for testing/migration)

✅ **Expiration**: URLs expire after the specified time (default: depends on client implementation)

✅ **Tamper-Proof**: Any modification to messageID or timestamp invalidates the signature

✅ **Zero Performance Impact**: HMAC validation takes ~0.1ms, streaming performance is unchanged

## URL Format

Protected URLs must include two query parameters:
```
/direct/{messageID}?sig={signature}&exp={expiration}

Example:
/direct/123?sig=8f7d3e2a9c1b4f6e8d5a7c9b3e1f4a6d8c2b5e7f9a3d6c8e1b4f7a9c2e5d8f&exp=1707379200
```

Where:
- `sig`: HMAC-SHA256 signature of `"messageID:exp"`
- `exp`: Unix timestamp when the URL expires

## Client Implementation

### Next.js / Node.js
See `nextjs-hmac-implementation.txt` for complete implementation guide.

### iOS / macOS (Swift)
```swift
import CryptoKit

func signStreamURL(messageId: Int, secret: String, expiresIn: Int = 3600) -> String {
    let exp = Int(Date().timeIntervalSince1970) + expiresIn
    let data = "\(messageId):\(exp)"
    
    let key = SymmetricKey(data: Data(secret.utf8))
    let signature = HMAC<SHA256>.authenticationCode(
        for: Data(data.utf8),
        using: key
    )
    let sig = Data(signature).map { String(format: "%02x", $0) }.joined()
    
    return "https://your-api.com/direct/\(messageId)?sig=\(sig)&exp=\(exp)"
}

// Usage
let url = signStreamURL(messageId: 123, secret: "your-secret-key")
```

### Python
```python
import hmac
import hashlib
import time

def sign_stream_url(message_id: int, secret: str, expires_in: int = 3600) -> str:
    exp = int(time.time()) + expires_in
    data = f"{message_id}:{exp}"
    
    signature = hmac.new(
        secret.encode(),
        data.encode(),
        hashlib.sha256
    ).hexdigest()
    
    return f"https://your-api.com/direct/{message_id}?sig={signature}&exp={exp}"

# Usage
url = sign_stream_url(123, "your-secret-key")
```

## Error Responses

### 401 Unauthorized
```json
{
  "error": "unauthorized: invalid or expired signature"
}
```

Causes:
- Missing `sig` or `exp` parameters
- Invalid signature (wrong secret or tampered data)
- Expired timestamp

## Testing

### Test without authentication (if STREAM_SECRET not set):
```bash
curl -I http://localhost:8080/direct/123
```

### Test with authentication:
```bash
# Generate signature (example with Python)
python3 << EOF
import hmac, hashlib, time
secret = "your-secret-key"
msg_id = 123
exp = int(time.time()) + 3600
data = f"{msg_id}:{exp}"
sig = hmac.new(secret.encode(), data.encode(), hashlib.sha256).hexdigest()
print(f"http://localhost:8080/direct/{msg_id}?sig={sig}&exp={exp}")
EOF

# Use the generated URL
curl -I "http://localhost:8080/direct/123?sig=...&exp=..."
```

## Migration Guide

1. **Phase 1** - Deploy backend with STREAM_SECRET **not set**
   - Current clients continue working
   - No breaking changes

2. **Phase 2** - Update clients to generate signed URLs
   - Test with both signed and unsigned URLs
   - Verify all clients are updated

3. **Phase 3** - Set STREAM_SECRET in production
   - HMAC validation becomes active
   - Unsigned requests will be rejected

## Logging

HMAC validation failures are logged with:
```
level=WARN msg="HMAC validation failed" messageID=123 clientIP=x.x.x.x error="signature expired"
```

Monitor these logs to detect:
- Potential attacks (invalid signatures)
- Clock drift issues (expired before use)
- Client implementation problems

## Recommendations

- ✅ Use minimum 32-character random secret
- ✅ Store secret in environment variables, never in code
- ✅ Use same secret across all clients
- ✅ Rotate secret periodically (requires client updates)
- ✅ Set reasonable expiration (1-24 hours depending on use case)
- ❌ Don't expose secret in client-side code (use server-side generation)
- ❌ Don't reuse secrets from other systems
