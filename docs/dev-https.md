# HTTPS on a real phone during development (optional)

Nothing here is required. `make run` already serves HTTPS with a self-signed
certificate (`CATO_DEV_TLS=1`, files in `data/dev-tls/`), and the test suite runs
over HTTPS without any setup (`e2e/README.md`). These steps only matter when you
want to install the PWA or use the service worker on a physical device.

The dev certificate covers `localhost`, `127.0.0.1`, `::1`, `cato.test`, this
machine's hostname (and `<hostname>.local`) and its current LAN addresses. It is
regenerated when it nears expiry (30-day lifetime) or when those names change.

## Option 1: accept the warning

Open `https://<your-LAN-IP>:7080` on the phone and accept the warning. Pages
work, but browsers treat the origin as having a certificate error: the service
worker won't register and the PWA can't be installed.

## Option 2: mkcert

[mkcert](https://github.com/FiloSottile/mkcert) creates a local CA that you
install on the computer and the phone.

```sh
mkcert -install
mkcert -cert-file data/dev-tls/mkcert.pem -key-file data/dev-tls/mkcert-key.pem \
  localhost 127.0.0.1 "$(hostname)" <your-LAN-IP>
CATO_TLS_CERT_FILE=data/dev-tls/mkcert.pem CATO_TLS_KEY_FILE=data/dev-tls/mkcert-key.pem make run
```

Install `$(mkcert -CAROOT)/rootCA.pem` on the phone: on iOS, AirDrop it, install
the profile, then enable it under Settings → General → About → Certificate Trust
Settings. On Android, install it under Settings → Security → Encryption &
credentials → Install a certificate → CA certificate.

## Option 3: `tailscale serve`

With Tailscale on the computer and the phone, `tailscale serve --bg 7080` (after
enabling HTTPS certificates for the tailnet) publishes a trusted
`https://<machine>.<tailnet>.ts.net` origin. Tailscale terminates TLS and
forwards plain HTTP to Cato on loopback, so run Cato without its own TLS:

```sh
CATO_DEV_TLS=0 CATO_BASE_URL=https://<machine>.<tailnet>.ts.net \
  CATO_TRUSTED_PROXIES=127.0.0.1 make run
```
