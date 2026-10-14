// b64.js — base64url <-> bytes for WebAuthn and Web Push (no DOM).

export function toBytes(b64url) {
  const s = b64url.replace(/-/g, '+').replace(/_/g, '/');
  const bin = atob(s + '='.repeat((4 - (s.length % 4)) % 4));
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

export function fromBytes(buf) {
  const bytes = buf instanceof Uint8Array ? buf : new Uint8Array(buf);
  let bin = '';
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

// creationOptions / requestOptions turn the server's JSON options into the
// ArrayBuffer-based objects navigator.credentials expects.
export function creationOptions(o) {
  return {
    ...o,
    challenge: toBytes(o.challenge),
    user: { ...o.user, id: toBytes(o.user.id) },
    excludeCredentials: (o.excludeCredentials || []).map(c => ({ ...c, id: toBytes(c.id) })),
  };
}

export function requestOptions(o) {
  return { ...o, challenge: toBytes(o.challenge), allowCredentials: (o.allowCredentials || []).map(c => ({ ...c, id: toBytes(c.id) })) };
}

// credentialJSON serialises a PublicKeyCredential for the server.
export function credentialJSON(cred) {
  const r = cred.response;
  const out = { id: cred.id, rawId: fromBytes(cred.rawId), type: cred.type, response: { clientDataJSON: fromBytes(r.clientDataJSON) } };
  if (r.attestationObject) {
    out.response.attestationObject = fromBytes(r.attestationObject);
    out.response.transports = typeof r.getTransports === 'function' ? r.getTransports() : [];
  }
  if (r.authenticatorData) {
    out.response.authenticatorData = fromBytes(r.authenticatorData);
    out.response.signature = fromBytes(r.signature);
    if (r.userHandle) out.response.userHandle = fromBytes(r.userHandle);
  }
  return out;
}

// passkeysPossible: the browser supports them and the host is a name (an
// IP address can't be a WebAuthn relying party).
export function passkeysPossible(loc = globalThis.location) {
  return typeof globalThis.PublicKeyCredential === 'function' && !/^[\d.]+$|^\[|:/.test(loc.hostname);
}
