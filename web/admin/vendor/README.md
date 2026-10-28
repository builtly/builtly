# Vendored admin UI dependencies

Downloaded at build-independent check-in time so `/admin` stays CDN-free and `go:embed`-able under CSP `script-src 'self'; style-src 'self'`.

| File | Package | License |
|------|---------|---------|
| `pico.min.css` | [@picocss/pico](https://picocss.com) 2.0.6 | MIT |
| `alpine.csp.min.js` | [@alpinejs/csp](https://alpinejs.dev/advanced/csp) 3.14.8 | MIT |

Use the **CSP** Alpine build so admin CSP does not need `'unsafe-eval'`. After injecting HTML with Alpine directives, call `Alpine.initTree(el)`.
